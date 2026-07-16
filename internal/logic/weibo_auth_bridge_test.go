package logic

import (
	"reflect"
	"testing"

	"github.com/gorilla/websocket"

	"pocket48-bot/internal/config"
)

func TestParseWeiboAuthCommand(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{name: "default", line: "", want: []string{"node", "./sidecar/weibo-auth/index.mjs"}},
		{name: "script only", line: "./sidecar/weibo-auth/index.mjs", want: []string{"node", "./sidecar/weibo-auth/index.mjs"}},
		{name: "explicit node", line: "node ./sidecar/weibo-auth/index.mjs", want: []string{"node", "./sidecar/weibo-auth/index.mjs"}},
		{name: "quoted path", line: `node "C:\\Program Files\\weibo auth\\index.mjs"`, want: []string{"node", `C:\\Program Files\\weibo auth\\index.mjs`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseWeiboAuthCommand(test.line)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseWeiboAuthCommand(%q) = %#v, want %#v", test.line, got, test.want)
			}
		})
	}
}

func TestParseWeiboAuthCommandRejectsUnclosedQuote(t *testing.T) {
	if _, err := parseWeiboAuthCommand(`node "broken`); err == nil {
		t.Fatal("expected an unclosed quote error")
	}
}

func TestWeiboAuthBridgeBeginStopSuppressesAvailabilityWithoutSkippingStop(t *testing.T) {
	b := NewWeiboAuthBridge(&config.Config{})
	b.started = true
	b.conn = &websocket.Conn{}

	b.BeginStop()

	if b.IsStarted() {
		t.Fatal("bridge should no longer report as started after shutdown begins")
	}
	if !b.stopping {
		t.Fatal("bridge should suppress disconnect errors after shutdown begins")
	}
	if b.stopRun {
		t.Fatal("BeginStop must not consume the subsequent Stop call")
	}
}
