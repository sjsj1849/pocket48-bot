package weverse

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostPasswordVerifiedStoredAndReused(t *testing.T) {
	const secret = "a+ &秘密"
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("lockPassword") != secret {
			w.WriteHeader(403)
			respond(w, Object{"errorCode": "post_700"})
			return
		}
		respond(w, Object{"postId": "1-123", "plainBody": "unlocked"})
	})
	row, e := c.SavePostPassword(context.Background(), "https://weverse.io/hearts2hearts/artist/1-123/comment/2-12", secret)
	if e != nil || row.Password != "" || row.PostID != "1-123" {
		t.Fatalf("save: %+v %v", row, e)
	}
	var post Object
	if e = c.call(context.Background(), "/post/v1.0/post-1-123?fieldSet=postV1", true, &post); e != nil {
		t.Fatal(e)
	}
	if text(post, "plainBody") != "unlocked" || calls != 2 {
		t.Fatal("stored password not reused")
	}
	_, e = c.SavePostPassword(context.Background(), row.URL, "wrong")
	if !errors.Is(e, ErrPostPassword) {
		t.Fatalf("wrong password: %v", e)
	}
	rows, _ := LoadPostPasswords(c.Dir)
	if len(rows) != 1 || rows[0].Password != secret {
		t.Fatal("failure overwrote saved password")
	}
	info, e := os.Stat(filepath.Join(c.Dir, "post-passwords.json"))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("password file not private")
	}
	ep, _ := c.withPostPassword("/post/v1.0/post-1-123/comments?fieldSet=x")
	if strings.Contains(ep, "lockPassword") {
		t.Fatal("password applied to unrelated endpoint")
	}
	if e = DeletePostPassword(c.Dir, "1-123"); e != nil {
		t.Fatal(e)
	}
	ep, _ = c.withPostPassword("/post/v1.0/post-1-123?fieldSet=x")
	u, _ := url.Parse(ep)
	if u.Query().Has("lockPassword") {
		t.Fatal("delete not honored")
	}
}
func TestPostURLRejectsUntrustedAndNonPostLinks(t *testing.T) {
	for _, raw := range []string{"https://evil.example/hearts2hearts/artist/1-123", "https://weverse.io@evil.example/a/artist/1-1", "https://weverse.io/hearts2hearts", "https://weverse.io/a/artist/../x", "https://weverse.io/a/live/1-123"} {
		if _, _, e := ParsePostURL(raw); e == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
