package logic

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestWriteMIMETextPartEncodesUTF8HTML(t *testing.T) {
	body := `<table><tr><td>八小妹 &amp; 哈two哈</td></tr></table>`
	var buf bytes.Buffer
	writeMIMETextPart(&buf, "test-boundary", "text/html", body)
	raw := buf.String()
	if !strings.Contains(raw, "Content-Transfer-Encoding: base64\r\n") {
		t.Fatalf("body is not base64 encoded:\n%s", raw)
	}
	parts := strings.SplitN(raw, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("missing MIME header/body separator: %q", raw)
	}
	encoded := strings.ReplaceAll(strings.TrimSpace(parts[1]), "\r\n", "")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode MIME body: %v", err)
	}
	if string(decoded) != body {
		t.Fatalf("decoded body mismatch: got %q want %q", decoded, body)
	}
	for _, line := range strings.Split(strings.TrimSpace(parts[1]), "\r\n") {
		if len(line) > 76 {
			t.Fatalf("base64 line has %d chars, want <= 76", len(line))
		}
	}
}
