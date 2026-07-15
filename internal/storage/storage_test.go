package storage

import "testing"

func TestCursorRoundTripAndOverwrite(t *testing.T) {
	store := NewStorage(t.TempDir(), "")
	if err := store.SaveCursor(123, "first", 1000); err != nil {
		t.Fatalf("first SaveCursor() error = %v", err)
	}
	if err := store.SaveCursor(123, "second", 2000); err != nil {
		t.Fatalf("overwrite SaveCursor() error = %v", err)
	}
	cursor, err := store.GetCursor(123)
	if err != nil {
		t.Fatalf("GetCursor() error = %v", err)
	}
	if cursor == nil || cursor.LastMsgID != "second" || cursor.LastMsgTime != 2000 {
		t.Fatalf("unexpected cursor: %#v", cursor)
	}
}
