package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestGetAccountBanAlert(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			AccountBanAlert: config.AccountBanAlertConfig{
				Enabled:                true,
				WebhookURL:             "https://open.larksuite.com/open-apis/bot/v2/hook/test-hook",
				ScanIntervalSeconds:    300,
				ProbeTimeoutSeconds:    15,
				Parallelism:            10,
				Confirm401Attempts:     2,
				Confirm401DelaySeconds: 3,
				DeleteBannedAuth:       false,
			},
		},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/account-ban-alert", nil)

	h.GetAccountBanAlert(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := body["enabled"]; got != true {
		t.Fatalf("enabled = %v, want true", got)
	}
	if got := body["webhook-url-preview"]; got == "" {
		t.Fatalf("webhook-url-preview empty, want masked preview")
	}
}

func TestPutAccountBanAlertNormalizesDefaults(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	tmpDir := t.TempDir()
	cfgPath := tmpDir + "/config.yaml"
	if err := writeTestConfigFile(cfgPath); err != nil {
		t.Fatalf("write config: %v", err)
	}

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: cfgPath,
	}

	reqBody := `{
		"enabled": true,
		"webhook-url": " https://open.larksuite.com/open-apis/bot/v2/hook/test-hook ",
		"scan-interval-seconds": 0,
		"probe-timeout-seconds": 0,
		"parallelism": 0,
		"confirm-401-attempts": 0,
		"confirm-401-delay-seconds": -1,
		"delete-banned-auth": true
	}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/account-ban-alert", strings.NewReader(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutAccountBanAlert(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	got := h.cfg.AccountBanAlert
	if !got.Enabled {
		t.Fatalf("enabled = false, want true")
	}
	if got.WebhookURL != "https://open.larksuite.com/open-apis/bot/v2/hook/test-hook" {
		t.Fatalf("webhook-url = %q", got.WebhookURL)
	}
	if got.ScanIntervalSeconds != 300 {
		t.Fatalf("scan interval = %d, want 300", got.ScanIntervalSeconds)
	}
	if got.ProbeTimeoutSeconds != 15 {
		t.Fatalf("probe timeout = %d, want 15", got.ProbeTimeoutSeconds)
	}
	if got.Parallelism != 10 {
		t.Fatalf("parallelism = %d, want 10", got.Parallelism)
	}
	if got.Confirm401Attempts != 2 {
		t.Fatalf("confirm attempts = %d, want 2", got.Confirm401Attempts)
	}
	if got.Confirm401DelaySeconds != 3 {
		t.Fatalf("confirm delay = %d, want 3", got.Confirm401DelaySeconds)
	}
	if !got.DeleteBannedAuth {
		t.Fatalf("delete-banned-auth = false, want true")
	}
}

func TestPostAccountBanAlertTest(t *testing.T) {
	t.Parallel()

	var webhookCalls int32
	var titleSeen int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&webhookCalls, 1)

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode webhook body: %v", err)
		}
		card, _ := body["card"].(map[string]any)
		header, _ := card["header"].(map[string]any)
		title, _ := header["title"].(map[string]any)
		if strings.TrimSpace(title["content"].(string)) == "自定义测试标题" {
			atomic.StoreInt32(&titleSeen, 1)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer webhook.Close()

	h := &Handler{
		cfg: &config.Config{
			AccountBanAlert: config.AccountBanAlertConfig{
				Enabled:                true,
				WebhookURL:             webhook.URL,
				ScanIntervalSeconds:    300,
				ProbeTimeoutSeconds:    15,
				Parallelism:            10,
				Confirm401Attempts:     2,
				Confirm401DelaySeconds: 3,
				DeleteBannedAuth:       false,
			},
		},
	}

	reqBody := `{"title":"自定义测试标题"}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/account-ban-alert/test", strings.NewReader(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PostAccountBanAlertTest(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := atomic.LoadInt32(&webhookCalls); got != 1 {
		t.Fatalf("webhook calls = %d, want 1", got)
	}
	if atomic.LoadInt32(&titleSeen) != 1 {
		t.Fatalf("custom title not observed")
	}
}

func TestPostAccountBanAlertTestRequiresWebhook(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			AccountBanAlert: config.AccountBanAlertConfig{},
		},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/account-ban-alert/test", nil)

	h.PostAccountBanAlertTest(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func writeTestConfigFile(path string) error {
	content := []byte(`
host: "127.0.0.1"
port: 8317
remote-management:
  secret-key: "$2a$10$7EqJtq98hPqEX7fNZaFWoOHiI0H9pG5QhQJ5f2Q6sK/Y06Fd25W6."
auth-dir: "./auths"
`)
	return os.WriteFile(path, content, 0o644)
}
