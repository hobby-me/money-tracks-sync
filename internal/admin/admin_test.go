package admin

import (
	"errors"
	"testing"
	"time"

	"github.com/hobby-me/money-tracks-sync/internal/store"
)

func testAdmin(t *testing.T) *Admin {
	t.Helper()
	a, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func TestAddAccountBootstrapAndResolve(t *testing.T) {
	a := testAdmin(t)
	acc, inv, err := a.AddAccount("  Alex ", true)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Name != "Alex" || inv == nil || inv.Code == "" {
		t.Fatalf("bad bootstrap: %+v %+v", acc, inv)
	}
	if _, _, err := a.AddAccount("   ", false); err == nil {
		t.Fatal("empty name accepted")
	}

	got, err := a.ResolveAccount("alex")
	if err != nil || got.ID != acc.ID {
		t.Fatalf("resolve by name: %v %+v", err, got)
	}
	got, err = a.ResolveAccount(acc.ID)
	if err != nil || got.ID != acc.ID {
		t.Fatalf("resolve by id: %v", err)
	}
	if _, err := a.ResolveAccount("nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown ref: %v", err)
	}
	if _, _, err := a.AddAccount("ALEX", false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ResolveAccount("Alex"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("duplicate name must be ambiguous: %v", err)
	}
}

func TestAccountsDevicesInvitesOverview(t *testing.T) {
	a := testAdmin(t)
	acc, inv, err := a.AddAccount("Alex", true)
	if err != nil {
		t.Fatal(err)
	}
	dev, _, err := a.accounts.Redeem(inv.Code, "iPhone")
	if err != nil {
		t.Fatal(err)
	}
	// Второе устройство отзываем, третий код оставляем висеть.
	inv2, err := a.Invite("Alex", false)
	if err != nil {
		t.Fatal(err)
	}
	dev2, _, err := a.accounts.Redeem(inv2.Code, "Pixel")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeDevice(dev2.ID); err != nil {
		t.Fatal(err)
	}
	pending, err := a.Invite(acc.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	// Активность: одно устройство что-то запушило.
	sdb, err := a.pool.Get(acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sdb.Push([]store.Op{
		{OpID: "op1", DeviceID: dev.ID, Entity: "transaction", EntityUUID: "u1", HLC: "0001", Payload: "{}"},
		{OpID: "op2", DeviceID: dev.ID, Entity: "category", EntityUUID: "u2", HLC: "0002", Payload: "{}"},
	}); err != nil {
		t.Fatal(err)
	}

	accs, err := a.Accounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(accs) != 1 {
		t.Fatalf("accounts: %d", len(accs))
	}
	ai := accs[0]
	if ai.DevicesActive != 1 || ai.DevicesRevoked != 1 || ai.PendingInvites != 1 {
		t.Fatalf("counts: %+v", ai)
	}
	if !ai.Stats.Exists || ai.Stats.LatestSeq != 2 || ai.Stats.Rows != 2 || ai.Stats.LastReceive.IsZero() {
		t.Fatalf("stats: %+v", ai.Stats)
	}
	if ai.Stats.Entities["transaction"].Count != 1 {
		t.Fatalf("entities: %+v", ai.Stats.Entities)
	}

	devs, err := a.Devices("alex")
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 || devs[0].AccountName != "Alex" {
		t.Fatalf("devices: %+v", devs)
	}
	if devs[0].Ops != 2 || devs[0].LastSeen.IsZero() || devs[1].Ops != 0 {
		t.Fatalf("activity: %+v", devs)
	}
	if devs[0].Active() == false || devs[1].Active() == true {
		t.Fatalf("states: %+v", devs)
	}

	invs, err := a.Invites("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	states := map[string]int{}
	for _, i := range invs {
		states[i.State(now)]++
	}
	if states["used"] != 2 || states["pending"] != 1 {
		t.Fatalf("invite states: %v", states)
	}
	// Отменяется только действующий; погашенный — история.
	var pendingHash, usedHash string
	for _, i := range invs {
		if i.State(now) == "pending" {
			pendingHash = i.CodeHash
		} else {
			usedHash = i.CodeHash
		}
	}
	if err := a.CancelInvite(usedHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cancel used: %v", err)
	}
	if err := a.CancelInvite(pendingHash); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.accounts.Redeem(pending.Code, "X"); !errors.Is(err, store.ErrInviteInvalid) {
		t.Fatalf("cancelled code redeemed: %v", err)
	}
	n, err := a.PurgeInvites(0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("purge removed fresh codes: %d", n)
	}
}

func TestStatsWithoutSyncDBDoesNotCreateDir(t *testing.T) {
	a := testAdmin(t)
	acc, _, err := a.AddAccount("Fresh", false)
	if err != nil {
		t.Fatal(err)
	}
	accs, err := a.Accounts()
	if err != nil {
		t.Fatal(err)
	}
	if accs[0].Stats.Exists {
		t.Fatal("stats claim sync.db exists")
	}
	if _, err := a.Devices(acc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := osStat(a.pool.AccountDir(acc.ID)); err == nil {
		t.Fatal("account dir was created by a read-only overview")
	}
}

func TestParseMetrics(t *testing.T) {
	var h Health
	h.Counters = map[string]float64{}
	parseMetrics(`# HELP synk_build_info Build metadata; value is always 1.
synk_build_info{build_date="x",git_commit="abc",go_version="go1.27",version="v1.2.3"} 1
synk_ops_received_total 1234
synk_errors_total{handler="push"} 2
synk_errors_total{handler="pull"} 3
go_goroutines 7
`, &h)
	if h.Version != "v1.2.3" {
		t.Fatalf("version: %q", h.Version)
	}
	if h.Counter("synk_ops_received_total") != 1234 || h.Counter("synk_errors_total") != 5 {
		t.Fatalf("counters: %v", h.Counters)
	}
	if _, ok := h.Counters["go_goroutines"]; ok {
		t.Fatal("foreign metric leaked in")
	}
}

func TestOverviewAndDeleteAccount(t *testing.T) {
	a := testAdmin(t)
	acc, inv, err := a.AddAccount("Old", true)
	if err != nil {
		t.Fatal(err)
	}
	dev, _, err := a.accounts.Redeem(inv.Code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	sdb, err := a.pool.Get(acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sdb.Push([]store.Op{{OpID: "o1", DeviceID: dev.ID, Entity: "t", EntityUUID: "u", HLC: "1", Payload: "{}"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.AddAccount("Empty", false); err != nil {
		t.Fatal(err)
	}

	ov, err := a.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if ov.Disk.TotalBytes <= 0 || ov.Disk.FreeBytes <= 0 || ov.DataBytes <= 0 {
		t.Fatalf("disk: %+v data=%d", ov.Disk, ov.DataBytes)
	}
	if len(ov.Accounts) != 2 || ov.Accounts[0].Name != "Old" { // самая большая сверху
		t.Fatalf("accounts order: %+v", ov.Accounts)
	}
	if ov.IdleCounts[0] != 2 || ov.AccountsBytes() <= 0 {
		t.Fatalf("idle counts %v bytes %d", ov.IdleCounts, ov.AccountsBytes())
	}

	// Свежую учётку без force удалять нельзя.
	if _, _, err := a.DeleteAccount("Old", false); !errors.Is(err, ErrStillActive) {
		t.Fatalf("fresh account deleted: %v", err)
	}
	got, trash, err := a.DeleteAccount("old", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != acc.ID || trash == "" {
		t.Fatalf("delete: %+v trash=%q", got, trash)
	}
	if _, err := osStat(trash + "/sync.db"); err != nil {
		t.Fatalf("sync.db not in trash: %v", err)
	}
	if _, err := osStat(a.pool.AccountDir(acc.ID)); err == nil {
		t.Fatal("account dir still in place")
	}
	if _, err := a.ResolveAccount(acc.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("account still resolvable: %v", err)
	}
	if devs, _ := a.Devices(""); len(devs) != 0 {
		t.Fatalf("devices left: %+v", devs)
	}
	if invs, _ := a.Invites(""); len(invs) != 0 {
		t.Fatalf("invites left: %+v", invs)
	}
	// Учётка без папки удаляется без trash.
	if _, trash, err := a.DeleteAccount("Empty", true); err != nil || trash != "" {
		t.Fatalf("empty account: %v %q", err, trash)
	}
	ov, _ = a.Overview()
	if ov.TrashBytes <= 0 {
		t.Fatal("trash should hold the old account")
	}
	if n, err := a.EmptyTrash(); err != nil || n <= 0 {
		t.Fatalf("empty trash: %v %d", err, n)
	}
	if _, err := osStat(a.trashDir()); err == nil {
		t.Fatal("trash dir still exists")
	}
}

func TestIdleLevels(t *testing.T) {
	cases := map[time.Duration]int{
		0: 0, 29 * 24 * time.Hour: 0, 31 * 24 * time.Hour: 1,
		200 * 24 * time.Hour: 2, 400 * 24 * time.Hour: 3,
	}
	for d, want := range cases {
		if got := IdleLevel(d); got != want {
			t.Errorf("IdleLevel(%v)=%d want %d", d, got, want)
		}
	}
	if IdleLabel(IdleYear) != "1y+" || IdleLabel(0) != "" {
		t.Fatal("labels")
	}
}

func TestParseMetricsLabelsAndProcess(t *testing.T) {
	var h Health
	h.Counters = map[string]float64{}
	parseMetrics(`synk_requests_total{code="200",device="dev1",route="POST /v1/ops"} 10
synk_requests_total{code="500",device="dev1",route="POST /v1/ops"} 2
synk_requests_total{code="401",device="-",route="GET /v1/ops"} 3
synk_disk_free_bytes 1.5e+10
process_start_time_seconds 1.7e+09
process_resident_memory_bytes 3.2e+07
`, &h)
	if h.Counter("synk_requests_total") != 15 {
		t.Fatalf("total: %v", h.Counter("synk_requests_total"))
	}
	if by := h.RequestsByDevice(); by["dev1"] != 12 || by["-"] != 3 {
		t.Fatalf("by device: %v", by)
	}
	if e := h.ErrorsByDevice(); e["dev1"] != 2 || e["-"] != 0 {
		t.Fatalf("5xx by device: %v", e)
	}
	if e := h.ErrorsByRoute(); e["GET /v1/ops"] != 3 || e["POST /v1/ops"] != 2 {
		t.Fatalf("4xx/5xx by route: %v", e)
	}
	if h.Gauge("synk_disk_free_bytes") != 1.5e10 || h.RSSBytes != 32000000 || h.StartedAt.Unix() != 1700000000 {
		t.Fatalf("gauges: %v %d %v", h.Gauge("synk_disk_free_bytes"), h.RSSBytes, h.StartedAt)
	}
	if h.Uptime(time.Unix(1700003600, 0)) != time.Hour {
		t.Fatal("uptime")
	}
}

func TestSetQuotaAndLabel(t *testing.T) {
	a := testAdmin(t)
	a.DefaultQuota = 1 << 30
	acc, _, err := a.AddAccount("Quota", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.QuotaLabel(acc); got != "1.0G" {
		t.Fatalf("default label: %q", got)
	}
	q := int64(512 << 20)
	acc, err = a.SetQuota("quota", &q) // по имени, без учёта регистра
	if err != nil {
		t.Fatal(err)
	}
	if got := a.QuotaLabel(acc); got != "512.0M*" {
		t.Fatalf("own label: %q", got)
	}
	zero := int64(0)
	if acc, err = a.SetQuota(acc.ID, &zero); err != nil || a.QuotaLabel(acc) != "∞*" {
		t.Fatalf("unlimited label: %v %q", err, a.QuotaLabel(acc))
	}
	if acc, err = a.SetQuota(acc.ID, nil); err != nil || a.QuotaLabel(acc) != "1.0G" {
		t.Fatalf("back to default: %v %q", err, a.QuotaLabel(acc))
	}
	accs, err := a.Accounts()
	if err != nil || len(accs) != 1 || accs[0].QuotaBytes != nil {
		t.Fatalf("listed quota: %v %+v", err, accs)
	}
	if _, err := a.SetQuota("nobody", &q); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
}
