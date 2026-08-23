package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	quotaRefreshInterval     = 5 * time.Minute
	quotaRefreshInitialDelay = 10 * time.Second
	quotaRefreshConcurrency  = 5
	codexAccessTokenSkew     = time.Minute
)

var (
	antigravityQuotaURLs = []string{
		"https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
		"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels",
		"https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
	}
	claudeProfileURL       = "https://api.anthropic.com/api/oauth/profile"
	claudeUsageURL         = "https://api.anthropic.com/api/oauth/usage"
	codexQuotaURL          = "https://chatgpt.com/backend-api/wham/usage"
	geminiCliCodeAssistURL = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	geminiCliQuotaURL      = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"
	kimiUsageURL           = "https://api.kimi.com/coding/v1/usages"
)

const defaultAntigravityProjectID = "bamboo-precept-lgxtn"

type quotaRefreshRequest struct {
	Names []string `json:"names"`
	All   bool     `json:"all"`
}

type quotaSnapshot struct {
	Status                  string `json:"status"`
	StatusCode              int    `json:"status_code,omitempty"`
	Body                    string `json:"body,omitempty"`
	Error                   string `json:"error,omitempty"`
	UpdatedAt               string `json:"updated_at"`
	PlanType                string `json:"plan_type,omitempty"`
	SubscriptionActiveStart any    `json:"subscription_active_start,omitempty"`
	SubscriptionActiveUntil any    `json:"subscription_active_until,omitempty"`
	ProfileStatusCode       int    `json:"profile_status_code,omitempty"`
	ProfileBody             string `json:"profile_body,omitempty"`
	SupplementaryStatusCode int    `json:"supplementary_status_code,omitempty"`
	SupplementaryBody       string `json:"supplementary_body,omitempty"`
}

type quotaRefreshStatus struct {
	LastCompletedAt string `json:"last_completed_at,omitempty"`
	NextRunAt       string `json:"next_run_at,omitempty"`
	Success         int    `json:"success"`
	Failed          int    `json:"failed"`
}

func (h *Handler) StartQuotaAutoRefresh() {
	if h == nil {
		return
	}
	h.codexQuotaAutoMu.Lock()
	if h.codexQuotaAutoCancel != nil {
		h.codexQuotaAutoMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.codexQuotaAutoCancel = cancel
	h.codexQuotaAutoMu.Unlock()

	go func() {
		timer := time.NewTimer(quotaRefreshInitialDelay)
		defer timer.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				h.refreshAllQuotaInBackground(ctx)
				timer.Reset(quotaRefreshInterval)
			}
		}
	}()
}

func (h *Handler) StartCodexQuotaAutoRefresh() {
	h.StartQuotaAutoRefresh()
}

func (h *Handler) StopQuotaAutoRefresh() {
	if h == nil {
		return
	}
	h.codexQuotaAutoMu.Lock()
	cancel := h.codexQuotaAutoCancel
	h.codexQuotaAutoCancel = nil
	h.codexQuotaAutoMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (h *Handler) StopCodexQuotaAutoRefresh() {
	h.StopQuotaAutoRefresh()
}

func (h *Handler) refreshAllQuotaInBackground(ctx context.Context) {
	if h == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return
	default:
	}
	h.codexQuotaAutoMu.Lock()
	if h.codexQuotaAutoBusy {
		h.codexQuotaAutoMu.Unlock()
		return
	}
	h.codexQuotaAutoBusy = true
	h.codexQuotaAutoMu.Unlock()

	defer func() {
		h.codexQuotaAutoMu.Lock()
		h.codexQuotaAutoBusy = false
		h.codexQuotaAutoMu.Unlock()
	}()

	auths := h.quotaTargets(nil, true)
	results := map[string]quotaSnapshot{}
	if len(auths) > 0 {
		log.Debugf("management quota auto refresh targets: %d", len(auths))
		results = h.refreshQuotaBatch(ctx, auths)
	}
	if ctx.Err() != nil {
		return
	}
	h.updateQuotaRefreshStatus(time.Now(), results)
}

func (h *Handler) RefreshQuota(c *gin.Context) {
	var body quotaRefreshRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil && errBind != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	auths := h.quotaTargets(body.Names, body.All || len(body.Names) == 0)
	results := h.refreshQuotaBatch(c.Request.Context(), auths)
	c.JSON(http.StatusOK, gin.H{"results": results})
}

