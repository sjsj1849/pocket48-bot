package outbound

import "testing"

func TestReplyMapRecordAndLookup(t *testing.T) {
	m := NewReplyMap(10)
	m.Record("src1", "msg1")
	m.Record("src2", "msg2")

	if got, ok := m.Lookup("src1"); !ok || got != "msg1" {
		t.Fatalf("Lookup(src1) = %q, %v; want msg1, true", got, ok)
	}
	if got, ok := m.Lookup("src2"); !ok || got != "msg2" {
		t.Fatalf("Lookup(src2) = %q, %v; want msg2, true", got, ok)
	}
	if _, ok := m.Lookup("missing"); ok {
		t.Fatal("Lookup(missing) should be false")
	}
}

func TestReplyMapEvictsOldest(t *testing.T) {
	m := NewReplyMap(2)
	m.Record("a", "1")
	m.Record("b", "2")
	m.Record("c", "3") // evicts "a"

	if _, ok := m.Lookup("a"); ok {
		t.Fatal("Lookup(a) should be evicted")
	}
	if got, ok := m.Lookup("c"); !ok || got != "3" {
		t.Fatalf("Lookup(c) = %q, %v; want 3, true", got, ok)
	}
}

func TestReplyMapNilSafe(t *testing.T) {
	var m *ReplyMap
	m.Record("a", "1") // must not panic
	if _, ok := m.Lookup("a"); ok {
		t.Fatal("nil map should not report a hit")
	}
}
