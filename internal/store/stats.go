package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// InviteInfo — код приглашения глазами оператора: самого кода нет (в базе
// только хэш), есть состояние и срок.
type InviteInfo struct {
	CodeHash   string
	AccountID  string
	GrantAdmin bool
	ExpiresAt  time.Time
	UsedAt     *time.Time
}

// Pending — код ещё можно погасить.
func (i InviteInfo) Pending(now time.Time) bool {
	return i.UsedAt == nil && i.ExpiresAt.After(now)
}

// ListInvites — все коды учётки (или всех, если accountID пуст), свежие сверху.
func (a *AccountsDB) ListInvites(accountID string) ([]InviteInfo, error) {
	q := `SELECT code_hash, account_id, grant_admin, expires_at, used_at FROM invites`
	args := []any{}
	if accountID != "" {
		q += ` WHERE account_id = ?`
		args = append(args, accountID)
	}
	q += ` ORDER BY expires_at DESC`
	rows, err := a.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InviteInfo
	for rows.Next() {
		var inv InviteInfo
		var admin int
		var exp int64
		var used sql.NullInt64
		if err := rows.Scan(&inv.CodeHash, &inv.AccountID, &admin, &exp, &used); err != nil {
			return nil, err
		}
		inv.GrantAdmin = admin == 1
		inv.ExpiresAt = time.UnixMilli(exp).UTC()
		if used.Valid {
			t := time.UnixMilli(used.Int64).UTC()
			inv.UsedAt = &t
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// DeleteInvite отзывает непогашенный код. Погашенный трогать нельзя — это
// история подключения устройства.
func (a *AccountsDB) DeleteInvite(codeHash string) error {
	res, err := a.db.Exec(`DELETE FROM invites WHERE code_hash = ? AND used_at IS NULL`, codeHash)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeInvites удаляет протухшие и погашенные коды старше keep.
func (a *AccountsDB) PurgeInvites(keep time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-keep).UnixMilli()
	res, err := a.db.Exec(
		`DELETE FROM invites WHERE expires_at < ? AND (used_at IS NULL OR used_at < ?)`,
		cutoff, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// AccountStats — что лежит в acc/<id>/: объёмы и свежесть без интерпретации.
type AccountStats struct {
	Exists      bool // sync.db ещё не создан — устройство ни разу не синкалось
	InstanceID  string
	LatestSeq   int64
	Rows        int64
	Entities    map[string]EntitySum
	LastReceive time.Time // нулевое, если ops пуст
	DBBytes     int64
	PhotoCount  int
	PhotoBytes  int64
}

// Stats читает статистику учётки, не создавая её папку: TUI смотрит на
// учётки, к которым ещё никто не подключался.
func (p *Pool) Stats(accountID string) (AccountStats, error) {
	var st AccountStats
	dbPath := filepath.Join(p.AccountDir(accountID), "sync.db")
	fi, err := os.Stat(dbPath)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Exists = true
	st.DBBytes = fi.Size()
	for _, suffix := range []string{"-wal", "-shm"} {
		if wfi, err := os.Stat(dbPath + suffix); err == nil {
			st.DBBytes += wfi.Size()
		}
	}

	s, err := p.Get(accountID)
	if err != nil {
		return st, err
	}
	st.InstanceID = s.InstanceID
	if st.LatestSeq, err = s.LatestSeq(); err != nil {
		return st, err
	}
	var lastMs sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(received_at) FROM ops`).Scan(&lastMs); err != nil {
		return st, err
	}
	if lastMs.Valid {
		st.LastReceive = time.UnixMilli(lastMs.Int64).UTC()
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM rows_state`).Scan(&st.Rows); err != nil {
		return st, err
	}
	if st.Entities, err = s.Checksum(); err != nil {
		return st, err
	}

	entries, err := os.ReadDir(p.PhotosDir(accountID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return st, err
	}
	for _, e := range entries {
		if e.IsDir() || e.Name()[0] == '.' {
			continue // .upload-* — недолитые
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		st.PhotoCount++
		st.PhotoBytes += info.Size()
	}
	return st, nil
}

// OpsByDevice — сколько правок прислало каждое устройство и когда последнюю.
type DeviceActivity struct {
	Ops      int64
	LastSeen time.Time
}

func (s *SyncDB) ActivityByDevice() (map[string]DeviceActivity, error) {
	rows, err := s.db.Query(
		`SELECT device_id, COUNT(*), MAX(received_at) FROM ops GROUP BY device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]DeviceActivity{}
	for rows.Next() {
		var dev string
		var act DeviceActivity
		var ms int64
		if err := rows.Scan(&dev, &act.Ops, &ms); err != nil {
			return nil, err
		}
		act.LastSeen = time.UnixMilli(ms).UTC()
		out[dev] = act
	}
	return out, rows.Err()
}

// Activity — то же, но без создания папки учётки.
func (p *Pool) Activity(accountID string) (map[string]DeviceActivity, error) {
	if _, err := os.Stat(filepath.Join(p.AccountDir(accountID), "sync.db")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]DeviceActivity{}, nil
		}
		return nil, err
	}
	s, err := p.Get(accountID)
	if err != nil {
		return nil, err
	}
	return s.ActivityByDevice()
}
