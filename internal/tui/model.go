package tui

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/hobby-me/money-tracks-sync/internal/admin"
	"github.com/hobby-me/money-tracks-sync/internal/store"
)

type view int

const (
	viewAccounts view = iota
	viewDevices
	viewInvites
	viewDashboard
	viewCount
)

func (v view) String() string {
	switch v {
	case viewAccounts:
		return "accounts"
	case viewDevices:
		return "devices"
	case viewInvites:
		return "invites"
	default:
		return "dashboard"
	}
}

// snapshot — всё, что показывает интерфейс, одним чтением. Перечитывается по
// таймеру и после каждого действия; строки таблиц пересобираются из него.
type snapshot struct {
	accounts []admin.AccountInfo
	devices  []admin.DeviceInfo
	invites  []admin.InviteInfo
	overview admin.Overview
	at       time.Time
}

type (
	snapshotMsg struct {
		snap snapshot
		err  error
	}
	healthMsg admin.Health
	tickMsg   time.Time
	// actionMsg — результат операции: строка статуса, ошибка и модалка,
	// которую нужно открыть после (например, код приглашения).
	actionMsg struct {
		status string
		err    error
		open   overlay
	}
)

const (
	tickEvery   = time.Second
	reloadEvery = 5 // тиков
	statusTTL   = 8 * time.Second
	purgeKeep   = 24 * time.Hour
)

type Model struct {
	adm     *admin.Admin
	listen  string
	metrics string
	url     string
	version string

	width, height int
	now           time.Time
	ticks         int

	view               view
	scopeID, scopeName string // фильтр по учётке для devices/invites
	tables             [viewCount]*table

	filter    string
	filtering bool
	filterIn  textinput.Model
	cmdMode   bool
	cmdIn     textinput.Model

	snap       snapshot
	health     admin.Health
	prevHealth admin.Health // для скоростей (RPS) между опросами
	loading    bool
	loadErr    error

	overlay   overlay
	status    string
	statusErr bool
	statusAt  time.Time
}

func newModel(adm *admin.Admin, opts Options) *Model {
	m := &Model{adm: adm, listen: opts.Listen, metrics: opts.Metrics, url: opts.URL, version: opts.Version, now: time.Now()}
	m.tables[viewAccounts] = &table{
		cols: []column{
			{title: "id", flex: true}, {title: "name", flex: true}, {title: "created"},
			{title: "devices"}, {title: "invites", right: true}, {title: "ops", right: true},
			{title: "rows", right: true}, {title: "photos", right: true},
			{title: "size", right: true}, {title: "quota", right: true}, {title: "last sync", right: true},
		},
		empty: "учёток нет — <a> заведёт первую",
	}
	m.tables[viewDevices] = &table{
		cols: []column{
			{title: "id", flex: true}, {title: "account", flex: true}, {title: "name", flex: true},
			{title: "state"}, {title: "role"}, {title: "ops", right: true},
			{title: "last seen", right: true}, {title: "created"},
		},
		empty: "устройств нет — <i> выпустит код подключения",
	}
	m.tables[viewInvites] = &table{
		cols: []column{
			{title: "hash", flex: true}, {title: "account", flex: true}, {title: "role"},
			{title: "state"}, {title: "expires"}, {title: "used"},
		},
		empty: "кодов нет — <i> выпустит новый",
	}
	m.tables[viewDashboard] = &table{
		cols: []column{
			{title: "name", flex: true}, {title: "idle"}, {title: "last activity"},
			{title: "size", right: true}, {title: "devices", right: true}, {title: "id", flex: true},
		},
		empty: "учёток нет",
	}
	m.filterIn = textinput.New()
	m.filterIn.Prompt = "/"
	m.filterIn.Placeholder = "подстрока"
	m.filterIn.CharLimit = 40
	m.cmdIn = textinput.New()
	m.cmdIn.Prompt = ":"
	m.cmdIn.Placeholder = "devices alex"
	m.cmdIn.CharLimit = 80
	return m
}

