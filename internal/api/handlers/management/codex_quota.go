package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	codexauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	codexQuotaInterval     = 10 * time.Minute
	codexQuotaInitialDelay = 10 * time.Second
	codexQuotaConcurrency  = 5
	codexAccessTokenSkew   = time.Minute
)

var codexQuotaURL = "https://chatgpt.com/backend-api/wham/usage"

type codexQuotaRefreshRequest struct {
	Names []string `json:"names"`
	All   bool     `json:"all"`
}

type codexQuotaSnapshot struct {
	Status                  string `json:"status"`
	StatusCode              int    `json:"status_code,omitempty"`
	Body                    string `json:"body,omitempty"`
	Error                   string `json:"error,omitempty"`
	UpdatedAt               string `json:"updated_at"`
	PlanType                string `json:"plan_type,omitempty"`
	SubscriptionActiveStart any    `json:"subscription_active_start,omitempty"`
	SubscriptionActiveUntil any    `json:"subscription_active_until,omitempty"`
}

func (h *Handler) StartCodexQuotaAutoRefresh() {
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
		timer := time.NewTimer(codexQuotaInitialDelay)
		defer timer.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				h.refreshAllCodexQuotaInBackground(ctx)
				timer.Reset(codexQuotaInterval)
			}
		}
	}()
}

func (h *Handler) StopCodexQuotaAutoRefresh() {
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

func (h *Handler) refreshAllCodexQuotaInBackground(ctx context.Context) {
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

	auths := h.codexQuotaTargets(nil, true)
	if len(auths) == 0 {
		return
	}
	log.Debugf("management codex quota auto refresh targets: %d", len(auths))
	h.refreshCodexQuotaBatch(ctx, auths)
}

func (h *Handler) RefreshCodexQuota(c *gin.Context) {
	var body codexQuotaRefreshRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil && errBind != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	auths := h.codexQuotaTargets(body.Names, body.All || len(body.Names) == 0)
	results := h.refreshCodexQuotaBatch(c.Request.Context(), auths)
	c.JSON(http.StatusOK, gin.H{"results": results})
}

func (h *Handler) codexQuotaTargets(names []string, all bool) []*coreauth.Auth {
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
		if auth == nil || auth.Disabled || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		if accountType, _ := auth.AccountInfo(); strings.EqualFold(accountType, "api_key") {
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

func (h *Handler) refreshCodexQuotaBatch(ctx context.Context, auths []*coreauth.Auth) map[string]codexQuotaSnapshot {
	results := make(map[string]codexQuotaSnapshot, len(auths))
	if len(auths) == 0 {
		return results
	}

	var mu sync.Mutex
	jobs := make(chan *coreauth.Auth)
	workers := codexQuotaConcurrency
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
				snapshot := h.refreshCodexQuotaForAuth(ctx, auth)
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

func (h *Handler) refreshCodexQuotaForAuth(ctx context.Context, auth *coreauth.Auth) codexQuotaSnapshot {
	now := time.Now().UTC()
	snapshot := h.codexQuotaSnapshotFromAuth(auth, now)

	refreshedAuth, token, errToken := h.codexAccessToken(ctx, auth, false)
	if errToken != nil {
		snapshot.Status = "error"
		snapshot.Error = errToken.Error()
		if errSave := h.saveCodexQuotaSnapshot(ctx, refreshedAuth, snapshot, now); errSave != nil {
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
		if errSave := h.saveCodexQuotaSnapshot(ctx, auth, snapshot, now); errSave != nil {
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
		if errSave := h.saveCodexQuotaSnapshot(ctx, auth, snapshot, now); errSave != nil {
			snapshot.Error = fmt.Sprintf("%s; failed to persist codex quota snapshot: %v", snapshot.Error, errSave)
		}
		return snapshot
	}

	snapshot.Status = "success"
	snapshot.Body = body
	snapshot.Error = ""
	snapshot.PlanType = codexPlanTypeFromQuotaBody(body, snapshot.PlanType)
	if errSave := h.saveCodexQuotaSnapshot(ctx, auth, snapshot, now); errSave != nil {
		snapshot.Status = "error"
		snapshot.Error = fmt.Sprintf("failed to persist codex quota snapshot: %v", errSave)
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
		updated, errPatch := h.authManager.Patch(ctx, auth.ID, func(latest *coreauth.Auth) {
			if latest.Metadata == nil {
				latest.Metadata = make(map[string]any)
			}
			for key, value := range fields {
				if text, ok := value.(string); ok {
					latest.Metadata[key] = strings.TrimSpace(text)
				} else {
					latest.Metadata[key] = value
				}
			}
			latest.LastRefreshedAt = now
			latest.UpdatedAt = now
		})
		if errPatch != nil {
			return auth, strings.TrimSpace(tokenData.AccessToken), errPatch
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

	client := &http.Client{Transport: h.apiCallTransport(auth)}
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

func (h *Handler) saveCodexQuotaSnapshot(ctx context.Context, auth *coreauth.Auth, snapshot codexQuotaSnapshot, now time.Time) error {
	if auth == nil {
		return nil
	}
	if snapshot.UpdatedAt == "" {
		snapshot.UpdatedAt = now.UTC().Format(time.RFC3339)
	}
	if h != nil && h.authManager != nil && auth.ID != "" {
		_, errPatch := h.authManager.Patch(ctx, auth.ID, func(latest *coreauth.Auth) {
			if latest.Metadata == nil {
				latest.Metadata = make(map[string]any)
			}
			latest.Metadata["codex_quota"] = snapshot
			latest.UpdatedAt = now.UTC()
		})
		if errPatch != nil {
			log.WithError(errPatch).Warnf("failed to persist codex quota snapshot for %s", codexQuotaResultName(auth))
			return errPatch
		}
		return nil
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["codex_quota"] = snapshot
	auth.UpdatedAt = now.UTC()
	return nil
}

func (h *Handler) codexQuotaSnapshotFromAuth(auth *coreauth.Auth, now time.Time) codexQuotaSnapshot {
	planType, subscriptionStart, subscriptionUntil := codexSubscriptionFromAuth(auth)
	return codexQuotaSnapshot{
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
