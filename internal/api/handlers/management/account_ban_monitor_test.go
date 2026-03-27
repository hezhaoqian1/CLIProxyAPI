package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestHandleScanResultsSendsOnlyNewBan(t *testing.T) {
	t.Parallel()

	var webhookCalls int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&webhookCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer webhook.Close()

	h := &Handler{}
	monitor := newAccountBanMonitor(h)
	cfg := config.AccountBanAlertConfig{
		Enabled:    true,
		WebhookURL: webhook.URL,
	}

	event := accountBanEvent{
		AuthID:          "auth-1",
		AuthIndex:       "idx-1",
		AuthName:        "codex-user.json",
		Email:           "user@example.com",
		AccountType:     "oauth",
		Account:         "user@example.com",
		AccountID:       "acct-1",
		StatusCode:      http.StatusUnauthorized,
		ConfirmAttempts: 2,
	}

	current := map[string]accountBanEvent{event.identityKey(): event}
	monitor.handleScanResults(current, cfg)
	monitor.handleScanResults(current, cfg)

	if got := atomic.LoadInt32(&webhookCalls); got != 1 {
		t.Fatalf("webhook calls = %d, want 1", got)
	}
}

func TestProbeAuthTreatsOnly401AsBan(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantBanned bool
	}{
		{name: "unauthorized", statusCode: http.StatusUnauthorized, wantBanned: true},
		{name: "ok", statusCode: http.StatusOK, wantBanned: false},
		{name: "quota", statusCode: http.StatusTooManyRequests, wantBanned: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/backend-api/wham/usage" {
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer upstream.Close()

			prevURL := accountBanProbeURL
			accountBanProbeURL = upstream.URL + "/backend-api/wham/usage"
			defer func() { accountBanProbeURL = prevURL }()

			h := &Handler{cfg: &config.Config{}}
			monitor := newAccountBanMonitor(h)

			auth := &coreauth.Auth{
				ID:       "auth-1",
				FileName: "codex-user.json",
				Provider: "codex",
				Metadata: map[string]any{
					"access_token": "token-1",
					"email":        "user@example.com",
					"account_id":   "acct-1",
				},
			}

			result, err := monitor.probeAuth(auth, config.AccountBanAlertConfig{
				Enabled:                true,
				WebhookURL:             "http://example.invalid",
				ProbeTimeoutSeconds:    5,
				Confirm401Attempts:     1,
				Confirm401DelaySeconds: 0,
			})
			if err != nil {
				t.Fatalf("probeAuth returned error: %v", err)
			}
			if result.banned != tt.wantBanned {
				t.Fatalf("probeAuth banned = %t, want %t", result.banned, tt.wantBanned)
			}
		})
	}
}

func TestHandleScanResultsDeletesAfterAlertWhenEnabled(t *testing.T) {
	t.Parallel()

	var webhookCalls int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&webhookCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer webhook.Close()

	authDir := t.TempDir()
	fileName := "codex-user@example.com.json"
	filePath := filepath.Join(authDir, fileName)
	if err := os.WriteFile(filePath, []byte(`{"type":"codex","email":"user@example.com"}`), 0o600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       "auth-1",
		FileName: fileName,
		Provider: "codex",
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"email":      "user@example.com",
			"account_id": "acct-1",
		},
	}
	if _, err := manager.Register(context.Background(), record); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	store := &memoryAuthStore{}
	h := &Handler{
		cfg:         &config.Config{AuthDir: authDir},
		authManager: manager,
		tokenStore:  store,
	}
	monitor := newAccountBanMonitor(h)

	event := accountBanEvent{
		AuthID:           record.ID,
		AuthIndex:        record.EnsureIndex(),
		AuthName:         fileName,
		AuthPath:         filePath,
		Email:            "user@example.com",
		AccountType:      "oauth",
		Account:          "user@example.com",
		AccountID:        "acct-1",
		StatusCode:       http.StatusUnauthorized,
		ConfirmAttempts:  2,
		DeleteAfterAlert: true,
	}

	monitor.handleScanResults(map[string]accountBanEvent{event.identityKey(): event}, config.AccountBanAlertConfig{
		Enabled:          true,
		WebhookURL:       webhook.URL,
		DeleteBannedAuth: true,
	})

	if got := atomic.LoadInt32(&webhookCalls); got != 1 {
		t.Fatalf("webhook calls = %d, want 1", got)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("expected auth file deleted, stat err: %v", err)
	}
	if _, ok := manager.GetByID(record.ID); !ok {
		t.Fatalf("expected auth to remain in manager in disabled state")
	}
	updated, _ := manager.GetByID(record.ID)
	if updated == nil || !updated.Disabled {
		t.Fatalf("expected updated auth to be disabled, got %+v", updated)
	}
	if got := strings.TrimSpace(updated.StatusMessage); got != "removed via management API" {
		t.Fatalf("status message = %q, want removed via management API", got)
	}
}
