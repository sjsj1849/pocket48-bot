package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger implements a file logger with size-based rotation
type Logger struct {
	filename   string
	maxSize    int64
	maxBackups int
	file       *os.File
	size       int64
	mu         sync.Mutex
}

// New creates a new Logger
// filename: path to log file
// maxSize: max bytes before rotation
// maxBackups: max old files to keep (0 = unlimited)
func New(filename string, maxSize int64, maxBackups int) (*Logger, error) {
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	l := &Logger{
		filename:   filename,
		maxSize:    maxSize,
		maxBackups: maxBackups,
	}

	// Open existing file to check size
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	l.file = file
	l.size = info.Size()

	return l, nil
}

// Write implements io.Writer
func (l *Logger) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Check rotation
	if l.size+int64(len(p)) > l.maxSize {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	}

	n, err = l.file.Write(p)
	l.size += int64(n)
	return n, err
}

func (l *Logger) rotate() error {
	// Close current file
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}

	// Rename current file to backup
	if _, err := os.Stat(l.filename); err == nil {
		timestamp := time.Now().Format("2006-01-02-15-04-05")
		backupName := fmt.Sprintf("%s.%s.bak", l.filename, timestamp)
		if err := os.Rename(l.filename, backupName); err != nil {
			return err
		}
	}

	// Open new file
	file, err := os.OpenFile(l.filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	l.file = file
	l.size = 0

	// Cleanup old backups (Basic implementation: just keeping it simple for now as per user request to be "controllable")
	// For now, sticking to just rotation to prevent single file growth.
	// Implementing MaxBackups logic if needed:
	// Find all files matching pattern, sort by time, delete oldest > MaxBackups.

	if l.maxBackups > 0 {
		l.cleanup()
	}

	return nil
}

func (l *Logger) cleanup() {
	dir := filepath.Dir(l.filename)
	base := filepath.Base(l.filename)

	// Assuming backups are base + "." + timestamp + ".bak"
	// We just look for files starting with base + "." and ending with ".bak"

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	var backups []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if len(entry.Name()) > len(base) && entry.Name()[:len(base)] == base && filepath.Ext(entry.Name()) == ".bak" {
			backups = append(backups, filepath.Join(dir, entry.Name()))
		}
	}

	// If count > limit, delete oldest?
	// The timestamp format YYYY-MM.... ensures alphabetical order is chronological order.
	// So we delete from the start of the list if we have too many.

	if len(backups) > l.maxBackups {
		// Sorted by name (timestamp) ascending -> Oldest first.
		// Delete the oldest ones.
		toDelete := len(backups) - l.maxBackups
		for i := 0; i < toDelete; i++ {
			os.Remove(backups[i])
		}
	}
}
