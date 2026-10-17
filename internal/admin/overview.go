package admin

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/hobby-me/money-tracks-sync/internal/store"
)

// Пороги застоя учётки. Год — граница, после которой учётку можно удалить.
const (
	IdleMonth    = 30 * 24 * time.Hour
	IdleHalfYear = 182 * 24 * time.Hour
	IdleYear     = 365 * 24 * time.Hour
)

// IdleLevel — 0 живая, 1 месяц, 2 полгода, 3 год.
func IdleLevel(idle time.Duration) int {
	switch {
	case idle >= IdleYear:
		return 3
	case idle >= IdleHalfYear:
		return 2
	case idle >= IdleMonth:
		return 1
	default:
		return 0
	}
}

var idleLabels = [...]string{"", "1m+", "6m+", "1y+"}

func IdleLabel(idle time.Duration) string { return idleLabels[IdleLevel(idle)] }

// LastActivity — последний push учётки; если лога нет, дата создания.
func (a AccountInfo) LastActivity() time.Time {
	if a.Stats.LastReceive.IsZero() {
		return a.CreatedAt
	}
	return a.Stats.LastReceive
}

func (a AccountInfo) Idle(now time.Time) time.Duration { return now.Sub(a.LastActivity()) }

// Overview — сводка для дашборда и `synk status`: диск, объёмы, застой.
type Overview struct {
	Disk       store.DiskSpace
	DataBytes  int64 // вся папка данных, включая accounts.db и trash
	TrashBytes int64
	Accounts   []AccountInfo // по убыванию размера
	IdleCounts [4]int        // по IdleLevel
	At         time.Time
}

func (o Overview) AccountsBytes() int64 {
	var n int64
	for _, a := range o.Accounts {
		n += a.Stats.DBBytes + a.Stats.PhotoBytes
	}
	return n
}

// Overview собирает сводку одним проходом по данным.
func (a *Admin) Overview() (Overview, error) {
	o := Overview{At: time.Now()}
	accs, err := a.Accounts()
	if err != nil {
		return o, err
	}
	if o.Disk, err = store.Disk(a.DataDir); err != nil {
		return o, err
	}
	if o.DataBytes, err = store.DirSize(a.DataDir); err != nil {
		return o, err
	}
	if o.TrashBytes, err = store.DirSize(a.trashDir()); err != nil {
		return o, err
	}
	for _, acc := range accs {
		o.IdleCounts[IdleLevel(acc.Idle(o.At))]++
	}
	sort.SliceStable(accs, func(i, j int) bool {
		return accs[i].Stats.DBBytes+accs[i].Stats.PhotoBytes > accs[j].Stats.DBBytes+accs[j].Stats.PhotoBytes
	})
	o.Accounts = accs
	return o, nil
}

// ErrStillActive — учётка неактивна меньше года; удалять только с force.
var ErrStillActive = errors.New("account was active less than a year ago (use force)")

func (a *Admin) trashDir() string { return filepath.Join(a.DataDir, "trash") }

// DeleteAccount убирает учётку: строки в accounts.db (устройства, коды)
// удаляются, папка acc/<id> переезжает в trash/<id>-<дата> — мгновенно и
// обратимо руками. Без force удаляются только учётки, молчавшие год.
// Возвращает путь в trash (пустой, если папки не было).
func (a *Admin) DeleteAccount(ref string, force bool) (store.Account, string, error) {
	acc, err := a.ResolveAccount(ref)
	if err != nil {
		return store.Account{}, "", err
	}
	if !force {
		infos, err := a.Accounts()
		if err != nil {
			return acc, "", err
		}
		now := time.Now()
		for _, info := range infos {
			if info.ID == acc.ID && info.Idle(now) < IdleYear {
				return acc, "", ErrStillActive
			}
		}
	}
	a.pool.Evict(acc.ID)
	src := a.pool.AccountDir(acc.ID)
	dst := ""
	if _, err := os.Stat(src); err == nil {
		if err := os.MkdirAll(a.trashDir(), 0o700); err != nil {
			return acc, "", err
		}
		dst = filepath.Join(a.trashDir(), acc.ID+"-"+time.Now().UTC().Format("20060102T150405Z"))
		if err := os.Rename(src, dst); err != nil {
			return acc, "", err
		}
	}
	if err := a.accounts.DeleteAccount(acc.ID); err != nil {
		return acc, dst, err
	}
	return acc, dst, nil
}

// EmptyTrash удаляет содержимое trash/ окончательно.
func (a *Admin) EmptyTrash() (int64, error) {
	n, err := store.DirSize(a.trashDir())
	if err != nil {
		return 0, err
	}
	if err := os.RemoveAll(a.trashDir()); err != nil {
		return 0, err
	}
	return n, nil
}