func (h *Handler) RefreshCodexQuota(c *gin.Context) {
	h.RefreshQuota(c)
}

func (h *Handler) quotaRefreshStatusSnapshot() quotaRefreshStatus {
	if h == nil {
		return quotaRefreshStatus{}
	}
	h.codexQuotaAutoMu.Lock()
	defer h.codexQuotaAutoMu.Unlock()
	return h.quotaRefreshStatus
}

func (h *Handler) updateQuotaRefreshStatus(completedAt time.Time, results map[string]quotaSnapshot) {
	if h == nil {
		return
	}
	success, failed := quotaRefreshResultCounts(results)
	completedAt = completedAt.UTC()
	h.codexQuotaAutoMu.Lock()
	h.quotaRefreshStatus = quotaRefreshStatus{
		LastCompletedAt: completedAt.Format(time.RFC3339),
		NextRunAt:       completedAt.Add(quotaRefreshInterval).Format(time.RFC3339),
		Success:         success,
		Failed:          failed,
	}
	h.codexQuotaAutoMu.Unlock()
}

func quotaRefreshResultCounts(results map[string]quotaSnapshot) (int, int) {
	success := 0
	failed := 0
	for _, snapshot := range results {
		switch snapshot.Status {
		case "success":
			success++
		case "error":
			failed++
		}
	}
	return success, failed
}

func (h *Handler) quotaTargets(names []string, all bool) []*coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}

	nameSet := make(map[string]struct{}, len(names))
	for _, name := range names {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			nameSet[trimmed] = struct{}{}
		}
	}

	auths := h.authManager.List()
	out := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if auth == nil || auth.Disabled || !quotaProviderSupported(auth.Provider) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			if accountType, _ := auth.AccountInfo(); strings.EqualFold(accountType, "api_key") {
				continue
			}
		}
		if strings.TrimSpace(auth.FileName) == "" && strings.TrimSpace(auth.ID) == "" {
			continue
		}
		if !all {
			if _, ok := nameSet[auth.FileName]; !ok {
				if _, okID := nameSet[auth.ID]; !okID {
					continue
				}
			}
		}
		out = append(out, auth)
	}
	return out
}

func (h *Handler) codexQuotaTargets(names []string, all bool) []*coreauth.Auth {
	allTargets := h.quotaTargets(names, all)
	out := make([]*coreauth.Auth, 0, len(allTargets))
	for _, auth := range allTargets {
		if auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			out = append(out, auth)
		}
	}
	return out
}

func quotaProviderSupported(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "antigravity", "claude", "codex", "gemini-cli", "kimi":
		return true
	default:
		return false
	}
}

func (h *Handler) refreshQuotaBatch(ctx context.Context, auths []*coreauth.Auth) map[string]quotaSnapshot {
	results := make(map[string]quotaSnapshot, len(auths))
	if len(auths) == 0 {
		return results
	}

	var mu sync.Mutex
	jobs := make(chan *coreauth.Auth)
	workers := quotaRefreshConcurrency
	if workers > len(auths) {
		workers = len(auths)
	}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for auth := range jobs {
				name := codexQuotaResultName(auth)
				snapshot := h.refreshQuotaForAuth(ctx, auth)
				mu.Lock()
				results[name] = snapshot
				mu.Unlock()
			}
		}()
	}

	for _, auth := range auths {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return results
		case jobs <- auth:
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

func (h *Handler) refreshCodexQuotaBatch(ctx context.Context, auths []*coreauth.Auth) map[string]quotaSnapshot {
	return h.refreshQuotaBatch(ctx, auths)
}

func (h *Handler) refreshQuotaForAuth(ctx context.Context, auth *coreauth.Auth) quotaSnapshot {
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "antigravity":
		return h.refreshAntigravityQuotaForAuth(ctx, auth)
	case "claude":
		return h.refreshClaudeQuotaForAuth(ctx, auth)
	case "codex":
		return h.refreshCodexQuotaForAuth(ctx, auth)
	case "gemini-cli":
		return h.refreshGeminiCliQuotaForAuth(ctx, auth)
	case "kimi":
		return h.refreshKimiQuotaForAuth(ctx, auth)
	default:
		now := time.Now().UTC()
		return quotaSnapshot{
			Status:    "error",
			Error:     "quota provider unsupported",
			UpdatedAt: now.Format(time.RFC3339),
		}
	}
}

