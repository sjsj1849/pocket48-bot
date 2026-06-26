package logic

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	mediaCacheDir    = "/tmp/bot48/mediacache"
	mediaFileTTL     = 5 * time.Minute
	mediaCleanupIntv = 5 * time.Minute
)

var mediaHTTPClient = &http.Client{
	Timeout: 20 * time.Second,
	Transport: &http.Transport{
		DisableKeepAlives: false,
	},
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

	// Download
	resp, err := mediaHTTPClient.Get(url)
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

	log.Printf("[Media] Cached %s → %s (%d bytes)", url, localPath, written)
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
