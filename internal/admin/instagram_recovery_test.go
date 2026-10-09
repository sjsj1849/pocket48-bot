package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstagramRecoveryTokenAndCredentialLifecycle(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "instagram", "recovery-device-token")
	manager := newInstagramRecoveryManager(path)

	if manager.authorize("wrong") {
		t.Fatal("wrong device token was accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	token := string(data[:len(data)-1])
	if !manager.authorize(token) {
		t.Fatal("generated device token was rejected")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("device token mode = %v, err = %v", info.Mode().Perm(), err)
	}

	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if err := manager.start("reader", "secret-password", now); err != nil {
		t.Fatal(err)
	}
	payload := manager.devicePayload(now.Add(time.Second))
	if payload["username"] != "reader" || payload["password"] != "secret-password" {
		t.Fatalf("unexpected device payload: %#v", payload)
	}
	if err := manager.setCode("123456", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	payload = manager.devicePayload(now.Add(3 * time.Second))
	if payload["code"] != "123456" {
		t.Fatalf("verification code missing: %#v", payload)
	}
	manager.complete(now.Add(4 * time.Second))
	if manager.job == nil || len(manager.job.Password) != 0 || len(manager.job.Code) != 0 {
		t.Fatal("credentials were retained after successful recovery")
	}
	if state := manager.panelState(now.Add(5 * time.Second)); state["status"] != "complete" {
		t.Fatalf("unexpected final state: %#v", state)
	}
}

func TestInstagramRecoveryExpiresAndClearsSecrets(t *testing.T) {
	manager := newInstagramRecoveryManager(filepath.Join(t.TempDir(), "token"))
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if err := manager.start("reader", "secret-password", now); err != nil {
		t.Fatal(err)
	}
	state := manager.panelState(now.Add(instagramRecoveryTTL + time.Second))
	if state["status"] != "expired" {
		t.Fatalf("unexpected state: %#v", state)
	}
	if len(manager.job.Password) != 0 || len(manager.job.Code) != 0 {
		t.Fatal("expired credentials were not cleared")
	}
	if payload := manager.devicePayload(now.Add(instagramRecoveryTTL + 2*time.Second)); payload["state"] != "idle" {
		t.Fatalf("expired job leaked to device: %#v", payload)
	}
}

func TestInstagramRecoveryPanelStateDoesNotExposeSecrets(t *testing.T) {
	manager := newInstagramRecoveryManager(filepath.Join(t.TempDir(), "token"))
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if err := manager.start("reader", "secret-password", now); err != nil {
		t.Fatal(err)
	}
	if err := manager.setCode("123456", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manager.panelState(now.Add(2 * time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "secret-password") || strings.Contains(text, "123456") || strings.Contains(text, "reader") {
		t.Fatalf("panel state exposed recovery credentials: %s", text)
	}
}

func TestInstagramDeviceRecoveryRequiresTokenAndValidState(t *testing.T) {
	manager := newInstagramRecoveryManager(filepath.Join(t.TempDir(), "token"))
	if err := manager.ensureToken(); err != nil {
		t.Fatal(err)
	}
	server := &Server{instagramRecovery: manager}

	unauthorized := httptest.NewRecorder()
	server.handleInstagramDeviceRecovery(unauthorized, httptest.NewRequest(http.MethodGet, "/api/instagram/device-recovery", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	tokenData, err := os.ReadFile(manager.tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(tokenData))
	request := httptest.NewRequest(http.MethodPost, "/api/instagram/device-recovery", strings.NewReader(`{"state":"made_up","message":"no"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Instagram-Recovery-Token", token)
	invalid := httptest.NewRecorder()
	server.handleInstagramDeviceRecovery(invalid, request)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid state status = %d, body = %s", invalid.Code, invalid.Body.String())
	}
}
