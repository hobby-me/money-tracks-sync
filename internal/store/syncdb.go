package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// SyncDB — лог правок одной учётки: ops (упорядоченный журнал) + rows
// (материализованное LWW-состояние). Одна SQLite на учётку — изоляция,
// которую нельзя нарушить забытым WHERE.
type SyncDB struct {
	db         *sql.DB
	InstanceID string
}

const syncSchema = `
CREATE TABLE IF NOT EXISTS ops (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  op_id TEXT UNIQUE NOT NULL, device_id TEXT NOT NULL,
  entity TEXT NOT NULL, entity_uuid TEXT NOT NULL, hlc TEXT NOT NULL,
  payload TEXT NOT NULL, received_at INT NOT NULL);
CREATE TABLE IF NOT EXISTS rows_state (
  entity TEXT NOT NULL, entity_uuid TEXT NOT NULL,
  hlc TEXT NOT NULL, device_id TEXT NOT NULL, payload TEXT NOT NULL,
  PRIMARY KEY(entity, entity_uuid));
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

// Pool лениво открывает sync.db учёток и держит их открытыми.
type Pool struct {
	dataDir string
	mu      sync.Mutex
	open    map[string]*SyncDB
}

func NewPool(dataDir string) *Pool {
	return &Pool{dataDir: dataDir, open: map[string]*SyncDB{}}
}

func (p *Pool) DataDir() string { return p.dataDir }

func (p *Pool) AccountDir(accountID string) string {
	return filepath.Join(p.dataDir, "acc", accountID)
}

func (p *Pool) PhotosDir(accountID string) string {
	return filepath.Join(p.AccountDir(accountID), "photos")
}

func (p *Pool) Get(accountID string) (*SyncDB, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.open[accountID]; ok {
		return s, nil
	}
	dir := p.AccountDir(accountID)
	if err := os.MkdirAll(filepath.Join(dir, "photos"), 0o700); err != nil {
		return nil, err
	}
	db, err := openSQLite(filepath.Join(dir, "sync.db"))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(syncSchema); err != nil {
		db.Close()
		return nil, err
	}
	s := &SyncDB{db: db}
	if err := s.loadInstanceID(); err != nil {
		db.Close()
		return nil, err
	}
	p.open[accountID] = s
	return s, nil
}

func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.open {
		s.db.Close()
	}
	p.open = map[string]*SyncDB{}
}

// loadInstanceID читает случайную личность лога, создавая при первом открытии.
// Клиент по её смене замечает пересозданный/восстановленный сервер.
func (s *SyncDB) loadInstanceID() error {
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'instance_id'`).Scan(&s.InstanceID)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	id := newULID()
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES('instance_id', ?)
		 ON CONFLICT(key) DO NOTHING`, id); err != nil {
		return err
	}
	return s.db.QueryRow(`SELECT value FROM meta WHERE key = 'instance_id'`).Scan(&s.InstanceID)
}

type Op struct {
	Seq        int64  `json:"seq,omitempty"`
	OpID       string `json:"op_id"`
	DeviceID   string `json:"device_id"`
	Entity     string `json:"entity"`
	EntityUUID string `json:"entity_uuid"`
	HLC        string `json:"hlc"`
	Payload    string `json:"payload"`
}

type Ack struct {
	OpID string `json:"op_id"`
	Seq  int64  `json:"seq"`
}

// newerThan — правило LWW: сперва hlc (строка фиксированной ширины,
// лексикографически), при равенстве — device_id, лишь бы детерминированно.
func newerThan(hlc, dev, oldHlc, oldDev string) bool {
	if hlc != oldHlc {
		return hlc > oldHlc
	}
	return dev > oldDev
}

// Push принимает пачку правок одной транзакцией: вставка в ops с
// идемпотентностью по op_id (повтор отвечает прежним seq) + LWW-апдейт rows.
// BEGIN IMMEDIATE сериализует конкурентный пуш двух устройств.
func (s *SyncDB) Push(ops []Op) ([]Ack, int64, error) {
	now := time.Now().UTC().UnixMilli()
	// Конкурентный пуш двух устройств сериализуется пулом: соединение с
	// базой одно (SetMaxOpenConns(1)), второй Begin ждёт первого, плюс
	// _txlock=immediate в DSN — писатель берёт замок сразу.
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()

	acks := make([]Ack, 0, len(ops))
	for i := range ops {
		op := &ops[i]
		var seq int64
		err := tx.QueryRow(`SELECT seq FROM ops WHERE op_id = ?`, op.OpID).Scan(&seq)
		if err == nil {
			acks = append(acks, Ack{OpID: op.OpID, Seq: seq})
			continue
		}
		if err != sql.ErrNoRows {
			return nil, 0, err
		}
		res, err := tx.Exec(
			`INSERT INTO ops(op_id, device_id, entity, entity_uuid, hlc, payload, received_at)
			 VALUES(?,?,?,?,?,?,?)`,
			op.OpID, op.DeviceID, op.Entity, op.EntityUUID, op.HLC, op.Payload, now)
		if err != nil {
			return nil, 0, err
		}
		seq, err = res.LastInsertId()
		if err != nil {
			return nil, 0, err
		}
		if err := lwwUpsert(tx, op); err != nil {
			return nil, 0, err
		}
		acks = append(acks, Ack{OpID: op.OpID, Seq: seq})
	}
	latest, err := latestSeqTx(tx)
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return acks, latest, nil
}

func lwwUpsert(tx *sql.Tx, op *Op) error {
	var oldHlc, oldDev string
	err := tx.QueryRow(
		`SELECT hlc, device_id FROM rows_state WHERE entity = ? AND entity_uuid = ?`,
		op.Entity, op.EntityUUID).Scan(&oldHlc, &oldDev)
	if err == sql.ErrNoRows {
		_, err = tx.Exec(
			`INSERT INTO rows_state(entity, entity_uuid, hlc, device_id, payload) VALUES(?,?,?,?,?)`,
			op.Entity, op.EntityUUID, op.HLC, op.DeviceID, op.Payload)
		return err
	}
	if err != nil {
		return err
	}
	if !newerThan(op.HLC, op.DeviceID, oldHlc, oldDev) {
		return nil // уже есть новее — правка легла в лог, но состояние не трогает
	}
	_, err = tx.Exec(
		`UPDATE rows_state SET hlc = ?, device_id = ?, payload = ?
		 WHERE entity = ? AND entity_uuid = ?`,
		op.HLC, op.DeviceID, op.Payload, op.Entity, op.EntityUUID)
	return err
}

func latestSeqTx(tx *sql.Tx) (int64, error) {
	var latest sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM ops`).Scan(&latest); err != nil {
		return 0, err
	}
	return latest.Int64, nil
}

