package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hobby-me/money-tracks-sync/internal/store"
)

type env struct {
	t        *testing.T
	ts       *httptest.Server
	accounts *store.AccountsDB
	srv      *Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	accounts, err := store.OpenAccounts(dir)
	if err != nil {
		t.Fatal(err)
	}
	pool := store.NewPool(dir)
	srv := New(accounts, pool)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); pool.Close(); accounts.Close() })
	return &env{t: t, ts: ts, accounts: accounts, srv: srv}
}

func (e *env) do(method, path, token string, body any) (*http.Response, map[string]any) {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

// register заводит учётку через CLI-слой и регистрирует устройство по коду.
func (e *env) register(name string, admin bool) (token string, accountID string) {
	e.t.Helper()
	acc, err := e.accounts.AddAccount(name)
	if err != nil {
		e.t.Fatal(err)
	}
	inv, err := e.accounts.CreateInvite(acc.ID, admin)
	if err != nil {
		e.t.Fatal(err)
	}
	resp, out := e.do("POST", "/v1/register", "",
		map[string]string{"invite_code": inv.Code, "device_name": name + "-dev"})
	if resp.StatusCode != 200 {
		e.t.Fatalf("register: %d %v", resp.StatusCode, out)
	}
	return out["token"].(string), acc.ID
}

func TestFullCycle(t *testing.T) {
	e := newEnv(t)
	token, _ := e.register("Alex", true)

	// push
	resp, out := e.do("POST", "/v1/ops", token, map[string]any{
		"ops": []map[string]any{{
			"op_id": "01OP", "entity": "accounts", "entity_uuid": "u1",
			"hlc": "0000000000010001", "device_id": "d1", "payload": `{"name":"Основной"}`,
		}},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("push: %d %v", resp.StatusCode, out)
	}
	if out["latest_seq"].(float64) != 1 || out["instance_id"] == "" {
		t.Fatalf("push out: %v", out)
	}

	// повторный push — тот же seq
	_, out2 := e.do("POST", "/v1/ops", token, map[string]any{
		"ops": []map[string]any{{
			"op_id": "01OP", "entity": "accounts", "entity_uuid": "u1",
			"hlc": "0000000000010001", "device_id": "d1", "payload": `{"name":"Основной"}`,
		}},
	})
	acked := out2["acked"].([]any)[0].(map[string]any)
	if acked["seq"].(float64) != 1 {
		t.Fatalf("idempotency broken: %v", out2)
	}

	// pull
	resp, out = e.do("GET", "/v1/ops?after=0", token, nil)
	if resp.StatusCode != 200 || len(out["ops"].([]any)) != 1 {
		t.Fatalf("pull: %d %v", resp.StatusCode, out)
	}

	// checksum + snapshot
	_, sums := e.do("GET", "/v1/checksum", token, nil)
	ents := sums["entities"].(map[string]any)
	if ents["accounts"].(map[string]any)["count"].(float64) != 1 {
		t.Fatalf("checksum: %v", sums)
	}
	_, snap := e.do("GET", "/v1/snapshot", token, nil)
	if len(snap["rows"].([]any)) != 1 {
		t.Fatalf("snapshot: %v", snap)
	}
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/v1/ops?after=0", "/v1/checksum", "/v1/snapshot", "/v1/photos"} {
		resp, _ := e.do("GET", path, "", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without token: %d", path, resp.StatusCode)
		}
	}
	resp, _ := e.do("GET", "/v1/checksum", "garbage", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("garbage token: %d", resp.StatusCode)
	}
}

func TestAccountIsolationOverHTTP(t *testing.T) {
	e := newEnv(t)
	tokenA, _ := e.register("A", false)
	tokenB, _ := e.register("B", false)
	e.do("POST", "/v1/ops", tokenA, map[string]any{
		"ops": []map[string]any{{
			"op_id": "01OP", "entity": "tx", "entity_uuid": "u1",
			"hlc": "0000000000010001", "device_id": "d1", "payload": `{}`,
		}},
	})
	_, out := e.do("GET", "/v1/snapshot", tokenB, nil)
	if len(out["rows"].([]any)) != 0 {
		t.Fatalf("B sees A's data: %v", out)
	}
}

func TestAdminOnlyEndpoints(t *testing.T) {
	e := newEnv(t)
	plain, otherAcc := e.register("Plain", false)
	admin, _ := e.register("Admin", true)

	// Учётки через HTTP не заводятся вообще, даже админом.
	resp, _ := e.do("POST", "/v1/accounts", admin, map[string]string{"name": "New"})
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("/v1/accounts still served: %d", resp.StatusCode)
	}
	// Код приглашения — только с админ-устройства.
	resp, _ = e.do("POST", "/v1/invites", plain, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin issued invite: %d", resp.StatusCode)
	}
	resp, out := e.do("POST", "/v1/invites", admin, nil)
	if resp.StatusCode != 200 || out["invite_code"] == "" {
		t.Fatalf("admin own invite: %d %v", resp.StatusCode, out)
	}
	// В чужую учётку — нельзя даже админу: только CLI/TUI на сервере.
	resp, _ = e.do("POST", "/v1/invites?account="+otherAcc, admin, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin cross-account invite over HTTP: %d", resp.StatusCode)
	}
	// Слишком длинный device_id в правке отбивается.
	resp, _ = e.do("POST", "/v1/ops", plain, map[string]any{
		"ops": []map[string]any{{
			"op_id": "01OP", "entity": "tx", "entity_uuid": "u1", "hlc": "0000000000010001",
			"device_id": strings.Repeat("x", 65), "payload": `{}`,
		}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized device_id accepted: %d", resp.StatusCode)
	}
}

func TestPhotos(t *testing.T) {
	e := newEnv(t)
	token, _ := e.register("P", false)
	name := "01J8ZZZZZZZZZZZZZZZZZZZZZZ.jpg"

	put := func(body string) int {
		req, _ := http.NewRequest("PUT", e.ts.URL+"/v1/photos/"+name, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := put("JPEGDATA"); code != http.StatusNoContent {
		t.Fatalf("put: %d", code)
	}
	if code := put("JPEGDATA2"); code != http.StatusNoContent {
		t.Fatalf("re-put: %d", code) // идемпотентно, повтор перезаписывает
	}
	resp, out := e.do("GET", "/v1/photos", token, nil)
	if resp.StatusCode != 200 || len(out["names"].([]any)) != 1 {
		t.Fatalf("list: %d %v", resp.StatusCode, out)
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/v1/photos/"+name, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	getResp, _ := http.DefaultClient.Do(req)
	b := make([]byte, 16)
	n, _ := getResp.Body.Read(b)
	getResp.Body.Close()
	if string(b[:n]) != "JPEGDATA2" {
		t.Fatalf("get body: %q", b[:n])
	}
	// path traversal и мусорные имена
	for _, bad := range []string{"..%2Fsync.db", "a/b.jpg", "x.exe", "no-ext"} {
		req, _ := http.NewRequest("PUT", e.ts.URL+"/v1/photos/"+bad, strings.NewReader("x"))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent {
			t.Fatalf("bad name accepted: %s", bad)
		}
	}
	// delete идемпотентен
	req, _ = http.NewRequest("DELETE", e.ts.URL+"/v1/photos/"+name, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	dResp, _ := http.DefaultClient.Do(req)
	dResp.Body.Close()
	if dResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", dResp.StatusCode)
	}
	resp, _ = e.do("GET", "/v1/photos/"+name, token, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted photo still served: %d", resp.StatusCode)
	}
}

func TestRegisterRateLimit(t *testing.T) {
	e := newEnv(t)
	var last int
	for i := 0; i < 12; i++ {
		resp, _ := e.do("POST", "/v1/register", "",
			map[string]string{"invite_code": "WRONG", "device_name": "x"})
		last = resp.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("rate limit did not kick in: %d", last)
	}
}

// Метрики живут на отдельном порту: на API их нет вовсе, чтобы через
// домен никто не читал финансовую статистику.
func TestMetricsOnSeparatePortOnly(t *testing.T) {
	dir := t.TempDir()
	accounts, err := store.OpenAccounts(dir)
	if err != nil {
		t.Fatal(err)
	}
	pool := store.NewPool(dir)
	app := New(accounts, pool)
	api := httptest.NewServer(app.Handler())
	metrics := httptest.NewServer(app.MetricsHandler())
	t.Cleanup(func() { api.Close(); metrics.Close(); pool.Close(); accounts.Close() })

	resp, err := http.Get(api.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("api healthz: %v %v", err, resp)
	}
	resp, err = http.Get(api.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("metrics exposed on api port: %d", resp.StatusCode)
	}
	// Один неавторизованный запрос — должен попасть в synk_requests_total как device="-".
	resp, _ = http.Get(api.URL + "/v1/ops")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth pull: %d", resp.StatusCode)
	}

	resp, err = http.Get(metrics.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	body := buf.String()
	for _, want := range []string{
		"synk_build_info",
		"synk_disk_total_bytes",
		"synk_data_bytes",
		"synk_accounts 0",
		`synk_requests_total{code="401",device="-",route="GET /v1/ops"} 1`,
		"process_start_time_seconds",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
	if resp, _ := http.Get(metrics.URL + "/healthz"); resp.StatusCode != 200 {
		t.Fatalf("metrics healthz: %d", resp.StatusCode)
	}
}

func TestAccountQuota(t *testing.T) {
	e := newEnv(t)
	token, _ := e.register("Q", false)
	e.srv.Quota = 1 // байт: любая заливка фото вылезает за потолок

	req, _ := http.NewRequest("PUT", e.ts.URL+"/v1/photos/01J8ZZZZZZZZZZZZZZZZZZZZZZ.jpg", strings.NewReader("JPEGDATA"))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("photo over quota: %d", resp.StatusCode)
	}
	if _, out := e.do("GET", "/v1/photos", token, nil); len(out["names"].([]any)) != 0 {
		t.Fatalf("rejected photo persisted: %v", out)
	}

	push := func() int {
		resp, _ := e.do("POST", "/v1/ops", token, map[string]any{
			"ops": []map[string]any{{
				"op_id": "01OP", "entity": "tx", "entity_uuid": "u1",
				"hlc": "0000000000010001", "device_id": "d1", "payload": `{}`,
			}},
		})
		return resp.StatusCode
	}
	// Первый пуш: папки учётки ещё нет, занято 0 — проходит и создаёт sync.db.
	if code := push(); code != 200 {
		t.Fatalf("first push: %d", code)
	}
	// Теперь sync.db весит больше байта — следующий пуш отбивается.
	if code := push(); code != http.StatusInsufficientStorage {
		t.Fatalf("push over quota: %d", code)
	}
	e.srv.Quota = 0 // без ограничения
	if code := push(); code != 200 {
		t.Fatalf("push unlimited: %d", code)
	}
}

func TestPerAccountQuotaAndEndpoint(t *testing.T) {
	e := newEnv(t)
	token, accID := e.register("Q2", false)
	e.srv.Quota = 0 // глобально без лимита

	resp, out := e.do("GET", "/v1/quota", token, nil)
	if resp.StatusCode != 200 || out["quota_bytes"].(float64) != 0 || out["used_bytes"].(float64) != 0 {
		t.Fatalf("quota unlimited: %d %v", resp.StatusCode, out)
	}

	push := func() (int, map[string]any) {
		resp, out := e.do("POST", "/v1/ops", token, map[string]any{
			"ops": []map[string]any{{
				"op_id": "01OP", "entity": "tx", "entity_uuid": "u1",
				"hlc": "0000000000010001", "device_id": "d1", "payload": `{}`,
			}},
		})
		return resp.StatusCode, out
	}
	if code, _ := push(); code != 200 {
		t.Fatalf("push unlimited: %d", code)
	}
	// Индивидуальный потолок 1 байт перекрывает глобальный «без лимита».
	one := int64(1)
	if err := e.accounts.SetAccountQuota(accID, &one); err != nil {
		t.Fatal(err)
	}
	code, body := push()
	if code != http.StatusInsufficientStorage {
		t.Fatalf("push over per-account quota: %d", code)
	}
	if body["quota_bytes"].(float64) != 1 || body["used_bytes"].(float64) <= 1 {
		t.Fatalf("507 body must carry the numbers: %v", body)
	}
	resp, out = e.do("GET", "/v1/quota", token, nil)
	if resp.StatusCode != 200 || out["quota_bytes"].(float64) != 1 || out["used_bytes"].(float64) <= 1 {
		t.Fatalf("quota endpoint: %d %v", resp.StatusCode, out)
	}
	// Обратно: глобальный 1 байт, но у учётки «без лимита» — проходит.
	e.srv.Quota = 1
	zero := int64(0)
	if err := e.accounts.SetAccountQuota(accID, &zero); err != nil {
		t.Fatal(err)
	}
	if code, _ := push(); code != 200 {
		t.Fatalf("push with per-account unlimited: %d", code)
	}
	if _, out := e.do("GET", "/v1/quota", "", nil); out["error"] == nil {
		t.Fatal("quota without token must be denied")
	}
}