func (h *Handler) refreshCodexQuotaForAuth(ctx context.Context, auth *coreauth.Auth) quotaSnapshot {
	now := time.Now().UTC()
	snapshot := h.codexQuotaSnapshotFromAuth(auth, now)

	refreshedAuth, token, errToken := h.codexAccessToken(ctx, auth, false)
	if errToken != nil {
		snapshot.Status = "error"
		snapshot.Error = errToken.Error()
		if errSave := h.saveQuotaSnapshot(ctx, refreshedAuth, snapshot, now); errSave != nil {
			snapshot.Error = fmt.Sprintf("%s; failed to persist codex quota snapshot: %v", snapshot.Error, errSave)
		}
		return snapshot
	}
	if refreshedAuth != nil {
		auth = refreshedAuth
		snapshot = h.codexQuotaSnapshotFromAuth(auth, now)
	}

	statusCode, body, errFetch := h.fetchCodexQuota(ctx, auth, token)
	if statusCode == http.StatusUnauthorized {
		refreshedAuth, token, errToken = h.codexAccessToken(ctx, auth, true)
		if errToken == nil {
			if refreshedAuth != nil {
				auth = refreshedAuth
				snapshot = h.codexQuotaSnapshotFromAuth(auth, now)
			}
			statusCode, body, errFetch = h.fetchCodexQuota(ctx, auth, token)
		} else {
			errFetch = errToken
		}
	}

	snapshot.StatusCode = statusCode
	if errFetch != nil {
		snapshot.Status = "error"
		snapshot.Error = errFetch.Error()
		if errSave := h.saveQuotaSnapshot(ctx, auth, snapshot, now); errSave != nil {
			snapshot.Error = fmt.Sprintf("%s; failed to persist codex quota snapshot: %v", snapshot.Error, errSave)
		}
		return snapshot
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		snapshot.Status = "error"
		snapshot.Error = strings.TrimSpace(body)
		if snapshot.Error == "" {
			snapshot.Error = fmt.Sprintf("codex quota request failed with status %d", statusCode)
		}
		if errSave := h.saveQuotaSnapshot(ctx, auth, snapshot, now); errSave != nil {
			snapshot.Error = fmt.Sprintf("%s; failed to persist codex quota snapshot: %v", snapshot.Error, errSave)
		}
		return snapshot
	}

	snapshot.Status = "success"
	snapshot.Body = body
	snapshot.Error = ""
	snapshot.PlanType = codexPlanTypeFromQuotaBody(body, snapshot.PlanType)
	if errSave := h.saveQuotaSnapshot(ctx, auth, snapshot, now); errSave != nil {
		snapshot.Status = "error"
		snapshot.Error = fmt.Sprintf("failed to persist codex quota snapshot: %v", errSave)
	}
	return snapshot
}

func (h *Handler) refreshAntigravityQuotaForAuth(ctx context.Context, auth *coreauth.Auth) quotaSnapshot {
	now := time.Now().UTC()
	snapshot := quotaSnapshot{Status: "error", UpdatedAt: now.Format(time.RFC3339)}

	token, errToken := h.resolveTokenForAuth(ctx, auth, "")
	if errToken != nil {
		snapshot.Error = errToken.Error()
		_ = h.saveQuotaSnapshot(ctx, auth, snapshot, now)
		return snapshot
	}
	if strings.TrimSpace(token) == "" {
		snapshot.Error = "antigravity access token missing"
		_ = h.saveQuotaSnapshot(ctx, auth, snapshot, now)
		return snapshot
	}

	requestBody := fmt.Sprintf(`{"project":%q}`, antigravityProjectIDFromAuth(auth))
	var lastStatus int
	var lastBody string
	var lastErr error
	for _, url := range antigravityQuotaURLs {
		lastStatus, lastBody, lastErr = h.fetchBearerQuota(ctx, auth, http.MethodPost, url, token, map[string]string{
			"Content-Type": "application/json",
			"User-Agent":   "antigravity/1.11.5 windows/amd64",
		}, requestBody)
		if lastErr == nil && lastStatus >= http.StatusOK && lastStatus < http.StatusMultipleChoices {
			snapshot.Status = "success"
			snapshot.StatusCode = lastStatus
			snapshot.Body = lastBody
			snapshot.Error = ""
			if errSave := h.saveQuotaSnapshot(ctx, auth, snapshot, now); errSave != nil {
				snapshot.Status = "error"
				snapshot.Error = fmt.Sprintf("failed to persist quota snapshot: %v", errSave)
			}
			return snapshot
		}
	}

	snapshot.StatusCode = lastStatus
	if lastErr != nil {
		snapshot.Error = lastErr.Error()
	} else {
		snapshot.Error = strings.TrimSpace(lastBody)
	}
	if snapshot.Error == "" {
		snapshot.Error = fmt.Sprintf("antigravity quota request failed with status %d", lastStatus)
	}
	_ = h.saveQuotaSnapshot(ctx, auth, snapshot, now)
	return snapshot
}