func (s *SyncDB) LatestSeq() (int64, error) {
	var latest sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM ops`).Scan(&latest); err != nil {
		return 0, err
	}
	return latest.Int64, nil
}

// Pull отдаёт правки после курсора, по seq, не больше limit.
// latest_seq читается той же транзакцией, что и страница.
func (s *SyncDB) Pull(after int64, limit int) ([]Op, int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(
		`SELECT seq, op_id, device_id, entity, entity_uuid, hlc, payload
		 FROM ops WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Op, 0, limit)
	for rows.Next() {
		var op Op
		if err := rows.Scan(&op.Seq, &op.OpID, &op.DeviceID, &op.Entity,
			&op.EntityUUID, &op.HLC, &op.Payload); err != nil {
			rows.Close()
			return nil, 0, err
		}
		out = append(out, op)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	latest, err := latestSeqTx(tx)
	if err != nil {
		return nil, 0, err
	}
	return out, latest, tx.Commit()
}

type Row struct {
	Entity     string `json:"entity"`
	EntityUUID string `json:"entity_uuid"`
	HLC        string `json:"hlc"`
	DeviceID   string `json:"device_id"`
	Payload    string `json:"payload"`
}

// Snapshot — материализованное состояние целиком + latest_seq одной транзакцией:
// новое устройство не проигрывает лог с нуля.
func (s *SyncDB) Snapshot() ([]Row, int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(
		`SELECT entity, entity_uuid, hlc, device_id, payload
		 FROM rows_state ORDER BY entity, entity_uuid`)
	if err != nil {
		return nil, 0, err
	}
	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Entity, &r.EntityUUID, &r.HLC, &r.DeviceID, &r.Payload); err != nil {
			rows.Close()
			return nil, 0, err
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	latest, err := latestSeqTx(tx)
	if err != nil {
		return nil, 0, err
	}
	return out, latest, tx.Commit()
}

type EntitySum struct {
	Count int    `json:"count"`
	Hash  string `json:"hash"`
}

// Checksum — по сущности: SHA-256 от конкатенации "uuid:hlc:device_id\n",
// строки отсортированы по uuid, tombstone'ы включены. Payload в хэш не
// входит намеренно: при идемпотентном применении равенство (uuid, hlc,
// device) влечёт равенство содержимого.
func (s *SyncDB) Checksum() (map[string]EntitySum, error) {
	rows, err := s.db.Query(
		`SELECT entity, entity_uuid, hlc, device_id
		 FROM rows_state ORDER BY entity, entity_uuid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]EntitySum{}
	var curEntity string
	var count int
	h := sha256.New()
	flush := func() {
		if curEntity == "" {
			return
		}
		out[curEntity] = EntitySum{Count: count, Hash: hex.EncodeToString(h.Sum(nil))}
	}
	line := make([]byte, 0, 128)
	for rows.Next() {
		var entity, uuid, hlc, dev string
		if err := rows.Scan(&entity, &uuid, &hlc, &dev); err != nil {
			return nil, err
		}
		if entity != curEntity {
			flush()
			curEntity, count = entity, 0
			h = sha256.New()
		}
		count++
		line = line[:0]
		line = append(line, uuid...)
		line = append(line, ':')
		line = append(line, hlc...)
		line = append(line, ':')
		line = append(line, dev...)
		line = append(line, '\n')
		h.Write(line)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	flush()
	return out, nil
}

// ChecksumLine — строка контрольной суммы; вынесена ради бенчмарка
// (docs/perf/checksum.md) и одинакового формата с клиентом.
func ChecksumLine(buf []byte, uuid, hlc, dev string) []byte {
	buf = append(buf, uuid...)
	buf = append(buf, ':')
	buf = append(buf, hlc...)
	buf = append(buf, ':')
	buf = append(buf, dev...)
	buf = append(buf, '\n')
	return buf
}

// SortedEntities — имена сущностей в детерминированном порядке (для CLI).
func SortedEntities(m map[string]EntitySum) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
