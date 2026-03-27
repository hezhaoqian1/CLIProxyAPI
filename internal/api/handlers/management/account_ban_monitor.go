package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	accountBanProbeUserAgent  = "codex_cli_rs/0.101.0 (Mac OS 26.0.1; arm64) Apple_Terminal/464"
	accountBanDefaultProvider = "codex"
)

var accountBanProbeURL = "https://chatgpt.com/backend-api/wham/usage"

type accountBanMonitor struct {
	handler *Handler

	mu      sync.RWMutex
	cfg     config.AccountBanAlertConfig
	authMgr *coreauth.Manager
	active  map[string]accountBanEvent

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
	wakeCh    chan struct{}
}

type accountBanProbeResult struct {
	event      accountBanEvent
	statusCode int
	banned     bool
}

type accountBanEvent struct {
	Provider         string
	AuthID           string
	AuthIndex        string
	AuthName         string
	AuthPath         string
	Email            string
	AccountType      string
	Account          string
	AccountID        string
	StatusCode       int
	DetectedAt       time.Time
	ConfirmAttempts  int
	ErrorBody        string
	DeleteAfterAlert bool
}

func newAccountBanMonitor(handler *Handler) *accountBanMonitor {
	m := &accountBanMonitor{
		handler: handler,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
		wakeCh:  make(chan struct{}, 1),
		active:  make(map[string]accountBanEvent),
	}
	if handler != nil {
		m.authMgr = handler.authManager
	}
	return m
}

func (m *accountBanMonitor) Start() {
	if m == nil {
		return
	}
	m.startOnce.Do(func() {
		go m.run()
	})
}

func (m *accountBanMonitor) Stop(ctx context.Context) {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-m.doneCh:
	case <-ctx.Done():
	}
}

func (m *accountBanMonitor) UpdateConfig(cfg *config.Config) {
	if m == nil {
		return
	}
	next := config.AccountBanAlertConfig{}
	if cfg != nil {
		next = cfg.AccountBanAlert
	}
	m.mu.Lock()
	m.cfg = next
	m.mu.Unlock()
	m.wake()
}

func (m *accountBanMonitor) SetAuthManager(manager *coreauth.Manager) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.authMgr = manager
	m.mu.Unlock()
	m.wake()
}

func (m *accountBanMonitor) wake() {
	if m == nil {
		return
	}
	select {
	case m.wakeCh <- struct{}{}:
	default:
	}
}

func (m *accountBanMonitor) currentConfig() config.AccountBanAlertConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

func (m *accountBanMonitor) currentAuthManager() *coreauth.Manager {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.authMgr
}

func (m *accountBanMonitor) run() {
	defer close(m.doneCh)

	for {
		cfg := m.currentConfig()
		wait := time.Duration(cfg.ScanIntervalSeconds) * time.Second
		if wait <= 0 {
			wait = 5 * time.Minute
		}

		if cfg.Enabled && strings.TrimSpace(cfg.WebhookURL) != "" {
			m.scanOnce()
		} else {
			m.resetActive(nil)
		}

		timer := time.NewTimer(wait)
		select {
		case <-m.stopCh:
			timer.Stop()
			return
		case <-m.wakeCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func (m *accountBanMonitor) scanOnce() {
	cfg := m.currentConfig()
	if !cfg.Enabled {
		return
	}
	if strings.TrimSpace(cfg.WebhookURL) == "" {
		return
	}

	manager := m.currentAuthManager()
	if manager == nil {
		return
	}

	auths := manager.List()
	if len(auths) == 0 {
		m.resetActive(nil)
		return
	}

	parallelism := cfg.Parallelism
	if parallelism <= 0 {
		parallelism = 10
	}
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	resultsCh := make(chan accountBanProbeResult, len(auths))

	for _, auth := range auths {
		if !shouldProbeAccountBan(auth) {
			continue
		}

		wg.Add(1)
		go func(item *coreauth.Auth) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-m.stopCh:
				return
			}
			defer func() { <-sem }()

			result, err := m.probeAuth(item, cfg)
			if err != nil {
				log.WithError(err).Debugf("account ban probe failed for auth %s", strings.TrimSpace(item.ID))
				return
			}
			resultsCh <- result
		}(auth)
	}

	wg.Wait()
	close(resultsCh)

	currentBanned := make(map[string]accountBanEvent)
	for result := range resultsCh {
		if !result.banned {
			continue
		}
		key := result.event.identityKey()
		currentBanned[key] = result.event
	}

	m.handleScanResults(currentBanned, cfg)
}

func shouldProbeAccountBan(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), accountBanDefaultProvider) {
		return false
	}
	auth.EnsureIndex()
	return strings.TrimSpace(auth.Index) != ""
}