func (h *Handler) refreshClaudeQuotaForAuth(ctx context.Context, auth *coreauth.Auth) quotaSnapshot {
	now := time.Now().UTC()
	snapshot := quotaSnapshot{Status: "error", UpdatedAt: now.Format(time.RFC3339)}

	token, errToken := h.resolveTokenForAuth(ctx, auth, "")
	if errToken != nil {
		snapshot.Error = errToken.Error()
		_ = h.saveQuotaSnapshot(ctx, auth, snapshot, now)
		return snapshot
	}
	usageStatus, usageBody, errUsage := h.fetchBearerQuota(ctx, auth, http.MethodGet, claudeUsageURL, token, map[string]string{
		"Content-Type":   "application/json",
		"anthropic-beta": "oauth-2025-04-20",
	}, "")
	snapshot.StatusCode = usageStatus
	if errUsage != nil {
		snapshot.Error = errUsage.Error()
		_ = h.saveQuotaSnapshot(ctx, auth, snapshot, now)
		return snapshot
	}
	if usageStatus < http.StatusOK || usageStatus >= http.StatusMultipleChoices {
		snapshot.Error = strings.TrimSpace(usageBody)
		if snapshot.Error == "" {
			snapshot.Error = fmt.Sprintf("claude quota request failed with status %d", usageStatus)
		}
		_ = h.saveQuotaSnapshot(ctx, auth, snapshot, now)
		return snapshot
	}

	profileStatus, profileBody, _ := h.fetchBearerQuota(ctx, auth, http.MethodGet, claudeProfileURL, token, map[string]string{
		"Content-Type":   "application/json",
		"anthropic-beta": "oauth-2025-04-20",
	}, "")
	snapshot.Status = "success"
	snapshot.Body = usageBody
	snapshot.ProfileStatusCode = profileStatus
	if profileStatus >= http.StatusOK && profileStatus < http.StatusMultipleChoices {
		snapshot.ProfileBody = profileBody
	}
	if errSave := h.saveQuotaSnapshot(ctx, auth, snapshot, now); errSave != nil {
		snapshot.Status = "error"
		snapshot.Error = fmt.Sprintf("failed to persist quota snapshot: %v", errSave)
	}
	return snapshot
}

func (h *Handler) refreshGeminiCliQuotaForAuth(ctx context.Context, auth *coreauth.Auth) quotaSnapshot {
	now := time.Now().UTC()
	snapshot := quotaSnapshot{Status: "error", UpdatedAt: now.Format(time.RFC3339)}

	token, errToken := h.resolveTokenForAuth(ctx, auth, "")
	if errToken != nil {
		snapshot.Error = errToken.Error()
		_ = h.saveQuotaSnapshot(ctx, auth, snapshot, now)
		return snapshot
	}
	projectID := geminiCliProjectIDFromAuth(auth)
	if projectID == "" {
		snapshot.Error = "gemini cli project ID missing"
		_ = h.saveQuotaSnapshot(ctx, auth, snapshot, now)
		return snapshot
	}

	quotaStatus, quotaBody, errQuota := h.fetchBearerQuota(ctx, auth, http.MethodPost, geminiCliQuotaURL, token, map[string]string{
		"Content-Type": "application/json",
	}, fmt.Sprintf(`{"project":%q}`, projectID))
	snapshot.StatusCode = quotaStatus
	if errQuota != nil {
		snapshot.Error = errQuota.Error()
		_ = h.saveQuotaSnapshot(ctx, auth, snapshot, now)
		return snapshot
	}
	if quotaStatus < http.StatusOK || quotaStatus >= http.StatusMultipleChoices {
		snapshot.Error = strings.TrimSpace(quotaBody)
		if snapshot.Error == "" {
			snapshot.Error = fmt.Sprintf("gemini cli quota request failed with status %d", quotaStatus)
		}
		_ = h.saveQuotaSnapshot(ctx, auth, snapshot, now)
		return snapshot
	}

	codeAssistBody := fmt.Sprintf(`{"cloudaicompanionProject":%q,"metadata":{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI","duetProject":%q}}`, projectID, projectID)
	supplementaryStatus, supplementaryBody, _ := h.fetchBearerQuota(ctx, auth, http.MethodPost, geminiCliCodeAssistURL, token, map[string]string{
		"Content-Type": "application/json",
	}, codeAssistBody)
	snapshot.Status = "success"
	snapshot.Body = quotaBody
	snapshot.SupplementaryStatusCode = supplementaryStatus
	if supplementaryStatus >= http.StatusOK && supplementaryStatus < http.StatusMultipleChoices {
		snapshot.SupplementaryBody = supplementaryBody
	}
	if errSave := h.saveQuotaSnapshot(ctx, auth, snapshot, now); errSave != nil {
		snapshot.Status = "error"
		snapshot.Error = fmt.Sprintf("failed to persist quota snapshot: %v", errSave)
	}
	return snapshot
}

