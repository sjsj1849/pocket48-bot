package outbound

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGatewayClientSendsNeutralMessageWithLegacyTarget(t *testing.T) {
	received := make(chan gatewayDelivery, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		var delivery gatewayDelivery
		if err := json.NewDecoder(r.Body).Decode(&delivery); err != nil {
			t.Error(err)
		}
		received <- delivery
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client := NewGatewayClient(server.URL, "secret")
	client.Send(Group(123), Text("hello"))
	select {
	case delivery := <-received:
		if delivery.Project != "pocket48" || delivery.LegacyTarget.Platform != "qq" || delivery.LegacyTarget.Address != "123" {
			t.Fatalf("unexpected delivery: %#v", delivery)
		}
		if len(delivery.Segments) != 1 || delivery.Segments[0].Data["text"] != "hello" {
			t.Fatalf("unexpected segments: %#v", delivery.Segments)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gateway delivery was not received")
	}
}
