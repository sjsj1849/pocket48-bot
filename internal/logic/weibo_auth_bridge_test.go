package logic

import (
	"reflect"
	"testing"
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
