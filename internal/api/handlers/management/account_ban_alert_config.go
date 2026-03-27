package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

type accountBanAlertResponse struct {
	Enabled                bool   `json:"enabled"`
	WebhookURLPreview      string `json:"webhook-url-preview"`
	ScanIntervalSeconds    int    `json:"scan-interval-seconds"`
	ProbeTimeoutSeconds    int    `json:"probe-timeout-seconds"`
	Parallelism            int    `json:"parallelism"`
	Confirm401Attempts     int    `json:"confirm-401-attempts"`
	Confirm401DelaySeconds int    `json:"confirm-401-delay-seconds"`
	DeleteBannedAuth       bool   `json:"delete-banned-auth"`
}

type accountBanAlertUpdateRequest struct {
	Enabled                *bool   `json:"enabled"`
	WebhookURL             *string `json:"webhook-url"`
	ScanIntervalSeconds    *int    `json:"scan-interval-seconds"`
	ProbeTimeoutSeconds    *int    `json:"probe-timeout-seconds"`
	Parallelism            *int    `json:"parallelism"`
	Confirm401Attempts     *int    `json:"confirm-401-attempts"`
	Confirm401DelaySeconds *int    `json:"confirm-401-delay-seconds"`
	DeleteBannedAuth       *bool   `json:"delete-banned-auth"`
}

type accountBanAlertTestRequest struct {
	Title   *string `json:"title"`
	Content *string `json:"content"`
}

func (h *Handler) GetAccountBanAlert(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusOK, accountBanAlertResponse{
			Enabled:                true,
			ScanIntervalSeconds:    300,
			ProbeTimeoutSeconds:    15,
			Parallelism:            10,
			Confirm401Attempts:     2,
			Confirm401DelaySeconds: 3,
			DeleteBannedAuth:       false,
		})
		return
	}

	cfg := h.cfg.AccountBanAlert
	c.JSON(http.StatusOK, accountBanAlertResponse{
		Enabled:                cfg.Enabled,
		WebhookURLPreview:      previewWebhookURL(cfg.WebhookURL),
		ScanIntervalSeconds:    cfg.ScanIntervalSeconds,
		ProbeTimeoutSeconds:    cfg.ProbeTimeoutSeconds,
		Parallelism:            cfg.Parallelism,
		Confirm401Attempts:     cfg.Confirm401Attempts,
		Confirm401DelaySeconds: cfg.Confirm401DelaySeconds,
		DeleteBannedAuth:       cfg.DeleteBannedAuth,
	})
}

func (h *Handler) PutAccountBanAlert(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config unavailable"})
		return
	}

	var body accountBanAlertUpdateRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	next := h.cfg.AccountBanAlert
	if body.Enabled != nil {
		next.Enabled = *body.Enabled
	}
	if body.WebhookURL != nil {
		webhook := strings.TrimSpace(*body.WebhookURL)
		if webhook != "" {
			next.WebhookURL = webhook
		}
	}
	if body.ScanIntervalSeconds != nil {
		next.ScanIntervalSeconds = *body.ScanIntervalSeconds
	}
	if body.ProbeTimeoutSeconds != nil {
		next.ProbeTimeoutSeconds = *body.ProbeTimeoutSeconds
	}
	if body.Parallelism != nil {
		next.Parallelism = *body.Parallelism
	}
	if body.Confirm401Attempts != nil {
		next.Confirm401Attempts = *body.Confirm401Attempts
	}
	if body.Confirm401DelaySeconds != nil {
		next.Confirm401DelaySeconds = *body.Confirm401DelaySeconds
	}
	if body.DeleteBannedAuth != nil {
		next.DeleteBannedAuth = *body.DeleteBannedAuth
	}

	normalizeAccountBanAlertConfig(&next)
	h.cfg.AccountBanAlert = next
	h.persist(c)
}

func (h *Handler) PostAccountBanAlertTest(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config unavailable"})
		return
	}

	var body accountBanAlertTestRequest
	if err := c.ShouldBindJSON(&body); err != nil && err.Error() != "EOF" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	cfg := h.cfg.AccountBanAlert
	webhookURL := strings.TrimSpace(cfg.WebhookURL)
	if webhookURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "webhook-url is empty"})
		return
	}

	now := time.Now()
	title := "CPA 封号告警测试"
	if body.Title != nil && strings.TrimSpace(*body.Title) != "" {
		title = strings.TrimSpace(*body.Title)
	}
	content := strings.Join([]string{
		"**检测结果**: `测试消息`",
		"**账号类型**: `oauth`",
		"**账号标识**: `test-account@example.com`",
		"**邮箱**: `test-account@example.com`",
		"**Account ID**: `test-account-id`",
		"**Auth 文件**: `test-auth.json`",
		"**Auth Index**: `test-auth-index`",
		"**Provider**: `codex`",
		"**HTTP 状态**: `401`",
		"**确认次数**: `2`",
		"**时间**: `" + now.Format("2006-01-02 15:04:05") + "`",
		"**自动删号**: `" + boolString(cfg.DeleteBannedAuth) + "`",
	}, "\n")
	if body.Content != nil && strings.TrimSpace(*body.Content) != "" {
		content = strings.TrimSpace(*body.Content)
	}

	err := sendLarkInteractiveCard(webhookURL, title, content, "red")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func normalizeAccountBanAlertConfig(cfg *config.AccountBanAlertConfig) {
	if cfg == nil {
		return
	}
	cfg.WebhookURL = strings.TrimSpace(cfg.WebhookURL)
	if cfg.ScanIntervalSeconds <= 0 {
		cfg.ScanIntervalSeconds = 300
	}
	if cfg.ProbeTimeoutSeconds <= 0 {
		cfg.ProbeTimeoutSeconds = 15
	}
	if cfg.Parallelism <= 0 {
		cfg.Parallelism = 10
	}
	if cfg.Confirm401Attempts <= 0 {
		cfg.Confirm401Attempts = 2
	}
	if cfg.Confirm401DelaySeconds < 0 {
		cfg.Confirm401DelaySeconds = 3
	}
}

func previewWebhookURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if len(value) <= 24 {
		return value
	}
	return value[:24] + "..."
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
