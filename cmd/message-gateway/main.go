package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"pocket48-bot/internal/gateway"
)

func main() {
	defaultPath := "storage/message-gateway.json"
	if executable, err := os.Executable(); err == nil {
		defaultPath = filepath.Join(filepath.Dir(executable), "storage/message-gateway.json")
	}
	path := flag.String("config", defaultPath, "message gateway config path")
	flag.Parse()
	store, err := gateway.OpenStore(*path)
	if err != nil {
		log.Fatal(err)
	}
	server, err := gateway.NewServer(store)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Message Gateway listening on %s", server.Address())
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
