package logic

import (
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	mediaCacheDir    = "/tmp/bot48/mediacache"
	mediaFileTTL     = 5 * time.Minute
	mediaCleanupIntv = 5 * time.Minute
)

var mediaHTTPClient = newMediaHTTPClient()

func newMediaHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 32
	transport.MaxIdleConnsPerHost = 8
	transport.ForceAttemptHTTP2 = true
	return &http.Client{
		Timeout:   20 * time.Second,
		Transport: transport,
	}
}

type mediaDownloadTrace struct {
	mu sync.Mutex

	dnsStarted     time.Time
	connectStarted time.Time
	tlsStarted     time.Time
	dnsElapsed     time.Duration
	connectElapsed time.Duration
	tlsElapsed     time.Duration
	firstByte      time.Duration
	reused         bool
}

func (t *mediaDownloadTrace) clientTrace(started time.Time) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			t.mu.Lock()
			t.dnsStarted = time.Now()
			t.mu.Unlock()
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			t.mu.Lock()
			if !t.dnsStarted.IsZero() {
				t.dnsElapsed = time.Since(t.dnsStarted)
			}
			t.mu.Unlock()
		},
		ConnectStart: func(_, _ string) {
			t.mu.Lock()
			t.connectStarted = time.Now()
			t.mu.Unlock()
		},
		ConnectDone: func(_, _ string, _ error) {
			t.mu.Lock()
			if !t.connectStarted.IsZero() {
				t.connectElapsed = time.Since(t.connectStarted)
			}
			t.mu.Unlock()
		},
		TLSHandshakeStart: func() {
			t.mu.Lock()
			t.tlsStarted = time.Now()
			t.mu.Unlock()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			t.mu.Lock()
			if !t.tlsStarted.IsZero() {
				t.tlsElapsed = time.Since(t.tlsStarted)
			}
			t.mu.Unlock()
		},
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			t.reused = info.Reused
			t.mu.Unlock()
		},
		GotFirstResponseByte: func() {
			t.mu.Lock()
			t.firstByte = time.Since(started)
			t.mu.Unlock()
		},
	}
}

func (t *mediaDownloadTrace) snapshot() (time.Duration, time.Duration, time.Duration, time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.firstByte, t.dnsElapsed, t.connectElapsed, t.tlsElapsed, t.reused
}

func init() {
	if err := os.MkdirAll(mediaCacheDir, 0755); err != nil {
		log.Printf("[Media] Failed to create cache dir %s: %v", mediaCacheDir, err)
	}
}

// downloadMedia downloads a media file from url to local cache.
// Returns local file path on success, or empty string + error on failure.
func (b *Bot) downloadMedia(url string) (string, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return "", fmt.Errorf("empty url")
	}

	// Build local path from URL hash
	h := md5.Sum([]byte(url))
	filename := hex.EncodeToString(h[:]) + guessExt(url)
	localPath := filepath.Join(mediaCacheDir, filename)

	// Already cached
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil
	}
	// Extension-less CDN URLs are initially guessed as .dat, then saved using
	// their response Content-Type. A realtime prefetch must find that refined
	// path without issuing another request just to rediscover the extension.
	if matches, err := filepath.Glob(filepath.Join(mediaCacheDir, filename[:32]+".*")); err == nil {
		for _, match := range matches {
			if info, statErr := os.Stat(match); statErr == nil && !info.IsDir() {
				return match, nil
			}
		}
	}

	// Download and retain enough timing detail to distinguish CDN latency from
	// local processing when a media message is slow.
	started := time.Now()
	downloadTrace := &mediaDownloadTrace{}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build download request failed: %w", err)
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), downloadTrace.clientTrace(started)))
	resp, err := mediaHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	// Try to refine extension from Content-Type
	ct := resp.Header.Get("Content-Type")
	if ct != "" {
		if ext := extFromContentType(ct); ext != "" {
			correctPath := filepath.Join(mediaCacheDir, filename[:32]+ext)
			if correctPath != localPath {
				localPath = correctPath
				// Check again with correct extension
				if _, err := os.Stat(localPath); err == nil {
					return localPath, nil
				}
			}
		}
	}

	f, err := os.Create(localPath)
	if err != nil {
		return "", fmt.Errorf("create file failed: %w", err)
	}
	defer f.Close()

	written, err := io.Copy(f, resp.Body)
	if err != nil {
		f.Close()
		os.Remove(localPath)
		return "", fmt.Errorf("write file failed: %w", err)
	}

	elapsed := time.Since(started)
	firstByte, dnsElapsed, connectElapsed, tlsElapsed, reused := downloadTrace.snapshot()
	rateMiB := 0.0
	if elapsed > 0 {
		rateMiB = float64(written) / (1024 * 1024) / elapsed.Seconds()
	}
	log.Printf(
		"[Media] Cached %s → %s (%d bytes, total=%s, ttfb=%s, dns=%s, connect=%s, tls=%s, reused=%t, rate=%.2f MiB/s)",
		url, localPath, written, elapsed.Round(time.Millisecond), firstByte.Round(time.Millisecond),
		dnsElapsed.Round(time.Millisecond), connectElapsed.Round(time.Millisecond), tlsElapsed.Round(time.Millisecond), reused, rateMiB,
	)
	return localPath, nil
}

// guessExt guesses file extension from URL path
func guessExt(url string) string {
	// Try URL path first
	path := url
	if idx := strings.Index(url, "?"); idx >= 0 {
		path = url[:idx]
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp",
		".mp4", ".mov", ".avi", ".mkv", ".webm",
		".mp3", ".wav", ".ogg", ".aac", ".m4a", ".wma",
		".amr", ".silk":
		return ext
	}
	return ".dat"
}

// extFromContentType maps Content-Type to file extension
func extFromContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	switch {
	case strings.Contains(ct, "jpeg"):
		return ".jpg"
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "gif"):
		return ".gif"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "mp4"):
		return ".mp4"
	case strings.Contains(ct, "webm"):
		return ".webm"
	case strings.Contains(ct, "mp3"):
		return ".mp3"
	case strings.Contains(ct, "mpeg"):
		return ".mp3"
	case strings.Contains(ct, "wav"):
		return ".wav"
	case strings.Contains(ct, "ogg"):
		return ".ogg"
	case strings.Contains(ct, "aac"):
		return ".aac"
	case strings.Contains(ct, "mp4") || strings.Contains(ct, "video"):
		return ".mp4"
	default:
		return ""
	}
}

// runMediaCleanupLoop periodically removes expired cached media files.
func (b *Bot) runMediaCleanupLoop() {
	ticker := time.NewTicker(mediaCleanupIntv)
	defer ticker.Stop()

	// Initial cleanup on start
	b.cleanupExpiredMedia()

	for range ticker.C {
		b.cleanupExpiredMedia()
	}
}

func (b *Bot) cleanupExpiredMedia() {
	entries, err := os.ReadDir(mediaCacheDir)
	if err != nil {
		return
	}

	now := time.Now()
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > mediaFileTTL {
			path := filepath.Join(mediaCacheDir, entry.Name())
			if err := os.Remove(path); err == nil {
				removed++
			}
		}
	}
	if removed > 0 {
		log.Printf("[Media] Cleanup removed %d expired files from %s", removed, mediaCacheDir)
	}
}

// localMediaPath downloads the media to local cache and returns local path.
// Falls back to original URL on any error.
func (b *Bot) localMediaPath(url string) string {
	if url == "" {
		return ""
	}
	local, err := b.downloadMedia(url)
	if err != nil {
		return url // fallback
	}
	return local
}