func (h *Handler) refreshKimiQuotaForAuth(ctx context.Context, auth *coreauth.Auth) quotaSnapshot {
	now := time.Now().UTC()
	snapshot := quotaSnapshot{Status: "error", UpdatedAt: now.Format(time.RFC3339)}

	token, errToken := h.resolveTokenForAuth(ctx, auth, "")
	if errToken != nil {
		snapshot.Error = errToken.Error()
		_ = h.saveQuotaSnapshot(ctx, auth, snapshot, now)
		return snapshot
	}
	statusCode, body, errFetch := h.fetchBearerQuota(ctx, auth, http.MethodGet, kimiUsageURL, token, nil, "")
	snapshot.StatusCode = statusCode
	if errFetch != nil {
		snapshot.Error = errFetch.Error()
		_ = h.saveQuotaSnapshot(ctx, auth, snapshot, now)
		return snapshot
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		snapshot.Error = strings.TrimSpace(body)
		if snapshot.Error == "" {
			snapshot.Error = fmt.Sprintf("kimi quota request failed with status %d", statusCode)
		}
		_ = h.saveQuotaSnapshot(ctx, auth, snapshot, now)
		return snapshot
	}
	snapshot.Status = "success"
	snapshot.Body = body
	if errSave := h.saveQuotaSnapshot(ctx, auth, snapshot, now); errSave != nil {
		snapshot.Status = "error"
		snapshot.Error = fmt.Sprintf("failed to persist quota snapshot: %v", errSave)
	}
	return snapshot
}

func (h *Handler) codexAccessToken(ctx context.Context, auth *coreauth.Auth, force bool) (*coreauth.Auth, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if auth == nil {
		return nil, "", fmt.Errorf("codex auth missing")
	}

	current := strings.TrimSpace(tokenValueFromMetadata(auth.Metadata))
	if current != "" && !force && !codexAccessTokenNeedsRefresh(current) {
		return auth, current, nil
	}

	h.codexTokenRefreshMu.Lock()
	defer h.codexTokenRefreshMu.Unlock()

	if h.authManager != nil && auth.ID != "" {
		if latest, ok := h.authManager.GetByID(auth.ID); ok && latest != nil {
			auth = latest
		}
	}

	current = strings.TrimSpace(tokenValueFromMetadata(auth.Metadata))
	if current != "" && !force && !codexAccessTokenNeedsRefresh(current) {
		return auth, current, nil
	}

	refreshToken := stringValue(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		return auth, current, fmt.Errorf("codex refresh token missing")
	}

	svc := codexauth.NewCodexAuthWithProxyURL(h.cfg, auth.ProxyURL)
	tokenData, errRefresh := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if errRefresh != nil {
		return auth, current, errRefresh
	}
	now := time.Now().UTC()
	fields := map[string]any{
		"access_token": tokenData.AccessToken,
		"type":         "codex",
		"last_refresh": now.Format(time.RFC3339),
	}
	if strings.TrimSpace(tokenData.IDToken) != "" {
		fields["id_token"] = strings.TrimSpace(tokenData.IDToken)
	}
	if strings.TrimSpace(tokenData.RefreshToken) != "" {
		fields["refresh_token"] = strings.TrimSpace(tokenData.RefreshToken)
	}
	if strings.TrimSpace(tokenData.AccountID) != "" {
		fields["account_id"] = strings.TrimSpace(tokenData.AccountID)
	}
	if strings.TrimSpace(tokenData.Email) != "" {
		fields["email"] = strings.TrimSpace(tokenData.Email)
	}
	if strings.TrimSpace(tokenData.Expire) != "" {
		fields["expired"] = strings.TrimSpace(tokenData.Expire)
	}

	if h.authManager != nil && auth.ID != "" {
		latest, ok := h.authManager.GetByID(auth.ID)
		if !ok || latest == nil {
			latest = auth.Clone()
		}
		if latest.Metadata == nil {
			latest.Metadata = make(map[string]any)
		}
		for key, value := range fields {
			if text, okText := value.(string); okText {
				latest.Metadata[key] = strings.TrimSpace(text)
			} else {
				latest.Metadata[key] = value
			}
		}
		latest.LastRefreshedAt = now
		latest.UpdatedAt = now
		updated, errUpdate := h.authManager.Update(ctx, latest)
		if errUpdate != nil {
			return auth, strings.TrimSpace(tokenData.AccessToken), errUpdate
		}
		if updated != nil {
			auth = updated
		}
	} else {
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		for key, value := range fields {
			if text, ok := value.(string); ok {
				auth.Metadata[key] = strings.TrimSpace(text)
			} else {
				auth.Metadata[key] = value
			}
		}
		auth.LastRefreshedAt = now
		auth.UpdatedAt = now
	}

	return auth, strings.TrimSpace(tokenData.AccessToken), nil
}

