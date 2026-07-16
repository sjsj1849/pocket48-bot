package main

import (
	"log"
	"os"

	"pocket48-bot/internal/admin"
)

func main() {
	server, err := admin.New(admin.Options{
		Address:      envOr("POCKET48_ADMIN_ADDR", "127.0.0.1:8787"),
		ConfigPath:   envOr("POCKET48_CONFIG_PATH", "/root/pocket48-bot/config.json"),
		LogPath:      envOr("POCKET48_LOG_PATH", "/root/pocket48-bot/bot.log"),
		PasswordPath: envOr("POCKET48_ADMIN_PASSWORD_FILE", "/root/pocket48-bot/storage/admin-password"),
		CookiePath:   envOr("POCKET48_ADMIN_COOKIE_PATH", "/"),
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Pocket48 Console listening on %s", server.Address())
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
