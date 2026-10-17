package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// DirSize — суммарный размер файлов под path. Отсутствующая папка — 0.
func DirSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return total, err
}

// DefaultQuotaBytes — глобальный потолок на учётку (sync.db + фото),
// если не задан флагом/окружением и у учётки нет своего: чеки после
// пережима — сотни КБ, гигабайта хватает на годы.
const DefaultQuotaBytes = 1 << 30

// UsedBytes — сколько учётка занимает на диске: sync.db с WAL и фото.
// Основа для квоты: аутентифицированное устройство не должно уметь
// заполнить диск всему серверу.
func (p *Pool) UsedBytes(accountID string) (int64, error) {
	return DirSize(p.AccountDir(accountID))
}

// DiskSpace — ёмкость и свободное место файловой системы, где лежит path.
type DiskSpace struct {
	TotalBytes int64
	FreeBytes  int64 // доступно непривилегированному процессу (f_bavail)
}

func (d DiskSpace) UsedBytes() int64 { return d.TotalBytes - d.FreeBytes }

// Disk — statfs по папке данных. Поля Statfs_t различаются типами между
// darwin и linux, поэтому всё приводится к uint64.
func Disk(path string) (DiskSpace, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return DiskSpace{}, err
	}
	bsize := uint64(st.Bsize)
	return DiskSpace{
		TotalBytes: int64(uint64(st.Blocks) * bsize),
		FreeBytes:  int64(uint64(st.Bavail) * bsize),
	}, nil
}

// DeleteAccount удаляет учётку из accounts.db вместе с устройствами и
// кодами одной транзакцией. Папку acc/<id> вызывающий убирает сам.
func (a *AccountsDB) DeleteAccount(accountID string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM accounts WHERE id = ?`, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM devices WHERE account_id = ?`, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM invites WHERE account_id = ?`, accountID); err != nil {
		return err
	}
	return tx.Commit()
}

// Evict закрывает sync.db учётки, если открыта — перед переносом папки.
func (p *Pool) Evict(accountID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.open[accountID]; ok {
		s.db.Close()
		delete(p.open, accountID)
	}
}
