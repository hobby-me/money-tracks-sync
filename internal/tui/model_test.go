package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/hobby-me/money-tracks-sync/internal/admin"
	"github.com/hobby-me/money-tracks-sync/internal/store"
)

// seeded — учётка с двумя устройствами (одно отозвано) и висящим кодом.
func seeded(t *testing.T) (*Model, store.Account) {
	t.Helper()
	dir := t.TempDir()
	adm, err := admin.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adm.Close)
	acc, inv, err := adm.AddAccount("Alex", true)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := store.OpenAccounts(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { accounts.Close() })
	if _, _, err := accounts.Redeem(inv.Code, "iPhone 15"); err != nil {
		t.Fatal(err)
	}
	inv2, _ := adm.Invite(acc.ID, false)
	dev2, _, err := accounts.Redeem(inv2.Code, "Pixel 8")
	if err != nil {
		t.Fatal(err)
	}
	if err := adm.RevokeDevice(dev2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := adm.Invite(acc.ID, false); err != nil {
		t.Fatal(err)
	}
	m := newModel(adm, Options{DataDir: dir, Listen: "127.0.0.1:1", Version: "test"})
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m.Update(m.loadCmd()())
	return m, acc
}

func key(m *Model, k string) tea.Cmd {
	var msg tea.KeyMsg
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	_, cmd := m.Update(msg)
	return cmd
}

func plain(m *Model) string { return ansi.Strip(m.View()) }

func TestViewRendersAccountsAndFitsScreen(t *testing.T) {
	m, acc := seeded(t)
	out := plain(m)
	for _, want := range []string{"accounts[1]", acc.ID, "Alex", "<1> учётки", "1 +1 revoked"} {
		if !strings.Contains(out, want) {
			t.Errorf("view lacks %q:\n%s", want, out)
		}
	}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 40 {
		t.Fatalf("view height %d, want 40", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 140 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
}

func TestDrillDownScopeAndFilter(t *testing.T) {
	m, _ := seeded(t)
	key(m, "enter")
	out := plain(m)
	if m.view != viewDevices || !strings.Contains(out, "devices(Alex)[2]") {
		t.Fatalf("enter did not open devices of account:\n%s", out)
	}
	if !strings.Contains(out, "revoked") || !strings.Contains(out, "admin") {
		t.Fatalf("device states missing:\n%s", out)
	}
	key(m, "/")
	key(m, "pix")
	key(m, "enter")
	if n := len(m.table().rows); n != 1 || !strings.Contains(plain(m), "/pix") {
		t.Fatalf("filter: %d rows", n)
	}
	key(m, "esc") // сброс фильтра
	if m.filter != "" || len(m.table().rows) != 2 {
		t.Fatal("esc did not clear filter")
	}
	key(m, "esc") // выход из учётки
	if m.view != viewAccounts || m.scopeID != "" {
		t.Fatal("esc did not leave scope")
	}
}

func TestRevokeRequiresConfirmAndReloads(t *testing.T) {
	m, _ := seeded(t)
	key(m, "2")
	m.table().first() // iPhone — активное
	key(m, "x")
	if _, ok := m.overlay.(*confirmOverlay); !ok {
		t.Fatalf("expected confirm overlay, got %T", m.overlay)
	}
	if !strings.Contains(plain(m), "Отозвать устройство") {
		t.Fatal("confirm text missing")
	}
	key(m, "n")
	if m.overlay != nil {
		t.Fatal("n did not cancel")
	}
	key(m, "x")
	cmd := key(m, "y")
	if cmd == nil {
		t.Fatal("y produced no action")
	}
	res := cmd().(actionMsg)
	if res.err != nil {
		t.Fatal(res.err)
	}
	_, reload := m.Update(res)
	m.Update(reload())
	for _, d := range m.snap.devices {
		if d.Active() {
			t.Fatalf("device still active: %+v", d)
		}
	}
	if !strings.Contains(plain(m), "отозвано") {
		t.Fatal("status line missing")
	}
}

func TestInviteFlowShowsCode(t *testing.T) {
	m, _ := seeded(t)
	key(m, "i")
	if _, ok := m.overlay.(*pickOverlay); !ok {
		t.Fatalf("expected role picker, got %T", m.overlay)
	}
	cmd := key(m, "2") // администратор
	res := cmd().(actionMsg)
	if res.err != nil {
		t.Fatal(res.err)
	}
	m.Update(res)
	n, ok := m.overlay.(*noticeOverlay)
	if !ok || len(n.code) != 16 {
		t.Fatalf("invite notice: %T %+v", m.overlay, m.overlay)
	}
	out := plain(m)
	if !strings.Contains(out, n.code) || !strings.Contains(out, "истекает через ") || !strings.Contains(out, "Код:     "+n.code) {
		t.Fatalf("code not shown:\n%s", out)
	}
}

func TestCommandsAndHelp(t *testing.T) {
	m, acc := seeded(t)
	key(m, ":")
	key(m, "devices alex")
	key(m, "enter")
	if m.view != viewDevices || m.scopeID != acc.ID {
		t.Fatalf("command did not scope: view=%v scope=%q", m.view, m.scopeID)
	}
	key(m, ":")
	key(m, "invites nobody")
	key(m, "enter")
	if !m.statusErr || !strings.Contains(m.status, "не найдена") {
		t.Fatalf("bad ref not reported: %q", m.status)
	}
	key(m, "?")
	if !strings.Contains(plain(m), "synk — помощь") {
		t.Fatal("help not shown")
	}
	key(m, "esc")
	if cmd := key(m, "q"); cmd == nil {
		t.Fatal("q must quit")
	}
}

func TestNarrowTerminalStillFits(t *testing.T) {
	m, _ := seeded(t)
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 20})
	for i, l := range strings.Split(m.View(), "\n") {
		if w := ansi.StringWidth(l); w > 70 {
			t.Errorf("line %d is %d wide: %q", i, w, ansi.Strip(l))
		}
	}
	if len(strings.Split(m.View(), "\n")) != 20 {
		t.Fatal("height mismatch")
	}
}

