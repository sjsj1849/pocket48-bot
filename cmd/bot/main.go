package main

import (
	"io"
	"log"
	"os"
	"path/filepath"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/logger"
	"pocket48-bot/internal/logic"
)

func main() {
	// Setup Logging
	// Ensure log directory exists is handled by logger package, or we can do it here.
	// Logger will create it.

	// Create/Open log file with rotation (10MB limit, keep 5 backups)
	// Path: ./log/bot.log
	logPath := "log/bot.log"
	fileLogger, err := logger.New(logPath, 10*1024*1024, 5) // 10MB
	if err != nil {
		log.Fatalf("Failed to initialize file logger: %v", err)
	}

	// MultiWriter to write to both Stdout and File
	mw := io.MultiWriter(os.Stdout, fileLogger)
	log.SetOutput(mw)

	// log.SetFlags(log.LstdFlags | log.Lshortfile) // Optional: Add line numbers

	// Determine config path
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	dir := filepath.Dir(exe)

	// Look for config.json in current directory or executable directory
	configPath := "config.json"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		configPath = filepath.Join(dir, "config.json")
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	bot := logic.NewBot(cfg)
	log.Println("Starting Bot...")
	if err := bot.Start(); err != nil {
		log.Fatalf("Bot crashed: %v", err)
	}
}
