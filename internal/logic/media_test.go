package logic

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"pocket48-bot/internal/config"
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

func TestQChatMediaPrefersLocalCacheOverDirectURL(t *testing.T) {
	url := "https://invalid.example/qchat-local-media-test.jpg"
	hash := md5.Sum([]byte(url))
	cached := filepath.Join(mediaCacheDir, hex.EncodeToString(hash[:])+".jpg")
	if err := os.WriteFile(cached, []byte("cached"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(cached) })

	msg := &pocket48.Message{MsgIDServer: "local", Type: pocket48.MsgImage, DirectMedia: true}
	if got := (&Bot{}).mediaPathForMessage(msg, url); got != cached {
		t.Fatalf("mediaPathForMessage returned %q, want local cache %q", got, cached)
	}
}

func TestMediaPathFallsBackToRemoteURLWhenDownloadFails(t *testing.T) {
	// Port 1 is almost always closed → immediate connection refused (no 20s wait).
	url := "http://127.0.0.1:1/qchat-missing-media-test-no-cache.jpg"
	msg := &pocket48.Message{MsgIDServer: "fallback", Type: pocket48.MsgImage, DirectMedia: true}
	if got := (&Bot{}).mediaPathForMessage(msg, url); got != url {
		t.Fatalf("mediaPathForMessage returned %q, want remote fallback %q", got, url)
	}
}

func TestMediaPathRemoteModeSkipsLocalCache(t *testing.T) {
	url := "https://invalid.example/qchat-remote-mode-test.jpg"
	hash := md5.Sum([]byte(url))
	cached := filepath.Join(mediaCacheDir, hex.EncodeToString(hash[:])+".jpg")
	if err := os.WriteFile(cached, []byte("cached"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(cached) })

	msg := &pocket48.Message{MsgIDServer: "remote", Type: pocket48.MsgImage, DirectMedia: true}
	b := &Bot{cfg: &config.Config{MediaDelivery: "remote"}}
	if got := b.mediaPathForMessage(msg, url); got != url {
		t.Fatalf("remote mode returned %q, want direct URL %q", got, url)
	}
}
