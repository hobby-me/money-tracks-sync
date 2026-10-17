// Package admin — операции оператора над данными synk: учётки, устройства,
// коды приглашений, статистика. Единственный слой, который знают и CLI
// (cmd/synk), и TUI (internal/tui): новая возможность добавляется здесь один
// раз и появляется в обоих интерфейсах.
package admin

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hobby-me/money-tracks-sync/internal/store"
)

type Admin struct {
	DataDir string
	// DefaultQuota — глобальный потолок сервера (SYNK_ACCOUNT_QUOTA_MB), чтобы
	// CLI и TUI показывали действующую квоту учёток без своей.
	DefaultQuota int64
	accounts     *store.AccountsDB
	pool         *store.Pool
}

// Open создаёт папку данных при необходимости и открывает accounts.db.
// Базы учёток открываются лениво, при первом запросе статистики.
func Open(dataDir string) (*Admin, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	accounts, err := store.OpenAccounts(dataDir)
	if err != nil {
		return nil, err
	}
	return &Admin{DataDir: dataDir, DefaultQuota: store.DefaultQuotaBytes, accounts: accounts, pool: store.NewPool(dataDir)}, nil
}

// SetQuota задаёт индивидуальный потолок учётки (байт; 0 — без лимита),
// nil возвращает её на глобальный.
func (a *Admin) SetQuota(accountRef string, quota *int64) (store.Account, error) {
	acc, err := a.ResolveAccount(accountRef)
	if err != nil {
		return store.Account{}, err
	}
	if err := a.accounts.SetAccountQuota(acc.ID, quota); err != nil {
		return store.Account{}, err
	}
	acc.QuotaBytes = quota
	return acc, nil
}

// QuotaLabel — квота для таблиц: «1.0G», «∞» без лимита, «*» на конце
// у индивидуальной.
func (a *Admin) QuotaLabel(acc store.Account) string {
	q := acc.EffectiveQuota(a.DefaultQuota)
	label := "∞"
	if q > 0 {
		label = HumanBytes(q)
	}
	if acc.QuotaBytes != nil {
		label += "*"
	}
	return label
}

