package main

import (
	"log"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
)

func main() {
	target := "ws://8.133.242.240:3001"
	token := "Zhe945dmima1145141919810"

	// Test 1: Header Auth with Bearer
	log.Println("Test 1: Header Auth with Bearer")
	h1 := http.Header{}
	h1.Add("Authorization", "Bearer "+token)
	tryConnect(target, h1)

	// Test 2: Header Auth without Bearer
	log.Println("Test 2: Header Auth without Bearer")
	h2 := http.Header{}
	h2.Add("Authorization", token)
	tryConnect(target, h2)

	// Test 3: Query Param Auth
	log.Println("Test 3: Query Param Auth")
	u, _ := url.Parse(target)
	q := u.Query()
	q.Set("access_token", token)
	u.RawQuery = q.Encode()
	tryConnect(u.String(), nil)

	// Test 4: Path variations
	paths := []string{"/ws", "/onebot/v11/ws"} // / is tested by default
	for _, p := range paths {
		log.Printf("Test Path: %s\n", p)
		tryConnect(target+p, h1)
	}
}

func tryConnect(urlStr string, headers http.Header) {
	conn, resp, err := websocket.DefaultDialer.Dial(urlStr, headers)
	if err != nil {
		log.Printf("Failed: %v", err)
		if resp != nil {
			log.Printf("Status: %s", resp.Status)
		}
		return
	}
	defer conn.Close()
	log.Println("Success!")

	// Try to read one message
	_, msg, err := conn.ReadMessage()
	if err != nil {
		log.Printf("Read Error: %v", err)
	} else {
		log.Printf("Received: %s", string(msg))
	}
}