func (h *Handler) fetchCodexQuota(ctx context.Context, auth *coreauth.Auth, token string) (int, string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, "", fmt.Errorf("codex access token missing")
	}
	accountID := codexAccountIDFromAuth(auth, token)
	if accountID == "" {
		return 0, "", fmt.Errorf("codex ChatGPT account ID missing")
	}

	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, codexQuotaURL, nil)
	if errReq != nil {
		return 0, "", errReq
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal")
	req.Header.Set("Chatgpt-Account-Id", accountID)

	client := &http.Client{Transport: h.apiCallTransport(auth, "")}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return 0, "", errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return resp.StatusCode, "", errRead
	}
	return resp.StatusCode, string(bodyBytes), nil
}

func (h *Handler) fetchBearerQuota(ctx context.Context, auth *coreauth.Auth, method string, url string, token string, headers map[string]string, body string) (int, string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, "", fmt.Errorf("access token missing")
	}

	var bodyReader io.Reader
	if body != "" {
		bodyReader = bytes.NewBufferString(body)
	}
	req, errReq := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if errReq != nil {
		return 0, "", errReq
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	for key, value := range headers {
		if strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}

	client := &http.Client{Transport: h.apiCallTransport(auth, "")}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return 0, "", errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return resp.StatusCode, "", errRead
	}
	return resp.StatusCode, string(bodyBytes), nil
}

