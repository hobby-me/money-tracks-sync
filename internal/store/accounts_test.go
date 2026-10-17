package store

import (
	"testing"
	"time"
)

func testAccounts(t *testing.T) *AccountsDB {
	t.Helper()
	a, err := OpenAccounts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func TestInviteRedeemFlow(t *testing.T) {
	a := testAccounts(t)
	acc, err := a.AddAccount("Alex")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := a.CreateInvite(acc.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	dev, token, err := a.Redeem(inv.Code, "iPhone")
	if err != nil {
		t.Fatal(err)
	}
	if dev.AccountID != acc.ID || !dev.IsAdmin || token == "" {
		t.Fatalf("bad device: %+v token=%q", dev, token)
	}
	// Код одноразовый: второй redeem обязан упасть.
	if _, _, err := a.Redeem(inv.Code, "Pixel"); err != ErrInviteInvalid {
		t.Fatalf("reused invite accepted: %v", err)
	}
	// Токен аутентифицирует, чужая строка — нет.
	got, err := a.Authenticate(token)
	if err != nil || got.ID != dev.ID {
		t.Fatalf("auth failed: %v %+v", err, got)
	}
	if _, err := a.Authenticate("wrong"); err != ErrNotFound {
		t.Fatalf("bad token accepted: %v", err)
	}
}

func TestRevokeKillsToken(t *testing.T) {
	a := testAccounts(t)
	acc, _ := a.AddAccount("X")
	inv, _ := a.CreateInvite(acc.ID, false)
	dev, token, _ := a.Redeem(inv.Code, "Mac")
	if err := a.RevokeDevice(dev.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(token); err != ErrNotFound {
		t.Fatalf("revoked token still works: %v", err)
	}
	if err := a.RevokeDevice(dev.ID); err != ErrNotFound {
		t.Fatalf("double revoke: %v", err)
	}
}

func TestInviteUnknownAccount(t *testing.T) {
	a := testAccounts(t)
	if _, err := a.CreateInvite("nope", false); err != ErrNotFound {
		t.Fatalf("invite for missing account: %v", err)
	}
}

func TestInviteExpiry(t *testing.T) {
	a := testAccounts(t)
	acc, _ := a.AddAccount("X")
	inv, _ := a.CreateInvite(acc.ID, false)
	// Просроченный код: подкручиваем expires_at в прошлое прямо в базе.
	if _, err := a.db.Exec(`UPDATE invites SET expires_at = ?`,
		time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Redeem(inv.Code, "Late"); err != ErrInviteInvalid {
		t.Fatalf("expired invite accepted: %v", err)
	}
}

func TestAccountQuotaAndMigration(t *testing.T) {
	dir := t.TempDir()
	// База «старой» схемы без quota_bytes: OpenAccounts обязан догнать её.
	old, err := openSQLite(dir + "/accounts.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at INT NOT NULL);
		INSERT INTO accounts VALUES ('A1', 'Legacy', 1)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	a, err := OpenAccounts(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	accs, err := a.ListAccounts()
	if err != nil || len(accs) != 1 || accs[0].QuotaBytes != nil {
		t.Fatalf("legacy account after migration: %v %+v", err, accs)
	}
	if accs[0].EffectiveQuota(100) != 100 {
		t.Fatal("nil quota must fall back to default")
	}
	// Повторное открытие не должно ломаться на уже добавленной колонке.
	if b, err := OpenAccounts(dir); err != nil {
		t.Fatal(err)
	} else {
		b.Close()
	}

	q := int64(512 << 20)
	if err := a.SetAccountQuota("A1", &q); err != nil {
		t.Fatal(err)
	}
	got, err := a.AccountQuota("A1")
	if err != nil || got == nil || *got != q {
		t.Fatalf("quota after set: %v %v", err, got)
	}
	zero := int64(0)
	if err := a.SetAccountQuota("A1", &zero); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.AccountQuota("A1"); got == nil || *got != 0 {
		t.Fatalf("unlimited quota: %v", got)
	}
	if err := a.SetAccountQuota("A1", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.AccountQuota("A1"); got != nil {
		t.Fatalf("reset to default: %v", *got)
	}
	neg := int64(-1)
	if err := a.SetAccountQuota("A1", &neg); err == nil {
		t.Fatal("negative quota accepted")
	}
	if err := a.SetAccountQuota("nope", &q); err != ErrNotFound {
		t.Fatalf("unknown account: %v", err)
	}
	if _, err := a.AccountQuota("nope"); err != ErrNotFound {
		t.Fatalf("unknown account quota: %v", err)
	}
}
