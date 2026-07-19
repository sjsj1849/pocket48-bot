package admin

import (
	"reflect"
	"testing"
)

func TestValidateConfigValue(t *testing.T) {
	tests := []struct {
		name    string
		kind    configKind
		input   any
		want    any
		wantErr bool
	}{
		{name: "boolean", kind: kindBoolean, input: true, want: true},
		{name: "integer", kind: kindInteger, input: float64(60), want: int64(60)},
		{name: "negative integer", kind: kindInteger, input: float64(-1), wantErr: true},
		{name: "trimmed string", kind: kindString, input: "  value  ", want: "value"},
		{name: "deduplicated list", kind: kindStringList, input: []any{"one", " one ", "", "two"}, want: []string{"one", "two"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := validateConfigValue(test.kind, test.input)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateConfigValue() error = %v, wantErr %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("validateConfigValue() = %#v, want %#v", got, test.want)
			}
		})
	}
}
