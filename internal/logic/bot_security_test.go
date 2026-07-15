package logic

import (
	"reflect"
	"testing"
)

func TestRedactCommandArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "password", args: []string{"login", "pwd", "a-b_c!"}, want: []string{"login", "pwd", "<redacted>"}},
		{name: "token", args: []string{"login", "secret-token"}, want: []string{"login", "<redacted>"}},
		{name: "sms mobile", args: []string{"login", "sms", "13800000000"}, want: []string{"login", "sms", "<redacted>"}},
		{name: "sms code", args: []string{"code", "123456"}, want: []string{"code", "<redacted>"}},
		{name: "ordinary", args: []string{"status"}, want: []string{"status"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := append([]string(nil), tt.args...)
			if got := redactCommandArgs(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("redactCommandArgs() = %#v, want %#v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.args, original) {
				t.Fatal("redactCommandArgs modified the caller's slice")
			}
		})
	}
}
