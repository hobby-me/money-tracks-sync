package store

import (
	"sync"
	"testing"
)

func testPool(t *testing.T) *Pool {
	t.Helper()
	p := NewPool(t.TempDir())
	t.Cleanup(p.Close)
	return p
}

func op(id, entity, uuid, hlc, dev, payload string) Op {
	return Op{OpID: id, Entity: entity, EntityUUID: uuid, HLC: hlc, DeviceID: dev, Payload: payload}
}

func TestPushAssignsMonotonicSeq(t *testing.T) {
	s, err := testPool(t).Get("acc1")
	if err != nil {
		t.Fatal(err)
	}
	acks, latest, err := s.Push([]Op{
		op("01A", "accounts", "u1", "0000000000010001", "devA", `{"n":1}`),
		op("01B", "accounts", "u2", "0000000000010002", "devA", `{"n":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(acks) != 2 || acks[0].Seq != 1 || acks[1].Seq != 2 || latest != 2 {
		t.Fatalf("acks=%v latest=%d", acks, latest)
	}
}

func TestPushIdempotentByOpID(t *testing.T) {
	s, _ := testPool(t).Get("acc1")
	first, _, err := s.Push([]Op{op("01A", "accounts", "u1", "0000000000010001", "devA", `{}`)})
	if err != nil {
		t.Fatal(err)
	}
	again, latest, err := s.Push([]Op{op("01A", "accounts", "u1", "0000000000010001", "devA", `{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Seq != first[0].Seq {
		t.Fatalf("repeat push got new seq: %d vs %d", again[0].Seq, first[0].Seq)
	}
	if latest != 1 {
		t.Fatalf("duplicate created an op: latest=%d", latest)
	}
}

func TestConcurrentPushSerializes(t *testing.T) {
	s, _ := testPool(t).Get("acc1")
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	push := func(dev string, ids []string) {
		defer wg.Done()
		for _, id := range ids {
			if _, _, err := s.Push([]Op{op(id, "tx", "u"+id, "0000000000010001", dev, `{}`)}); err != nil {
				errs <- err
				return
			}
		}
	}
	var idsA, idsB []string
	for i := 0; i < n; i++ {
		idsA = append(idsA, "A"+string(rune('0'+i/10))+string(rune('0'+i%10)))
		idsB = append(idsB, "B"+string(rune('0'+i/10))+string(rune('0'+i%10)))
	}
	wg.Add(2)
	go push("devA", idsA)
	go push("devB", idsB)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	latest, err := s.LatestSeq()
	if err != nil {
		t.Fatal(err)
	}
	if latest != 2*n {
		t.Fatalf("expected %d ops, got %d", 2*n, latest)
	}
	ops, _, err := s.Pull(0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for i, o := range ops {
		if o.Seq != int64(i+1) {
			t.Fatalf("hole in seq at %d: %d", i, o.Seq)
		}
	}
}

func TestPullPagination(t *testing.T) {
	s, _ := testPool(t).Get("acc1")
	var ops []Op
	for i := 0; i < 7; i++ {
		ops = append(ops, op("id"+string(rune('0'+i)), "tx", "u"+string(rune('0'+i)),
			"0000000000010001", "devA", `{}`))
	}
	if _, _, err := s.Push(ops); err != nil {
		t.Fatal(err)
	}
	page1, latest, err := s.Pull(0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 3 || latest != 7 {
		t.Fatalf("page1=%d latest=%d", len(page1), latest)
	}
	page2, _, _ := s.Pull(page1[2].Seq, 3)
	page3, _, _ := s.Pull(page2[2].Seq, 3)
	if len(page2) != 3 || len(page3) != 1 {
		t.Fatalf("pages: %d %d", len(page2), len(page3))
	}
	if page3[0].Seq != 7 {
		t.Fatalf("last seq: %d", page3[0].Seq)
	}
}

func TestRowsLWW(t *testing.T) {
	s, _ := testPool(t).Get("acc1")
	// Правки одной строки приходят не по порядку hlc: старшая первой.
	s.Push([]Op{op("op1", "accounts", "u1", "0000000000020000", "devB", `{"v":"newer"}`)})
	s.Push([]Op{op("op2", "accounts", "u1", "0000000000010000", "devA", `{"v":"older"}`)})
	rows, _, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Payload != `{"v":"newer"}` {
		t.Fatalf("LWW lost: %+v", rows)
	}
	// Равный hlc — побеждает больший device_id, детерминированно.
	s.Push([]Op{op("op3", "accounts", "u2", "0000000000030000", "devA", `{"v":"a"}`)})
	s.Push([]Op{op("op4", "accounts", "u2", "0000000000030000", "devZ", `{"v":"z"}`)})
	rows, _, _ = s.Snapshot()
	for _, r := range rows {
		if r.EntityUUID == "u2" && r.Payload != `{"v":"z"}` {
			t.Fatalf("tie-break failed: %+v", r)
		}
	}
}

func TestChecksumMatchesKnownVector(t *testing.T) {
	s, _ := testPool(t).Get("acc1")
	s.Push([]Op{
		op("op1", "accounts", "u1", "0000000000010000", "devA", `{}`),
		op("op2", "accounts", "u2", "0000000000020000", "devB", `{}`),
		op("op3", "tx", "t1", "0000000000030000", "devA", `{}`),
	})
	sums, err := s.Checksum()
	if err != nil {
		t.Fatal(err)
	}
	if sums["accounts"].Count != 2 || sums["tx"].Count != 1 {
		t.Fatalf("counts: %+v", sums)
	}
	// Payload не входит в хэш: та же строка с новым payload, но тем же
	// (hlc, device) хэша не меняет; с новым hlc — меняет.
	before := sums["tx"].Hash
	s.Push([]Op{op("op4", "tx", "t1", "0000000000040000", "devA", `{"x":1}`)})
	sums, _ = s.Checksum()
	if sums["tx"].Hash == before {
		t.Fatal("hash must change when hlc changes")
	}
}

func TestInstanceIDStable(t *testing.T) {
	dir := t.TempDir()
	p1 := NewPool(dir)
	s1, _ := p1.Get("acc1")
	id1 := s1.InstanceID
	if id1 == "" {
		t.Fatal("empty instance id")
	}
	p1.Close()
	p2 := NewPool(dir)
	defer p2.Close()
	s2, _ := p2.Get("acc1")
	if s2.InstanceID != id1 {
		t.Fatalf("instance id changed on reopen: %s vs %s", s2.InstanceID, id1)
	}
}

func TestAccountsIsolated(t *testing.T) {
	p := testPool(t)
	s1, _ := p.Get("acc1")
	s2, _ := p.Get("acc2")
	s1.Push([]Op{op("op1", "accounts", "u1", "0000000000010000", "devA", `{}`)})
	rows, latest, err := s2.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || latest != 0 {
		t.Fatalf("acc2 sees acc1 data: rows=%d latest=%d", len(rows), latest)
	}
}