func (h *Handler) saveQuotaSnapshot(ctx context.Context, auth *coreauth.Auth, snapshot quotaSnapshot, now time.Time) error {
	if auth == nil {
		return nil
	}
	if snapshot.UpdatedAt == "" {
		snapshot.UpdatedAt = now.UTC().Format(time.RFC3339)
	}
	if h != nil && h.authManager != nil && auth.ID != "" {
		latest, ok := h.authManager.GetByID(auth.ID)
		if !ok || latest == nil {
			latest = auth.Clone()
		}
		if latest.Metadata == nil {
			latest.Metadata = make(map[string]any)
		}
		latest.Metadata["quota"] = snapshot
		delete(latest.Metadata, "codex_quota")
		latest.UpdatedAt = now.UTC()
		_, errUpdate := h.authManager.Update(ctx, latest)
		if errUpdate != nil {
			log.WithError(errUpdate).Warnf("failed to persist quota snapshot for %s", codexQuotaResultName(auth))
			return errUpdate
		}
		return nil
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["quota"] = snapshot
	delete(auth.Metadata, "codex_quota")
	auth.UpdatedAt = now.UTC()
	return nil
}

func (h *Handler) saveCodexQuotaSnapshot(ctx context.Context, auth *coreauth.Auth, snapshot quotaSnapshot, now time.Time) error {
	return h.saveQuotaSnapshot(ctx, auth, snapshot, now)
}

func (h *Handler) codexQuotaSnapshotFromAuth(auth *coreauth.Auth, now time.Time) quotaSnapshot {
	planType, subscriptionStart, subscriptionUntil := codexSubscriptionFromAuth(auth)
	return quotaSnapshot{
		Status:                  "error",
		UpdatedAt:               now.UTC().Format(time.RFC3339),
		PlanType:                planType,
		SubscriptionActiveStart: subscriptionStart,
		SubscriptionActiveUntil: subscriptionUntil,
	}
}

func codexQuotaResultName(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if name := strings.TrimSpace(auth.FileName); name != "" {
		return name
	}
	return strings.TrimSpace(auth.ID)
}

func codexAccessTokenNeedsRefresh(token string) bool {
	claims, errParse := codexauth.ParseJWTToken(strings.TrimSpace(token))
	if errParse != nil || claims == nil || claims.Exp <= 0 {
		return false
	}
	return !time.Unix(int64(claims.Exp), 0).After(time.Now().Add(codexAccessTokenSkew))
}

func codexAccountIDFromAuth(auth *coreauth.Auth, accessToken string) string {
	if auth != nil && auth.Metadata != nil {
		if idToken, ok := auth.Metadata["id_token"].(string); ok {
			if claims, errParse := codexauth.ParseJWTToken(idToken); errParse == nil && claims != nil {
				if accountID := strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID); accountID != "" {
					return accountID
				}
			}
		}
		if accountID := stringValue(auth.Metadata, "account_id"); accountID != "" {
			return accountID
		}
	}
	if claims, errParse := codexauth.ParseJWTToken(accessToken); errParse == nil && claims != nil {
		return strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID)
	}
	return ""
}

func codexSubscriptionFromAuth(auth *coreauth.Auth) (string, any, any) {
	if auth == nil || auth.Metadata == nil {
		return "", nil, nil
	}
	idToken, ok := auth.Metadata["id_token"].(string)
	if !ok || strings.TrimSpace(idToken) == "" {
		return "", nil, nil
	}
	claims, errParse := codexauth.ParseJWTToken(idToken)
	if errParse != nil || claims == nil {
		return "", nil, nil
	}
	return strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType),
		claims.CodexAuthInfo.ChatgptSubscriptionActiveStart,
		claims.CodexAuthInfo.ChatgptSubscriptionActiveUntil
}

func codexPlanTypeFromQuotaBody(body string, fallback string) string {
	var payload struct {
		PlanType      string `json:"plan_type"`
		PlanTypeCamel string `json:"planType"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return fallback
	}
	if strings.TrimSpace(payload.PlanType) != "" {
		return strings.TrimSpace(payload.PlanType)
	}
	if strings.TrimSpace(payload.PlanTypeCamel) != "" {
		return strings.TrimSpace(payload.PlanTypeCamel)
	}
	return fallback
}

func antigravityProjectIDFromAuth(auth *coreauth.Auth) string {
	if auth != nil && auth.Metadata != nil {
		if value := stringValue(auth.Metadata, "project_id"); value != "" {
			return value
		}
		if value := stringValue(auth.Metadata, "projectId"); value != "" {
			return value
		}
		if nested := nestedStringValue(auth.Metadata, "installed", "project_id"); nested != "" {
			return nested
		}
		if nested := nestedStringValue(auth.Metadata, "installed", "projectId"); nested != "" {
			return nested
		}
		if nested := nestedStringValue(auth.Metadata, "web", "project_id"); nested != "" {
			return nested
		}
		if nested := nestedStringValue(auth.Metadata, "web", "projectId"); nested != "" {
			return nested
		}
	}
	return defaultAntigravityProjectID
}

func geminiCliProjectIDFromAuth(auth *coreauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if value := stringValue(auth.Metadata, "project_id"); value != "" {
		return value
	}
	account := stringValue(auth.Metadata, "account")
	if account == "" {
		return ""
	}
	start := strings.LastIndex(account, "(")
	end := strings.LastIndex(account, ")")
	if start < 0 || end <= start+1 {
		return ""
	}
	return strings.TrimSpace(account[start+1 : end])
}

func nestedStringValue(metadata map[string]any, objectKey string, valueKey string) string {
	raw, ok := metadata[objectKey]
	if !ok || raw == nil {
		return ""
	}
	nested, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	return stringValue(nested, valueKey)
}