// HumanBytes — K/M/G с одним знаком; общая для CLI и TUI, без fmt.
func HumanBytes(n int64) string {
	if n <= 0 {
		return "-"
	}
	units := []string{"B", "K", "M", "G", "T"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return strconv.FormatInt(n, 10) + units[0]
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + units[i]
}

func (a *Admin) Close() {
	a.pool.Close()
	a.accounts.Close()
}

// ErrAmbiguous — ссылка на учётку по имени совпала с несколькими.
var ErrAmbiguous = errors.New("ambiguous account reference")

// AccountInfo — учётка с тем, что о ней можно сказать по данным на диске.
type AccountInfo struct {
	store.Account
	Stats          store.AccountStats
	DevicesActive  int
	DevicesRevoked int
	PendingInvites int
}

// Accounts — все учётки со статистикой, в порядке создания.
func (a *Admin) Accounts() ([]AccountInfo, error) {
	accs, err := a.accounts.ListAccounts()
	if err != nil {
		return nil, err
	}
	devs, err := a.accounts.ListDevices("")
	if err != nil {
		return nil, err
	}
	invs, err := a.accounts.ListInvites("")
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]AccountInfo, 0, len(accs))
	for _, acc := range accs {
		info := AccountInfo{Account: acc}
		if info.Stats, err = a.pool.Stats(acc.ID); err != nil {
			return nil, err
		}
		for _, d := range devs {
			if d.AccountID != acc.ID {
				continue
			}
			if d.RevokedAt != nil {
				info.DevicesRevoked++
			} else {
				info.DevicesActive++
			}
		}
		for _, inv := range invs {
			if inv.AccountID == acc.ID && inv.Pending(now) {
				info.PendingInvites++
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// ResolveAccount принимает id или имя (без учёта регистра). Имя должно быть
// уникальным, иначе ErrAmbiguous — чтобы код не ушёл не в ту учётку.
func (a *Admin) ResolveAccount(ref string) (store.Account, error) {
	accs, err := a.accounts.ListAccounts()
	if err != nil {
		return store.Account{}, err
	}
	for _, acc := range accs {
		if acc.ID == ref {
			return acc, nil
		}
	}
	var found []store.Account
	for _, acc := range accs {
		if strings.EqualFold(acc.Name, ref) {
			found = append(found, acc)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return store.Account{}, store.ErrNotFound
	default:
		return store.Account{}, ErrAmbiguous
	}
}

// AddAccount заводит учётку; с bootstrap сразу выпускает админ-код для
// первого устройства.
func (a *Admin) AddAccount(name string, bootstrap bool) (store.Account, *store.Invite, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return store.Account{}, nil, errors.New("account name is empty")
	}
	acc, err := a.accounts.AddAccount(name)
	if err != nil {
		return store.Account{}, nil, err
	}
	if !bootstrap {
		return acc, nil, nil
	}
	inv, err := a.accounts.CreateInvite(acc.ID, true)
	if err != nil {
		return acc, nil, err
	}
	return acc, &inv, nil
}

// Invite выпускает одноразовый код подключения устройства к учётке.
func (a *Admin) Invite(accountRef string, admin bool) (store.Invite, error) {
	acc, err := a.ResolveAccount(accountRef)
	if err != nil {
		return store.Invite{}, err
	}
	return a.accounts.CreateInvite(acc.ID, admin)
}

// DeviceInfo — устройство с именем учётки и активностью из её лога.
type DeviceInfo struct {
	store.Device
	AccountName string
	Ops         int64
	LastSeen    time.Time
}

func (d DeviceInfo) Active() bool { return d.RevokedAt == nil }

// Devices — устройства (всех учёток или одной), в порядке создания.
func (a *Admin) Devices(accountRef string) ([]DeviceInfo, error) {
	accountID := ""
	if accountRef != "" {
		acc, err := a.ResolveAccount(accountRef)
		if err != nil {
			return nil, err
		}
		accountID = acc.ID
	}
	devs, err := a.accounts.ListDevices(accountID)
	if err != nil {
		return nil, err
	}
	names, err := a.accountNames()
	if err != nil {
		return nil, err
	}
	activity := map[string]map[string]store.DeviceActivity{}
	out := make([]DeviceInfo, 0, len(devs))
	for _, d := range devs {
		info := DeviceInfo{Device: d, AccountName: names[d.AccountID]}
		act, ok := activity[d.AccountID]
		if !ok {
			if act, err = a.pool.Activity(d.AccountID); err != nil {
				return nil, err
			}
			activity[d.AccountID] = act
		}
		if da, ok := act[d.ID]; ok {
			info.Ops, info.LastSeen = da.Ops, da.LastSeen
		}
		out = append(out, info)
	}
	return out, nil
}

func (a *Admin) RevokeDevice(deviceID string) error { return a.accounts.RevokeDevice(deviceID) }
func (a *Admin) DemoteDevice(deviceID string) error { return a.accounts.DemoteDevice(deviceID) }

// InviteInfo — код приглашения с именем учётки.
type InviteInfo struct {
	store.InviteInfo
	AccountName string
}

// State — pending / used / expired, на момент now.
func (i InviteInfo) State(now time.Time) string {
	switch {
	case i.UsedAt != nil:
		return "used"
	case i.ExpiresAt.After(now):
		return "pending"
	default:
		return "expired"
	}
}

// Invites — коды (всех учёток или одной), свежие сверху.
func (a *Admin) Invites(accountRef string) ([]InviteInfo, error) {
	accountID := ""
	if accountRef != "" {
		acc, err := a.ResolveAccount(accountRef)
		if err != nil {
			return nil, err
		}
		accountID = acc.ID
	}
	invs, err := a.accounts.ListInvites(accountID)
	if err != nil {
		return nil, err
	}
	names, err := a.accountNames()
	if err != nil {
		return nil, err
	}
	out := make([]InviteInfo, 0, len(invs))
	for _, inv := range invs {
		out = append(out, InviteInfo{InviteInfo: inv, AccountName: names[inv.AccountID]})
	}
	return out, nil
}

// CancelInvite отзывает непогашенный код по его хэшу.
func (a *Admin) CancelInvite(codeHash string) error { return a.accounts.DeleteInvite(codeHash) }

// PurgeInvites чистит протухшие/погашенные коды старше keep.
func (a *Admin) PurgeInvites(keep time.Duration) (int64, error) {
	return a.accounts.PurgeInvites(keep)
}

func (a *Admin) accountNames() (map[string]string, error) {
	accs, err := a.accounts.ListAccounts()
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(accs))
	for _, acc := range accs {
		names[acc.ID] = acc.Name
	}
	return names, nil
}