func (m *accountBanMonitor) probeAuth(auth *coreauth.Auth, cfg config.AccountBanAlertConfig) (accountBanProbeResult, error) {
	if auth == nil {
		return accountBanProbeResult{}, fmt.Errorf("auth is nil")
	}

	attempts := cfg.Confirm401Attempts
	if attempts <= 0 {
		attempts = 2
	}
	delay := time.Duration(cfg.Confirm401DelaySeconds) * time.Second
	if delay < 0 {
		delay = 0
	}

	var last accountBanProbeResult
	for attempt := 1; attempt <= attempts; attempt++ {
		result, err := m.singleProbe(auth, cfg)
		if err != nil {
			return accountBanProbeResult{}, err
		}
		result.event.ConfirmAttempts = attempt
		last = result
		if result.statusCode != http.StatusUnauthorized {
			last.banned = false
			return last, nil
		}
		if attempt < attempts && delay > 0 {
			select {
			case <-m.stopCh:
				return accountBanProbeResult{}, context.Canceled
			case <-time.After(delay):
			}
		}
	}
	last.banned = last.statusCode == http.StatusUnauthorized
	return last, nil
}

func (m *accountBanMonitor) singleProbe(auth *coreauth.Auth, cfg config.AccountBanAlertConfig) (accountBanProbeResult, error) {
	timeout := cfg.ProbeTimeoutSeconds
	if timeout <= 0 {
		timeout = 15
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	auth = auth.Clone()
	auth.EnsureIndex()
	token, err := m.handler.resolveTokenForAuth(ctx, auth)
	if err != nil {
		return accountBanProbeResult{}, fmt.Errorf("resolve token: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return accountBanProbeResult{}, fmt.Errorf("empty token for auth %s", strings.TrimSpace(auth.ID))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, accountBanProbeURL, nil)
	if err != nil {
		return accountBanProbeResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", accountBanProbeUserAgent)
	if accountID := authAccountID(auth); accountID != "" {
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}

	client := &http.Client{
		Timeout:   time.Duration(timeout) * time.Second,
		Transport: m.handler.apiCallTransport(auth),
	}
	resp, err := client.Do(req)
	if err != nil {
		return accountBanProbeResult{}, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	event := buildAccountBanEvent(auth, resp.StatusCode, string(bytes.TrimSpace(bodyBytes)), cfg.DeleteBannedAuth)
	return accountBanProbeResult{
		event:      event,
		statusCode: resp.StatusCode,
		banned:     resp.StatusCode == http.StatusUnauthorized,
	}, nil
}

func buildAccountBanEvent(auth *coreauth.Auth, statusCode int, errBody string, deleteAfterAlert bool) accountBanEvent {
	event := accountBanEvent{
		StatusCode:       statusCode,
		DetectedAt:       time.Now(),
		DeleteAfterAlert: deleteAfterAlert,
		ErrorBody:        truncateForAlert(errBody, 400),
	}
	if auth == nil {
		return event
	}
	auth.EnsureIndex()
	event.Provider = strings.TrimSpace(auth.Provider)
	event.AuthID = strings.TrimSpace(auth.ID)
	event.AuthIndex = strings.TrimSpace(auth.Index)
	event.AuthName = strings.TrimSpace(auth.FileName)
	if event.AuthName == "" {
		event.AuthName = event.AuthID
	}
	event.AuthPath = strings.TrimSpace(authAttribute(auth, "path"))
	event.Email = authEmail(auth)
	event.AccountType, event.Account = auth.AccountInfo()
	event.AccountID = authAccountID(auth)
	return event
}

func (m *accountBanMonitor) handleScanResults(current map[string]accountBanEvent, cfg config.AccountBanAlertConfig) {
	m.mu.Lock()
	previous := make(map[string]accountBanEvent, len(m.active))
	for key, event := range m.active {
		previous[key] = event
	}
	m.active = current
	m.mu.Unlock()

	for key, event := range current {
		if _, seen := previous[key]; seen {
			continue
		}
		if err := m.sendBanAlert(cfg.WebhookURL, event); err != nil {
			log.WithError(err).Warnf("failed to send account ban alert for %s", key)
			m.clearActiveKey(key)
			continue
		}
		if event.DeleteAfterAlert {
			if err := m.deleteBannedAuth(event); err != nil {
				log.WithError(err).Warnf("failed to delete banned auth after alert: %s", event.AuthName)
			}
		}
	}
}

func (m *accountBanMonitor) resetActive(current map[string]accountBanEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = current
}

func (m *accountBanMonitor) clearActiveKey(key string) {
	if m == nil || strings.TrimSpace(key) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.active, key)
}

func (m *accountBanMonitor) sendBanAlert(webhookURL string, event accountBanEvent) error {
	payload := map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"header": map[string]any{
				"template": "red",
				"title": map[string]any{
					"content": "CPA 账号封号告警",
					"tag":     "plain_text",
				},
			},
			"elements": []map[string]any{
				{
					"tag": "div",
					"text": map[string]any{
						"tag":     "lark_md",
						"content": event.larkContent(),
					},
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimSpace(webhookURL), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("webhook status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result struct {
		Code       int    `json:"code"`
		Msg        string `json:"msg"`
		StatusCode int    `json:"StatusCode"`
	}
	_ = json.Unmarshal(respBody, &result)
	if result.Code != 0 && result.StatusCode != 0 {
		return fmt.Errorf("webhook rejected alert: %s", strings.TrimSpace(string(respBody)))
	}
	return nil
}

func (m *accountBanMonitor) deleteBannedAuth(event accountBanEvent) error {
	if m == nil || m.handler == nil {
		return fmt.Errorf("handler unavailable")
	}
	targetPath := strings.TrimSpace(event.AuthPath)
	if targetPath == "" && m.handler.cfg != nil {
		targetPath = filepath.Join(m.handler.cfg.AuthDir, filepath.Base(event.AuthName))
	}
	if targetPath == "" {
		return fmt.Errorf("missing auth path")
	}
	if !filepath.IsAbs(targetPath) {
		if abs, errAbs := filepath.Abs(targetPath); errAbs == nil {
			targetPath = abs
		}
	}

	if err := os.Remove(targetPath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}

	ctx := context.Background()
	if err := m.handler.deleteTokenRecord(ctx, targetPath); err != nil {
		return err
	}
	if strings.TrimSpace(event.AuthID) != "" {
		m.handler.disableAuth(ctx, event.AuthID)
	} else {
		m.handler.disableAuth(ctx, targetPath)
	}
	return nil
}

func (e accountBanEvent) identityKey() string {
	if e.AuthID != "" {
		return "auth-id:" + e.AuthID
	}
	if e.AuthIndex != "" {
		return "auth-index:" + e.AuthIndex
	}
	if e.AuthName != "" {
		return "auth-name:" + e.AuthName
	}
	if e.Email != "" {
		return "email:" + e.Email
	}
	return fmt.Sprintf("fallback:%s:%s", e.Provider, e.Account)
}

func (e accountBanEvent) larkContent() string {
	lines := []string{
		"**检测结果**: `wham/usage` 返回 `401`，判定为账号已封",
		fmt.Sprintf("**账号类型**: `%s`", firstNonEmptyValue(e.AccountType, "unknown")),
		fmt.Sprintf("**账号标识**: `%s`", firstNonEmptyValue(e.Account, "unknown")),
		fmt.Sprintf("**邮箱**: `%s`", firstNonEmptyValue(e.Email, "unknown")),
		fmt.Sprintf("**Account ID**: `%s`", firstNonEmptyValue(e.AccountID, "unknown")),
		fmt.Sprintf("**Auth 文件**: `%s`", firstNonEmptyValue(e.AuthName, "unknown")),
		fmt.Sprintf("**Auth Index**: `%s`", firstNonEmptyValue(e.AuthIndex, "unknown")),
		fmt.Sprintf("**Provider**: `%s`", firstNonEmptyValue(e.Provider, "unknown")),
		fmt.Sprintf("**HTTP 状态**: `%d`", e.StatusCode),
		fmt.Sprintf("**确认次数**: `%d`", e.ConfirmAttempts),
		fmt.Sprintf("**时间**: `%s`", e.DetectedAt.Format("2006-01-02 15:04:05")),
		fmt.Sprintf("**自动删号**: `%t`", e.DeleteAfterAlert),
	}
	if trimmed := strings.TrimSpace(e.ErrorBody); trimmed != "" {
		lines = append(lines, fmt.Sprintf("**响应摘要**: `%s`", escapeInlineCode(trimmed)))
	}
	return strings.Join(lines, "\n")
}

func authAccountID(auth *coreauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if value, ok := auth.Metadata["account_id"].(string); ok {
		return strings.TrimSpace(value)
	}
	if value, ok := auth.Metadata["chatgpt_account_id"].(string); ok {
		return strings.TrimSpace(value)
	}
	if claims := extractCodexIDTokenClaims(auth); claims != nil {
		if value, ok := claims["chatgpt_account_id"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonEmptyValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func truncateForAlert(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return strings.TrimSpace(value[:limit]) + "..."
}

func escapeInlineCode(value string) string {
	return strings.ReplaceAll(value, "`", "'")
}