func TestFormatHelpers(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		humanBytes(0):                      dash,
		humanBytes(512):                    "512B",
		humanBytes(1536):                   "1.5K",
		humanBytes(3 << 20):                "3M",
		groupInt(1234567):                  "1 234 567",
		groupInt(999):                      "999",
		ago(now.Add(-90*time.Second), now): "1m",
		ago(now.Add(-3*24*time.Hour), now): "3d",
		ago(time.Time{}, now):              dash,
		countdown(now.Add(9*time.Minute+5*time.Second), now): "9:05",
		countdown(now.Add(-time.Second), now):                "0:00",
		fit("абвгдеж", 4):                                    "абв…",
		shortID("01M1DYPEZPME09G03XDTE7N486"):                "DTE7N486",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}

func TestDashboardRendersAndDeleteGuard(t *testing.T) {
	m, acc := seeded(t)
	key(m, "4")
	out := plain(m)
	for _, want := range []string{"диск", "сервер", "застой учёток", "запросы по устройствам", "учётки по застою[1]", "живые (< 1 мес)", "свободно"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard lacks %q:\n%s", want, out)
		}
	}
	if len(strings.Split(m.View(), "\n")) != 40 {
		t.Fatal("dashboard height mismatch")
	}
	// Свежую учётку удалить нельзя — статус с объяснением, модалки нет.
	key(m, "D")
	if m.overlay != nil || !m.statusErr || !strings.Contains(m.status, "молчавшие год") {
		t.Fatalf("delete guard: overlay=%T status=%q", m.overlay, m.status)
	}
	key(m, ":")
	key(m, "delete alex")
	key(m, "enter")
	if m.overlay != nil || !m.statusErr {
		t.Fatal(":delete bypassed the guard")
	}
	// Health с метриками рисует RPS по устройствам.
	h := admin.Health{Up: true, Version: "v1", Counters: map[string]float64{"synk_requests_total": 10},
		Samples:   []admin.Sample{{Name: "synk_requests_total", Labels: map[string]string{"device": "-", "code": "401", "route": "GET /v1/ops"}, Value: 10}},
		CheckedAt: time.Now().Add(-5 * time.Second)}
	m.Update(healthMsg(h))
	h2 := h
	h2.Counters = map[string]float64{"synk_requests_total": 20}
	h2.Samples = []admin.Sample{{Name: "synk_requests_total", Labels: h.Samples[0].Labels, Value: 20}}
	h2.CheckedAt = time.Now()
	m.Update(healthMsg(h2))
	out = plain(m)
	if !strings.Contains(out, "без авторизации") || !strings.Contains(out, "● up v1") {
		t.Fatalf("device requests missing:\n%s", out)
	}
	_ = acc
}

// Квота: Q → число → enter задаёт свою; Q → пусто → enter возвращает
// на глобальную (пустой ввод здесь легитимен, в отличие от имени учётки).
func TestQuotaKeySetsAndResets(t *testing.T) {
	m, acc := seeded(t)
	m.adm.DefaultQuota = 1 << 30

	submit := func(text string) {
		t.Helper()
		key(m, "Q")
		if _, ok := m.overlay.(*inputOverlay); !ok {
			t.Fatalf("Q did not open input: %T", m.overlay)
		}
		for _, r := range text {
			key(m, string(r))
		}
		cmd := key(m, "enter")
		if cmd == nil {
			t.Fatalf("enter on %q returned no command", text)
		}
		m.Update(cmd())
		m.Update(m.loadCmd()())
	}

	submit("500")
	got, err := m.adm.ResolveAccount(acc.ID)
	if err != nil || got.QuotaBytes == nil || *got.QuotaBytes != 500<<20 {
		t.Fatalf("own quota after Q: %v %+v", err, got.QuotaBytes)
	}
	if !strings.Contains(plain(m), "500.0M*") {
		t.Fatal("table does not show own quota")
	}

	submit("")
	got, _ = m.adm.ResolveAccount(acc.ID)
	if got.QuotaBytes != nil {
		t.Fatalf("empty input must reset to global, got %d", *got.QuotaBytes)
	}
	if m.overlay != nil {
		t.Fatalf("overlay still open after empty enter: %T", m.overlay)
	}
}
