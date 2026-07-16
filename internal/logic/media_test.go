package logic

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"pocket48-bot/internal/pocket48"
)

func TestDownloadMediaFindsRefinedExtensionCache(t *testing.T) {
	url := "https://invalid.example/media-without-extension-cache-test"
	hash := md5.Sum([]byte(url))
	cached := filepath.Join(mediaCacheDir, hex.EncodeToString(hash[:])+".jpg")
	if err := os.WriteFile(cached, []byte("cached"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(cached) })

	got, err := (&Bot{}).downloadMedia(url)
	if err != nil {
		t.Fatalf("downloadMedia unexpectedly requested an already cached URL: %v", err)
	}
	if got != cached {
		t.Fatalf("downloadMedia returned %q, want %q", got, cached)
	}
}

func TestQChatMediaUsesDirectNapCatURL(t *testing.T) {
	url := "https://invalid.example/qchat-direct-media-test.jpg"
	msg := &pocket48.Message{MsgIDServer: "direct", Type: pocket48.MsgImage, DirectMedia: true}
	if got := (&Bot{}).mediaPathForMessage(msg, url); got != url {
		t.Fatalf("mediaPathForMessage returned %q, want direct URL %q", got, url)
	}
}
