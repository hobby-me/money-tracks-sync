// Package store — данные сервиса: глобальная accounts.db и по-учёточные sync.db.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"
)

// AccountsDB — глобальная база: учётки, устройства, коды приглашений.
// Токены и коды хранятся только SHA-256-хэшами.
type AccountsDB struct {
	db *sql.DB
}

var ErrNotFound = errors.New("not found")
var ErrInviteInvalid = errors.New("invite invalid or expired")

const accountsSchema = `
CREATE TABLE IF NOT EXISTS accounts (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at INT NOT NULL,
  quota_bytes INT);
CREATE TABLE IF NOT EXISTS invites (
  code_hash TEXT PRIMARY KEY, account_id TEXT NOT NULL,
  grant_admin INT NOT NULL DEFAULT 0,
  expires_at INT NOT NULL, used_at INT);
CREATE TABLE IF NOT EXISTS devices (
  id TEXT PRIMARY KEY, account_id TEXT NOT NULL, name TEXT NOT NULL,
  token_hash TEXT UNIQUE NOT NULL, is_admin INT NOT NULL DEFAULT 0,
  created_at INT NOT NULL, revoked_at INT);
`

func OpenAccounts(dataDir string) (*AccountsDB, error) {
	db, err := openSQLite(filepath.Join(dataDir, "accounts.db"))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(accountsSchema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateAccounts(db); err != nil {
		db.Close()
		return nil, err
	}
	return &AccountsDB{db: db}, nil
}

// migrateAccounts догоняет схему баз, созданных до появления колонки.
// SQLite не умеет ADD COLUMN IF NOT EXISTS, поэтому смотрим pragma.
func migrateAccounts(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(accounts)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	hasQuota := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "quota_bytes" {
			hasQuota = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if hasQuota {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE accounts ADD COLUMN quota_bytes INT`)
	return err
}

func (a *AccountsDB) Close() error { return a.db.Close() }

func openSQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	// SQLite — один писатель; лишние соединения дают только SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	return db, nil
}

func hashHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func newULID() string { return ulid.Make().String() }

// newSecret — 256 бит случайности в hex (64 знака).
func newSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // без энтропии работать нельзя
	}
	return hex.EncodeToString(b[:])
}

// inviteAlphabet — base32 Крокфорда без паддинга: код читается и диктуется.
var inviteEnc = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

func newInviteCode() string {
	var b [10]byte // 80 бит — при TTL 10 минут перебор бессмыслен
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return inviteEnc.EncodeToString(b[:])
}

type Account struct {
	ID        string
	Name      string
	CreatedAt time.Time
	// QuotaBytes — индивидуальный потолок учётки: nil — действует
	// глобальный (SYNK_ACCOUNT_QUOTA_MB), 0 — без ограничения.
	QuotaBytes *int64
}

// EffectiveQuota — что реально ограничивает учётку при глобальном def.
func (a Account) EffectiveQuota(def int64) int64 {
	if a.QuotaBytes != nil {
		return *a.QuotaBytes
	}
	return def
}

func (a *AccountsDB) AddAccount(name string) (Account, error) {
	acc := Account{ID: newULID(), Name: name, CreatedAt: time.Now().UTC()}
	_, err := a.db.Exec(`INSERT INTO accounts(id, name, created_at) VALUES(?,?,?)`,
		acc.ID, acc.Name, acc.CreatedAt.UnixMilli())
	return acc, err
}

func (a *AccountsDB) ListAccounts() ([]Account, error) {
	rows, err := a.db.Query(`SELECT id, name, created_at, quota_bytes FROM accounts ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var acc Account
		var ms int64
		var quota sql.NullInt64
		if err := rows.Scan(&acc.ID, &acc.Name, &ms, &quota); err != nil {
			return nil, err
		}
		acc.CreatedAt = time.UnixMilli(ms).UTC()
		if quota.Valid {
			q := quota.Int64
			acc.QuotaBytes = &q
		}
		out = append(out, acc)
	}
	return out, rows.Err()
}

// SetAccountQuota задаёт индивидуальный потолок; nil возвращает учётку
// на глобальный.
func (a *AccountsDB) SetAccountQuota(accountID string, quota *int64) error {
	var v any
	if quota != nil {
		if *quota < 0 {
			return errors.New("quota must be >= 0")
		}
		v = *quota
	}
	res, err := a.db.Exec(`UPDATE accounts SET quota_bytes = ? WHERE id = ?`, v, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AccountQuota — индивидуальный потолок учётки или nil (глобальный).
// Один запрос по первичному ключу — дешевле обхода папки, который идёт
// рядом, поэтому вызывается на каждом push и фото без кэша.
func (a *AccountsDB) AccountQuota(accountID string) (*int64, error) {
	var quota sql.NullInt64
	err := a.db.QueryRow(`SELECT quota_bytes FROM accounts WHERE id = ?`, accountID).Scan(&quota)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !quota.Valid {
		return nil, nil
	}
	q := quota.Int64
	return &q, nil
}

func (a *AccountsDB) accountExists(id string) (bool, error) {
	var one int
	err := a.db.QueryRow(`SELECT 1 FROM accounts WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

type Invite struct {
	Code      string // отдаётся один раз, в базе только хэш
	AccountID string
	ExpiresAt time.Time
}

const inviteTTL = 10 * time.Minute

func (a *AccountsDB) CreateInvite(accountID string, grantAdmin bool) (Invite, error) {
	ok, err := a.accountExists(accountID)
	if err != nil {
		return Invite{}, err
	}
	if !ok {
		return Invite{}, ErrNotFound
	}
	inv := Invite{
		Code:      newInviteCode(),
		AccountID: accountID,
		ExpiresAt: time.Now().UTC().Add(inviteTTL),
	}
	admin := 0
	if grantAdmin {
		admin = 1
	}
	_, err = a.db.Exec(`INSERT INTO invites(code_hash, account_id, grant_admin, expires_at) VALUES(?,?,?,?)`,
		hashHex(inv.Code), inv.AccountID, admin, inv.ExpiresAt.UnixMilli())
	return inv, err
}

type Device struct {
	ID        string
	AccountID string
	Name      string
	IsAdmin   bool
	CreatedAt time.Time
	RevokedAt *time.Time
}

// Redeem гасит код первым использованием и заводит устройство.
// Токен возвращается один раз; в базе остаётся хэш.
func (a *AccountsDB) Redeem(code, deviceName string) (Device, string, error) {
	now := time.Now().UTC()
	tx, err := a.db.Begin()
	if err != nil {
		return Device{}, "", err
	}
	defer tx.Rollback()

	var accountID string
	var grantAdmin int
	err = tx.QueryRow(
		`SELECT account_id, grant_admin FROM invites
		 WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?`,
		hashHex(code), now.UnixMilli()).Scan(&accountID, &grantAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, "", ErrInviteInvalid
	}
	if err != nil {
		return Device{}, "", err
	}
	if _, err := tx.Exec(`UPDATE invites SET used_at = ? WHERE code_hash = ?`,
		now.UnixMilli(), hashHex(code)); err != nil {
		return Device{}, "", err
	}
	dev := Device{
		ID:        newULID(),
		AccountID: accountID,
		Name:      deviceName,
		IsAdmin:   grantAdmin == 1,
		CreatedAt: now,
	}
	token := newSecret()
	if _, err := tx.Exec(
		`INSERT INTO devices(id, account_id, name, token_hash, is_admin, created_at)
		 VALUES(?,?,?,?,?,?)`,
		dev.ID, dev.AccountID, dev.Name, hashHex(token), grantAdmin, now.UnixMilli()); err != nil {
		return Device{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return Device{}, "", err
	}
	return dev, token, nil
}

// Authenticate находит живое устройство по токену.
func (a *AccountsDB) Authenticate(token string) (Device, error) {
	var dev Device
	var admin int
	var ms int64
	err := a.db.QueryRow(
		`SELECT id, account_id, name, is_admin, created_at FROM devices
		 WHERE token_hash = ? AND revoked_at IS NULL`, hashHex(token)).
		Scan(&dev.ID, &dev.AccountID, &dev.Name, &admin, &ms)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, err
	}
	dev.IsAdmin = admin == 1
	dev.CreatedAt = time.UnixMilli(ms).UTC()
	return dev, nil
}

func (a *AccountsDB) ListDevices(accountID string) ([]Device, error) {
	q := `SELECT id, account_id, name, is_admin, created_at, revoked_at FROM devices`
	args := []any{}
	if accountID != "" {
		q += ` WHERE account_id = ?`
		args = append(args, accountID)
	}
	q += ` ORDER BY created_at`
	rows, err := a.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var admin int
		var ms int64
		var revoked sql.NullInt64
		if err := rows.Scan(&d.ID, &d.AccountID, &d.Name, &admin, &ms, &revoked); err != nil {
			return nil, err
		}
		d.IsAdmin = admin == 1
		d.CreatedAt = time.UnixMilli(ms).UTC()
		if revoked.Valid {
			t := time.UnixMilli(revoked.Int64).UTC()
			d.RevokedAt = &t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (a *AccountsDB) RevokeDevice(deviceID string) error {
	res, err := a.db.Exec(`UPDATE devices SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		time.Now().UTC().UnixMilli(), deviceID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DemoteDevice снимает админство (утеря админ-устройства: revoke + выдать другому).
func (a *AccountsDB) DemoteDevice(deviceID string) error {
	res, err := a.db.Exec(`UPDATE devices SET is_admin = 0 WHERE id = ?`, deviceID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
