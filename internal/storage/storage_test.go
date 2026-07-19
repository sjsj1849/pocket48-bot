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

func TestSaveCursorNeverRegresses(t *testing.T) {
	store := NewStorage(t.TempDir(), "")
	if err := store.SaveCursor(7, "new", 5000); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCursor(7, "old", 4000); err != nil {
		t.Fatal(err)
	}
	cursor, err := store.GetCursor(7)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.LastMsgTime != 5000 || cursor.LastMsgID != "new" {
		t.Fatalf("cursor regressed: %#v", cursor)
	}
	// empty id on newer/same time keeps previous id
	if err := store.SaveCursor(7, "", 5000); err != nil {
		t.Fatal(err)
	}
	cursor, err = store.GetCursor(7)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.LastMsgID != "new" || cursor.LastMsgTime != 5000 {
		t.Fatalf("empty id overwrite broke cursor: %#v", cursor)
	}
}

func TestQChatIdentityRoundTrip(t *testing.T) {
	store := NewStorage(t.TempDir(), "")
	want := QChatIdentity{Account: "opaque-accid", UserID: 63559, Nickname: "owner", UpdatedAt: 1234}
	if err := store.SaveQChatIdentity(1279287, want); err != nil {
		t.Fatalf("SaveQChatIdentity() error = %v", err)
	}
	got, err := store.GetQChatIdentity(1279287)
	if err != nil {
		t.Fatalf("GetQChatIdentity() error = %v", err)
	}
	if got == nil || *got != want {
		t.Fatalf("GetQChatIdentity() = %#v, want %#v", got, want)
	}
}