func (m *Model) Init() tea.Cmd {
	m.loading = true
	return tea.Batch(m.loadCmd(), m.probeCmd(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(tickEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) loadCmd() tea.Cmd {
	adm := m.adm
	return func() tea.Msg {
		var s snapshot
		var err error
		if s.accounts, err = adm.Accounts(); err != nil {
			return snapshotMsg{err: err}
		}
		if s.devices, err = adm.Devices(""); err != nil {
			return snapshotMsg{err: err}
		}
		if s.invites, err = adm.Invites(""); err != nil {
			return snapshotMsg{err: err}
		}
		if s.overview, err = adm.Overview(); err != nil {
			return snapshotMsg{err: err}
		}
		s.at = time.Now()
		return snapshotMsg{snap: s}
	}
}

func (m *Model) probeCmd() tea.Cmd {
	listen, metrics := m.listen, m.metrics
	return func() tea.Msg { return healthMsg(admin.Probe(context.Background(), listen, metrics)) }
}

// act оборачивает операцию admin в команду Bubble Tea.
func act(f func() (string, overlay, error)) tea.Cmd {
	return func() tea.Msg {
		s, o, err := f()
		return actionMsg{status: s, err: err, open: o}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.filterIn.Width = msg.Width - 4
		m.cmdIn.Width = msg.Width - 4
		return m, nil

	case tickMsg:
		m.now = time.Time(msg)
		m.ticks++
		if m.status != "" && m.now.Sub(m.statusAt) > statusTTL {
			m.status = ""
		}
		m.rebuild()
		if m.ticks%reloadEvery == 0 && !m.loading {
			m.loading = true
			return m, tea.Batch(tick(), m.loadCmd(), m.probeCmd())
		}
		return m, tick()

	case snapshotMsg:
		m.loading = false
		m.loadErr = msg.err
		if msg.err == nil {
			m.snap = msg.snap
			m.rebuild()
		}
		return m, nil

	case healthMsg:
		if m.health.Up {
			m.prevHealth = m.health
		}
		m.health = admin.Health(msg)
		return m, nil

	case actionMsg:
		if msg.err != nil {
			m.setStatus(msg.err.Error(), true)
			return m, nil
		}
		if msg.status != "" {
			m.setStatus(msg.status, false)
		}
		if msg.open != nil {
			m.overlay = msg.open
		}
		m.loading = true
		return m, m.loadCmd()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) setStatus(s string, isErr bool) {
	m.status, m.statusErr, m.statusAt = s, isErr, m.now
}

func (m *Model) table() *table { return m.tables[m.view] }

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.overlay != nil {
		cmd, closeIt := m.overlay.update(msg, m)
		if closeIt {
			m.overlay = nil
		}
		return m, cmd
	}
	if m.cmdMode {
		switch msg.String() {
		case "esc", "ctrl+c":
			m.cmdMode = false
			m.cmdIn.Reset()
			return m, nil
		case "enter":
			line := strings.TrimSpace(m.cmdIn.Value())
			m.cmdMode = false
			m.cmdIn.Reset()
			return m, m.runCommand(line)
		}
		var cmd tea.Cmd
		m.cmdIn, cmd = m.cmdIn.Update(msg)
		return m, cmd
	}
	if m.filtering {
		switch msg.String() {
		case "esc", "ctrl+c":
			m.filtering = false
			m.filter = ""
			m.filterIn.Reset()
			m.rebuild()
			return m, nil
		case "enter":
			m.filtering = false
			return m, nil
		}
		var cmd tea.Cmd
		m.filterIn, cmd = m.filterIn.Update(msg)
		m.filter = strings.ToLower(strings.TrimSpace(m.filterIn.Value()))
		m.rebuild()
		return m, cmd
	}

	t := m.table()
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc":
		switch {
		case m.filter != "":
			m.filter = ""
			m.filterIn.Reset()
			m.rebuild()
		case m.scopeID != "":
			m.setScope("", "")
			m.switchView(viewAccounts)
		}
		return m, nil
	case ":":
		m.cmdMode = true
		m.cmdIn.Focus()
		return m, textinput.Blink
	case "/":
		m.filtering = true
		m.filterIn.Focus()
		return m, textinput.Blink
	case "?":
		m.overlay = helpOverlay()
		return m, nil
	case "1":
		m.switchView(viewAccounts)
		return m, nil
	case "2":
		m.switchView(viewDevices)
		return m, nil
	case "3":
		m.switchView(viewInvites)
		return m, nil
	case "4":
		m.switchView(viewDashboard)
		return m, nil
	case "r":
		m.loading = true
		return m, tea.Batch(m.loadCmd(), m.probeCmd())
	case "up", "k":
		t.move(-1)
		return m, nil
	case "down", "j":
		t.move(1)
		return m, nil
	case "pgup", "ctrl+u":
		t.move(-m.pageSize())
		return m, nil
	case "pgdown", "ctrl+d":
		t.move(m.pageSize())
		return m, nil
	case "g", "home":
		t.first()
		return m, nil
	case "G", "end":
		t.last()
		return m, nil
	}
	switch m.view {
	case viewAccounts:
		return m, m.accountsKey(msg.String())
	case viewDevices:
		return m, m.devicesKey(msg.String())
	case viewInvites:
		return m, m.invitesKey(msg.String())
	default:
		return m, m.dashboardKey(msg.String())
	}
}

func (m *Model) pageSize() int {
	n := m.height - headerLines - 4
	if n < 1 {
		return 1
	}
	return n
}

func (m *Model) switchView(v view) {
	m.view = v
	m.rebuild()
}

func (m *Model) setScope(id, name string) {
	m.scopeID, m.scopeName = id, name
	m.rebuild()
}

// ---- клавиши по вкладкам ----

func (m *Model) accountsKey(key string) tea.Cmd {
	acc := m.selectedAccount()
	switch key {
	case "a":
		m.overlay = newInput("Новая учётка", "имя, например Alex",
			"Учётка заводится только здесь, по SSH — снаружи такой ручки нет.",
			func(m *Model, name string) tea.Cmd { return m.addAccount(name) })
	case "enter":
		if acc != nil {
			m.setScope(acc.ID, acc.Name)
			m.switchView(viewDevices)
		}
	case "i":
		if acc != nil {
			m.overlay = m.invitePicker(acc.Account)
		}
	case "d":
		if acc != nil {
			m.overlay = accountDetails(*acc, m)
		}
	case "D":
		if acc != nil {
			m.overlay = m.deleteConfirm(*acc)
		}
	case "Q":
		if acc != nil {
			m.overlay = m.quotaInput(acc.Account)
		}
	}
	return nil
}

// dashboardKey — таблица дашборда тоже про учётки: те же действия.
func (m *Model) dashboardKey(key string) tea.Cmd {
	acc := m.selectedIn(viewDashboard)
	switch key {
	case "D":
		if acc != nil {
			m.overlay = m.deleteConfirm(*acc)
		}
	case "d", "enter":
		if acc != nil {
			m.overlay = accountDetails(*acc, m)
		}
	case "i":
		if acc != nil {
			m.overlay = m.invitePicker(acc.Account)
		}
	case "t":
		if m.snap.overview.TrashBytes == 0 {
			m.setStatus("trash пуст", false)
			return nil
		}
		size := humanBytes(m.snap.overview.TrashBytes)
		m.overlay = &confirmOverlay{
			title: "Очистить trash", danger: true, yes: "Удалить навсегда",
			body: []string{"В data/trash/ лежат папки удалённых учёток: " + size + ".",
				"После очистки восстановить их будет нельзя."},
			onYes: func(m *Model) tea.Cmd {
				return act(func() (string, overlay, error) {
					n, err := m.adm.EmptyTrash()
					return "trash очищен, освобождено " + humanBytes(n), nil, err
				})
			},
		}
	}
	return nil
}

// deleteConfirm — удаление учётки: только молчавшие год, подтверждение
// вводом имени. Папка уезжает в trash, так что ошибка обратима руками.
func (m *Model) deleteConfirm(acc admin.AccountInfo) overlay {
	idle := acc.Idle(m.now)
	if idle < admin.IdleYear {
		m.setStatus("учётка "+acc.Name+" была активна "+ago(acc.LastActivity(), m.now)+
			" назад — удалять можно только молчавшие год (CLI: account delete --force)", true)
		return nil
	}
	id, name := acc.ID, acc.Name
	return newInput("Удалить учётку "+name+" ("+admin.IdleLabel(idle)+", последняя активность "+
		localTime(acc.LastActivity())+")", "введите имя учётки",
		"Устройства и коды будут удалены, папка acc/"+shortID(id)+" переедет в data/trash/. Для подтверждения наберите имя: "+name,
		func(m *Model, typed string) tea.Cmd {
			if typed != name {
				m.setStatus("имя не совпало — удаление отменено", true)
				return nil
			}
			return act(func() (string, overlay, error) {
				_, trash, err := m.adm.DeleteAccount(id, false)
				if err != nil {
					return "", nil, err
				}
				st := "учётка " + name + " удалена"
				if trash != "" {
					st += ", папка в " + trash
				}
				return st, nil, nil
			})
		})
}

func (m *Model) devicesKey(key string) tea.Cmd {
	dev := m.selectedDevice()
	switch key {
	case "i":
		if dev != nil {
			m.overlay = m.invitePicker(store.Account{ID: dev.AccountID, Name: dev.AccountName})
		} else if m.scopeID != "" {
			m.overlay = m.invitePicker(store.Account{ID: m.scopeID, Name: m.scopeName})
		}
	case "x":
		if dev == nil {
			return nil
		}
		if !dev.Active() {
			m.setStatus("устройство уже отозвано", true)
			return nil
		}
		id, name := dev.ID, dev.Name
		m.overlay = &confirmOverlay{
			title: "Отозвать устройство", danger: true, yes: "Отозвать",
			body: []string{
				name + "  (" + dev.AccountName + ")",
				shortID(id) + " · " + roleOf(dev.IsAdmin),
				"",
				"Данные учётки целы. Устройство перестанет синхронизироваться,",
				"пока не подключится по новому коду приглашения.",
			},
			onYes: func(m *Model) tea.Cmd {
				return act(func() (string, overlay, error) {
					return "устройство " + name + " отозвано", nil, m.adm.RevokeDevice(id)
				})
			},
		}
	case "m":
		if dev == nil {
			return nil
		}
		if !dev.IsAdmin {
			m.setStatus("устройство и так без админства", true)
			return nil
		}
		id, name := dev.ID, dev.Name
		m.overlay = &confirmOverlay{
			title: "Снять админство", yes: "Снять",
			body: []string{
				name + "  (" + dev.AccountName + ")",
				"",
				"Устройство больше не сможет заводить учётки и приглашать в чужие.",
				"Синхронизация своей учётки не пострадает.",
			},
			onYes: func(m *Model) tea.Cmd {
				return act(func() (string, overlay, error) {
					return "админство снято с " + name, nil, m.adm.DemoteDevice(id)
				})
			},
		}
	case "d", "enter":
		if dev != nil {
			m.overlay = deviceDetails(*dev)
		}
	}
	return nil
}

func (m *Model) invitesKey(key string) tea.Cmd {
	inv := m.selectedInvite()
	switch key {
	case "i":
		if m.scopeID != "" {
			m.overlay = m.invitePicker(store.Account{ID: m.scopeID, Name: m.scopeName})
		} else if inv != nil {
			m.overlay = m.invitePicker(store.Account{ID: inv.AccountID, Name: inv.AccountName})
		} else {
			m.overlay = m.accountPicker("Код приглашения — для какой учётки?",
				func(m *Model, acc store.Account) tea.Cmd {
					m.overlay = m.invitePicker(acc)
					return nil
				})
		}
	case "x":
		if inv == nil {
			return nil
		}
		if inv.State(m.now) != "pending" {
			m.setStatus("отменить можно только действующий код", true)
			return nil
		}
		hash := inv.CodeHash
		m.overlay = &confirmOverlay{
			title: "Отменить код приглашения", danger: true, yes: "Отменить",
			body: []string{
				shortID(hash) + " · " + inv.AccountName + " · " + roleOf(inv.GrantAdmin),
				"истекает через " + countdown(inv.ExpiresAt, m.now),
				"",
				"Код перестанет действовать немедленно.",
			},
			onYes: func(m *Model) tea.Cmd {
				return act(func() (string, overlay, error) {
					return "код отменён", nil, m.adm.CancelInvite(hash)
				})
			},
		}
	case "p":
		m.overlay = &confirmOverlay{
			title: "Чистка кодов", yes: "Удалить",
			body: []string{"Удалить погашенные и протухшие коды старше суток?",
				"Действующие коды не трогаются."},
			onYes: func(m *Model) tea.Cmd {
				return act(func() (string, overlay, error) {
					n, err := m.adm.PurgeInvites(purgeKeep)
					return "удалено кодов: " + groupIntZero(n), nil, err
				})
			},
		}
	}
	return nil
}

// ---- сценарии ----

// quotaInput — индивидуальная квота учётки: число в МБ, 0 — без лимита,
// пустая строка — вернуть на глобальную.
func (m *Model) quotaInput(acc store.Account) overlay {
	o := newInput("Квота учётки · "+acc.Name+" · сейчас "+m.adm.QuotaLabel(acc),
		"МБ; 0 — без лимита; пусто — глобальная ("+humanBytes(m.adm.DefaultQuota)+")",
		"Потолок на sync.db + фото. Сверх него сервер отвечает 507, приложение видит его в GET /v1/quota.",
		func(m *Model, text string) tea.Cmd { return m.setQuota(acc, text) })
	o.allowEmpty = true
	return o
}

func (m *Model) setQuota(acc store.Account, text string) tea.Cmd {
	text = strings.TrimSpace(text)
	var q *int64
	if text != "" && !strings.EqualFold(text, "default") {
		mb, err := strconv.ParseInt(text, 10, 64)
		if err != nil || mb < 0 {
			m.setStatus("квота: нужно целое число МБ, 0 — без лимита, пусто — глобальная", true)
			return nil
		}
		v := mb << 20
		q = &v
	}
	return act(func() (string, overlay, error) {
		updated, err := m.adm.SetQuota(acc.ID, q)
		if err != nil {
			return "", nil, err
		}
		return "квота " + updated.Name + ": " + m.adm.QuotaLabel(updated), nil, nil
	})
}

func (m *Model) addAccount(name string) tea.Cmd {
	return act(func() (string, overlay, error) {
		acc, _, err := m.adm.AddAccount(name, false)
		if err != nil {
			return "", nil, err
		}
		return "учётка " + acc.Name + " создана", &confirmOverlay{
			title: "Учётка " + acc.Name + " создана",
			yes:   "Выпустить",
			body: []string{
				"Выпустить админ-код для первого устройства?",
				"Оно сможет приглашать другие устройства в эту учётку из приложения.",
				"Квота — глобальная (" + humanBytes(m.adm.DefaultQuota) + "), <Q> задаст свою.",
			},
			onYes: func(m *Model) tea.Cmd { return m.invite(acc, true) },
		}, nil
	})
}

func (m *Model) invitePicker(acc store.Account) overlay {
	return &pickOverlay{
		title: "Код приглашения · " + acc.Name,
		items: []string{"Обычное устройство", "Администратор"},
		notes: []string{
			"синхронизирует эту учётку, приглашать не может",
			"плюс выпускает коды подключения в эту учётку из приложения",
		},
		onPick: func(m *Model, i int) tea.Cmd { return m.invite(acc, i == 1) },
	}
}

func (m *Model) invite(acc store.Account, admin bool) tea.Cmd {
	return act(func() (string, overlay, error) {
		inv, err := m.adm.Invite(acc.ID, admin)
		if err != nil {
			return "", nil, err
		}
		return "код для " + acc.Name + " выпущен", &noticeOverlay{
			title:   "Код приглашения · " + acc.Name + " · " + roleOf(admin),
			code:    inv.Code,
			expires: inv.ExpiresAt,
			paste:   inviteText(acc, inv, admin, m.url),
		}, nil
	})
}

// inviteText — тот же блок, что печатает CLI `synk invite`.
var inviteText = admin.InviteText

func (m *Model) accountPicker(title string, then func(*Model, store.Account) tea.Cmd) overlay {
	items := make([]string, 0, len(m.snap.accounts))
	notes := make([]string, 0, len(m.snap.accounts))
	for _, a := range m.snap.accounts {
		items = append(items, a.Name)
		notes = append(notes, shortID(a.ID)+" · устройств: "+groupIntZero(int64(a.DevicesActive)))
	}
	if len(items) == 0 {
		return &noticeOverlay{title: "Учёток нет", lines: []string{"Сначала заведите учётку: <1> → <a>."}}
	}
	return &pickOverlay{title: title, items: items, notes: notes,
		onPick: func(m *Model, i int) tea.Cmd {
			if i < len(m.snap.accounts) {
				return then(m, m.snap.accounts[i].Account)
			}
			return nil
		}}
}

// runCommand — :-команды в духе k9s. Ссылка на учётку — id или имя.
func (m *Model) runCommand(line string) tea.Cmd {
	if line == "" {
		return nil
	}
	fields := strings.Fields(line)
	cmd, args := fields[0], fields[1:]
	scopeTo := func(ref string) bool {
		if ref == "" {
			m.setScope("", "")
			return true
		}
		acc, err := m.adm.ResolveAccount(ref)
		if err != nil {
			m.setStatus("учётка «"+ref+"»: "+describeErr(err), true)
			return false
		}
		m.setScope(acc.ID, acc.Name)
		return true
	}
	switch cmd {
	case "q", "quit", "exit":
		return tea.Quit
	case "a", "acc", "accounts":
		m.setScope("", "")
		m.switchView(viewAccounts)
	case "d", "dev", "devices":
		if scopeTo(strings.Join(args, " ")) {
			m.switchView(viewDevices)
		}
	case "i", "inv", "invites":
		if scopeTo(strings.Join(args, " ")) {
			m.switchView(viewInvites)
		}
	case "invite":
		if len(args) == 0 {
			m.setStatus("usage: :invite <учётка> [admin]", true)
			return nil
		}
		admin := len(args) > 1 && args[len(args)-1] == "admin"
		ref := strings.Join(args, " ")
		if admin {
			ref = strings.Join(args[:len(args)-1], " ")
		}
		acc, err := m.adm.ResolveAccount(ref)
		if err != nil {
			m.setStatus("учётка «"+ref+"»: "+describeErr(err), true)
			return nil
		}
		return m.invite(acc, admin)
	case "add":
		if len(args) == 0 {
			m.setStatus("usage: :add <имя учётки>", true)
			return nil
		}
		return m.addAccount(strings.Join(args, " "))
	case "quota":
		if len(args) < 2 {
			m.setStatus("usage: :quota <учётка> <МБ|0|default>", true)
			return nil
		}
		ref := strings.Join(args[:len(args)-1], " ")
		acc, err := m.adm.ResolveAccount(ref)
		if err != nil {
			m.setStatus("учётка «"+ref+"»: "+describeErr(err), true)
			return nil
		}
		return m.setQuota(acc, args[len(args)-1])
	case "purge":
		return m.invitesKey("p")
	case "dash", "dashboard", "overview", "o":
		m.switchView(viewDashboard)
	case "delete", "del":
		if len(args) == 0 {
			m.setStatus("usage: :delete <учётка>", true)
			return nil
		}
		ref := strings.Join(args, " ")
		for i := range m.snap.accounts {
			a := &m.snap.accounts[i]
			if a.ID == ref || strings.EqualFold(a.Name, ref) {
				m.overlay = m.deleteConfirm(*a)
				return nil
			}
		}
		m.setStatus("учётка «"+ref+"»: не найдена", true)
	case "help", "?":
		m.overlay = helpOverlay()
	default:
		m.setStatus("неизвестная команда: "+cmd+" — <?> покажет список", true)
	}
	return nil
}

func describeErr(err error) string {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "не найдена"
	case errors.Is(err, admin.ErrAmbiguous):
		return "имя неоднозначно, укажите id"
	default:
		return err.Error()
	}
}

// ---- выбранные записи ----

func (m *Model) selectedAccount() *admin.AccountInfo { return m.selectedIn(viewAccounts) }

func (m *Model) selectedIn(v view) *admin.AccountInfo {
	r := m.tables[v].selected()
	if r == nil {
		return nil
	}
	for i := range m.snap.accounts {
		if m.snap.accounts[i].ID == r.id {
			return &m.snap.accounts[i]
		}
	}
	return nil
}

func (m *Model) selectedDevice() *admin.DeviceInfo {
	r := m.tables[viewDevices].selected()
	if r == nil {
		return nil
	}
	for i := range m.snap.devices {
		if m.snap.devices[i].ID == r.id {
			return &m.snap.devices[i]
		}
	}
	return nil
}

func (m *Model) selectedInvite() *admin.InviteInfo {
	r := m.tables[viewInvites].selected()
	if r == nil {
		return nil
	}
	for i := range m.snap.invites {
		if m.snap.invites[i].CodeHash == r.id {
			return &m.snap.invites[i]
		}
	}
	return nil
}

// ---- строки таблиц ----

// rebuild пересобирает строки всех таблиц из снимка с учётом scope и фильтра.
func (m *Model) rebuild() {
	now := m.now
	rows := make([]row, 0, len(m.snap.accounts))
	for _, a := range m.snap.accounts {
		st := a.Stats
		rows = append(rows, row{id: a.ID, cells: []string{
			a.ID, a.Name, a.CreatedAt.Local().Format("2006-01-02"),
			devicesCell(a.DevicesActive, a.DevicesRevoked),
			pendingCell(a.PendingInvites),
			groupInt(st.LatestSeq), groupInt(st.Rows), groupInt(int64(st.PhotoCount)),
			humanBytes(st.DBBytes + st.PhotoBytes), m.adm.QuotaLabel(a.Account), idleCell(a, now),
		}})
	}
	m.tables[viewAccounts].setRows(m.filtered(rows))

	// Дашборд: учётки по застою, самые тихие сверху.
	byIdle := append([]admin.AccountInfo(nil), m.snap.accounts...)
	sort.SliceStable(byIdle, func(i, j int) bool { return byIdle[i].Idle(now) > byIdle[j].Idle(now) })
	rows = rows[:0]
	for _, a := range byIdle {
		rows = append(rows, row{id: a.ID, cells: []string{
			a.Name, idleCell(a, now), localTime(a.LastActivity()),
			humanBytes(a.Stats.DBBytes + a.Stats.PhotoBytes),
			groupIntZero(int64(a.DevicesActive)), a.ID,
		}})
	}
	m.tables[viewDashboard].setRows(m.filtered(rows))

	rows = rows[:0]
	for _, d := range m.snap.devices {
		if m.scopeID != "" && d.AccountID != m.scopeID {
			continue
		}
		state, dim := sOK.Render("active"), false
		if !d.Active() {
			state, dim = "revoked", true
		}
		role := ""
		if d.IsAdmin {
			role = sWarn.Render("admin")
		}
		rows = append(rows, row{id: d.ID, dim: dim, cells: []string{
			d.ID, d.AccountName, d.Name, state, role,
			groupInt(d.Ops), ago(d.LastSeen, now), d.CreatedAt.Local().Format("2006-01-02"),
		}})
	}
	m.tables[viewDevices].setRows(m.filtered(rows))

	rows = rows[:0]
	for _, inv := range m.snap.invites {
		if m.scopeID != "" && inv.AccountID != m.scopeID {
			continue
		}
		state := inv.State(now)
		var stateCell, expires, used string
		dim := state != "pending"
		switch state {
		case "pending":
			stateCell = sOK.Render("pending")
			expires = sWarn.Render(countdown(inv.ExpiresAt, now))
		case "used":
			stateCell = "used"
			expires = localTime(inv.ExpiresAt)
			used = localTime(*inv.UsedAt)
		default:
			stateCell = "expired"
			expires = localTime(inv.ExpiresAt)
		}
		if used == "" {
			used = dash
		}
		rows = append(rows, row{id: inv.CodeHash, dim: dim, cells: []string{
			shortID(inv.CodeHash), inv.AccountName, roleOf(inv.GrantAdmin), stateCell, expires, used,
		}})
	}
	m.tables[viewInvites].setRows(m.filtered(rows))
}

func (m *Model) filtered(rows []row) []row {
	if m.filter == "" {
		return append([]row(nil), rows...)
	}
	out := make([]row, 0, len(rows))
	for _, r := range rows {
		if r.matches(m.filter) {
			out = append(out, r)
		}
	}
	return out
}

// idleCell — давность последней активности с подсветкой порогов застоя.
func idleCell(a admin.AccountInfo, now time.Time) string {
	idle := a.Idle(now)
	text := ago(a.LastActivity(), now)
	if a.Stats.LastReceive.IsZero() {
		text += " ∅" // ни одного пуша, считаем от создания
	}
	switch admin.IdleLevel(idle) {
	case 1:
		return sWarn.Render(text + " 1m+")
	case 2:
		return sHot.Render(text + " 6m+")
	case 3:
		return sErr.Render(text + " 1y+")
	}
	return text
}

func devicesCell(active, revoked int) string {
	if active == 0 && revoked == 0 {
		return dash
	}
	s := groupIntZero(int64(active))
	if revoked > 0 {
		s += sRowDim.Render(" +" + groupIntZero(int64(revoked)) + " revoked")
	}
	return s
}

func pendingCell(n int) string {
	if n == 0 {
		return dash
	}
	return sWarn.Render(groupIntZero(int64(n)))
}

func roleOf(admin bool) string {
	if admin {
		return "admin"
	}
	return "device"
}

func groupIntZero(n int64) string {
	if n == 0 {
		return "0"
	}
	return groupInt(n)
}

// ---- детали и помощь ----

func accountDetails(a admin.AccountInfo, m *Model) overlay {
	st := a.Stats
	lines := []string{
		kv("id", a.ID),
		kv("создана", localTime(a.CreatedAt)),
		kv("устройства", groupIntZero(int64(a.DevicesActive))+" активных, "+
			groupIntZero(int64(a.DevicesRevoked))+" отозвано"),
		kv("инвайты", groupIntZero(int64(a.PendingInvites))+" действующих"),
		"",
	}
	if !st.Exists {
		lines = append(lines, sLabel.Render("Лог правок ещё не создан — ни одно устройство не синхронизировалось."))
	} else {
		lines = append(lines,
			kv("instance_id", st.InstanceID),
			kv("latest_seq", groupIntZero(st.LatestSeq)),
			kv("последний push", localTime(st.LastReceive)+"  ("+ago(st.LastReceive, m.now)+")"),
			kv("rows", groupIntZero(st.Rows)),
		)
		for _, e := range store.SortedEntities(st.Entities) {
			lines = append(lines, kv("  "+e, groupIntZero(int64(st.Entities[e].Count))+
				"  "+sLabel.Render(st.Entities[e].Hash[:12])))
		}
		lines = append(lines,
			kv("фото", groupIntZero(int64(st.PhotoCount))+"  ("+humanBytes(st.PhotoBytes)+")"),
			kv("sync.db", humanBytes(st.DBBytes)),
			kv("квота", quotaDetail(a.Account, m)),
			"",
			sLabel.Render("Бэкап учётки — копия папки acc/"+a.ID+"/."),
		)
	}
	return &noticeOverlay{title: "Учётка · " + a.Name, lines: lines, wide: true}
}

func deviceDetails(d admin.DeviceInfo) overlay {
	state := sOK.Render("active")
	if !d.Active() {
		state = sErr.Render("revoked " + localTime(*d.RevokedAt))
	}
	lines := []string{
		kv("id", d.ID),
		kv("учётка", d.AccountName+"  "+sLabel.Render(d.AccountID)),
		kv("состояние", state),
		kv("роль", roleOf(d.IsAdmin)),
		kv("подключено", localTime(d.CreatedAt)),
		kv("прислало ops", groupIntZero(d.Ops)),
		kv("последний push", localTime(d.LastSeen)),
		"",
		sLabel.Render("Токен хранится хэшем и не восстанавливается: утеря — только revoke + новый код."),
	}
	return &noticeOverlay{title: "Устройство · " + d.Name, lines: lines, wide: true}
}

func helpOverlay() overlay {
	sec := func(t string) string { return sHead.Render(t) }
	k := func(key, text string) string { return "  " + padRight(sKey.Render(key), 22) + sValue.Render(text) }
	lines := []string{
		sec("Вкладки"),
		k("1 / 2 / 3 / 4", "учётки · устройства · коды · дашборд"),
		k("enter", "в учётках — устройства этой учётки; в устройствах — детали"),
		k("esc", "сбросить фильтр → выйти из учётки → (в учётках) ничего"),
		"",
		sec("Действия"),
		k("a", "новая учётка (потом предложит админ-код)"),
		k("i", "код приглашения для выбранной учётки"),
		k("x", "отозвать устройство / отменить код"),
		k("m", "снять админство с устройства"),
		k("d", "детали записи"),
		k("p", "чистка старых кодов (вкладка invites)"),
		k("D", "удалить учётку, молчавшую год (папка → data/trash)"),
		k("Q", "квота учётки (МБ; 0 — без лимита; пусто — глобальная)"),
		k("t", "очистить trash (дашборд)"),
		"",
		sec("Навигация"),
		k("↑↓ j k", "по строкам · pgup/pgdn · g/G — начало/конец"),
		k("/", "фильтр по подстроке (esc — сбросить)"),
		k("r", "перечитать данные (и так каждые 5 с)"),
		k("q", "выход"),
		"",
		sec("Команды (:)"),
		k(":accounts", ":devices [учётка]  :invites [учётка]"),
		k(":invite <учётка> [admin]", "выпустить код"),
		k(":add <имя>", "новая учётка · :purge — чистка кодов · :q — выход"),
		k(":delete <учётка>", "удалить молчавшую год учётку · :dash — дашборд"),
		k(":quota <учётка> <МБ|0|default>", "квота учётки; в таблице * — своя, без * — глобальная"),
		"",
		sLabel.Render("То же самое из CLI: synk account|invite|device … — см. man /opt/synk/synk.1"),
	}
	return &noticeOverlay{title: "synk — помощь", lines: lines, wide: true}
}

// quotaDetail — строка квоты в деталях: значение, откуда оно, сколько занято.
func quotaDetail(acc store.Account, m *Model) string {
	src := "глобальная"
	if acc.QuotaBytes != nil {
		src = "своя"
	}
	q := acc.EffectiveQuota(m.adm.DefaultQuota)
	if q <= 0 {
		return "без лимита (" + src + ")"
	}
	return humanBytes(q) + " (" + src + ")"
}
