package pocket48

import (
	"errors"
	"testing"

	"pocket48-bot/internal/config"
)

func TestIsAuthorizationExpired(t *testing.T) {
	if !IsAuthorizationExpired(&APIError{Status: 401003, Message: "expired"}) {
		t.Fatal("expected 401003 to be recognized as expired authorization")
	}
	if IsAuthorizationExpired(&APIError{Status: 500, Message: "failed"}) {
		t.Fatal("did not expect other API errors to be recognized as expired authorization")
	}
	if IsAuthorizationExpired(errors.New("API error: expired (status: 401003)")) {
		t.Fatal("did not expect unstructured errors to be recognized as expired authorization")
	}
}

func TestAuthHeadersCanExcludeExpiredToken(t *testing.T) {
	client := NewClient(&config.Config{PocketToken: "expired-token"})
	if got := client.getHeaders(true)["token"]; got != "expired-token" {
		t.Fatalf("expected token header, got %q", got)
	}
	if _, ok := client.getHeaders(false)["token"]; ok {
		t.Fatal("did not expect token header on authentication requests")
	}
}
