package management

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexAccessTokenNeedsRefresh(t *testing.T) {
	expired := testJWT(t, map[string]any{"exp": time.Now().Add(-time.Minute).Unix()})
	if !codexAccessTokenNeedsRefresh(expired) {
		t.Fatal("expected expired token to need refresh")
	}

	fresh := testJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	if codexAccessTokenNeedsRefresh(fresh) {
		t.Fatal("expected fresh token to skip refresh")
	}

	if codexAccessTokenNeedsRefresh("not-a-jwt") {
		t.Fatal("expected unparsable token to skip proactive refresh")
	}
}

func TestCodexSubscriptionFromAuth(t *testing.T) {
	idToken := testJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type":                 "plus",
			"chatgpt_subscription_active_start": "2026-05-01T00:00:00Z",
			"chatgpt_subscription_active_until": "2026-05-16T09:30:00Z",
		},
	})
	auth := &coreauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"id_token": idToken},
	}

	planType, activeStart, activeUntil := codexSubscriptionFromAuth(auth)
	if planType != "plus" {
		t.Fatalf("planType = %q, want plus", planType)
	}
	if activeStart != "2026-05-01T00:00:00Z" {
		t.Fatalf("activeStart = %v", activeStart)
	}
	if activeUntil != "2026-05-16T09:30:00Z" {
		t.Fatalf("activeUntil = %v", activeUntil)
	}
}

func TestCodexPlanTypeFromQuotaBody(t *testing.T) {
	if got := codexPlanTypeFromQuotaBody(`{"plan_type":"pro"}`, "free"); got != "pro" {
		t.Fatalf("plan type = %q, want pro", got)
	}
	if got := codexPlanTypeFromQuotaBody(`{"planType":"plus"}`, "free"); got != "plus" {
		t.Fatalf("plan type = %q, want plus", got)
	}
	if got := codexPlanTypeFromQuotaBody(`{`, "free"); got != "free" {
		t.Fatalf("plan type = %q, want fallback", got)
	}
}

func TestRefreshCodexQuota_SavesSnapshotAndSendsAccountHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var accountHeader string
	var authorizationHeader string
	accessToken := testAccessToken(t, "acct-header", time.Now().Add(time.Hour))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountHeader = r.Header.Get("Chatgpt-Account-Id")
		authorizationHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		if _, errWrite := w.Write([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":11,"reset_after_seconds":3600},"secondary_window":{"used_percent":22,"reset_after_seconds":86400}}}`)); errWrite != nil {
			t.Fatalf("failed to write quota response: %v", errWrite)
		}
	}))
	defer upstream.Close()
	restoreCodexQuotaURL(t, upstream.URL)

	h, manager := newCodexQuotaTestHandler(t)
	auth := testCodexAuth(t, "auth-1", "alpha.json", "acct-header", "free", time.Now().Add(time.Hour))
	auth.Metadata["access_token"] = accessToken
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codex-quota/refresh", bytes.NewReader([]byte(`{"names":["alpha.json"]}`)))
	ginCtx.Request.Header.Set("Content-Type", "application/json")

	h.RefreshCodexQuota(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body %s", rec.Code, rec.Body.String())
	}
	if accountHeader != "acct-header" {
		t.Fatalf("account header = %q, want acct-header", accountHeader)
	}
	if authorizationHeader != "Bearer "+accessToken {
		t.Fatalf("authorization header = %q", authorizationHeader)
	}

	updated, ok := manager.GetByID("auth-1")
	if !ok {
		t.Fatal("expected auth to remain registered")
	}
	snapshot := quotaSnapshotFromMetadata(t, updated.Metadata)
	if snapshot.Status != "success" {
		t.Fatalf("snapshot status = %q, want success", snapshot.Status)
	}
	if snapshot.PlanType != "plus" {
		t.Fatalf("snapshot plan type = %q, want plus", snapshot.PlanType)
	}
	if snapshot.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status code = %d, want 200", snapshot.StatusCode)
	}
	if snapshot.Body == "" {
		t.Fatal("expected quota body to be stored")
	}
}

func TestRefreshCodexQuota_SavesErrorSnapshot(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"bad token"}`, http.StatusUnauthorized)
	}))
	defer upstream.Close()
	restoreCodexQuotaURL(t, upstream.URL)

	h, manager := newCodexQuotaTestHandler(t)
	auth := testCodexAuth(t, "auth-1", "alpha.json", "acct-error", "plus", time.Now().Add(time.Hour))
	auth.Metadata["refresh_token"] = ""
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	snapshot := h.refreshCodexQuotaForAuth(context.Background(), auth)
	if snapshot.Status != "error" {
		t.Fatalf("snapshot status = %q, want error", snapshot.Status)
	}
	if snapshot.StatusCode != http.StatusUnauthorized {
		t.Fatalf("snapshot status code = %d, want 401", snapshot.StatusCode)
	}
	if snapshot.Error == "" {
		t.Fatal("expected error message to be stored")
	}

	updated, ok := manager.GetByID("auth-1")
	if !ok {
		t.Fatal("expected auth to remain registered")
	}
	stored := quotaSnapshotFromMetadata(t, updated.Metadata)
	if stored.Status != "error" || stored.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stored snapshot = %#v, want 401 error", stored)
	}
}

func TestBuildAuthFileEntry_IncludesCodexQuotaSnapshot(t *testing.T) {
	now := time.Date(2026, 5, 6, 1, 2, 3, 0, time.UTC)
	h := &Handler{}
	auth := testCodexAuth(t, "auth-1", "alpha.json", "acct-file", "plus", time.Now().Add(time.Hour))
	auth.Attributes = map[string]string{"path": t.TempDir() + "/alpha.json"}
	auth.Metadata["quota"] = quotaSnapshot{
		Status:     "success",
		StatusCode: http.StatusOK,
		UpdatedAt:  now.Format(time.RFC3339),
		PlanType:   "plus",
	}

	entry := h.buildAuthFileEntry(auth)
	if entry == nil {
		t.Fatal("expected auth file entry")
	}
	rawSnapshot, ok := entry["quota"].(quotaSnapshot)
	if !ok {
		t.Fatalf("quota = %#v, want quotaSnapshot", entry["quota"])
	}
	if rawSnapshot.PlanType != "plus" {
		t.Fatalf("snapshot plan type = %q, want plus", rawSnapshot.PlanType)
	}
	if claims, ok := entry["id_token"].(gin.H); !ok || claims["plan_type"] != "plus" {
		t.Fatalf("id token claims = %#v, want plus plan", entry["id_token"])
	}
}

func TestCodexQuotaTargets_FiltersCodexOAuthAuths(t *testing.T) {
	_, manager := newCodexQuotaTestHandler(t)
	h := &Handler{authManager: manager}

	seedAuths := []*coreauth.Auth{
		testCodexAuth(t, "codex-oauth", "b.json", "acct-a", "plus", time.Now().Add(time.Hour)),
		{
			ID:         "codex-api-key",
			Provider:   "codex",
			FileName:   "a.json",
			Attributes: map[string]string{"api_key": "sk-test"},
		},
		func() *coreauth.Auth {
			auth := testCodexAuth(t, "disabled", "disabled.json", "acct-disabled", "free", time.Now().Add(time.Hour))
			auth.Disabled = true
			return auth
		}(),
		{
			ID:       "claude",
			Provider: "claude",
			FileName: "claude.json",
		},
	}
	for _, auth := range seedAuths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("failed to register %s: %v", auth.ID, errRegister)
		}
	}

	all := h.codexQuotaTargets(nil, true)
	if len(all) != 1 || all[0].ID != "codex-oauth" {
		t.Fatalf("all targets = %#v, want only codex-oauth", authIDs(all))
	}

	byID := h.codexQuotaTargets([]string{"codex-oauth"}, false)
	if len(byID) != 1 || byID[0].ID != "codex-oauth" {
		t.Fatalf("id targets = %#v, want codex-oauth", authIDs(byID))
	}

	missing := h.codexQuotaTargets([]string{"missing.json"}, false)
	if len(missing) != 0 {
		t.Fatalf("missing targets = %#v, want none", authIDs(missing))
	}
}

func TestQuotaTargets_IncludesSupportedProviders(t *testing.T) {
	_, manager := newCodexQuotaTestHandler(t)
	h := &Handler{authManager: manager}

	seedAuths := []*coreauth.Auth{
		testCodexAuth(t, "codex-oauth", "codex.json", "acct-a", "plus", time.Now().Add(time.Hour)),
		{
			ID:       "claude",
			Provider: "claude",
			FileName: "claude.json",
		},
		{
			ID:       "xai",
			Provider: "xai",
			FileName: "xai.json",
			Metadata: map[string]any{"auth_kind": "oauth"},
		},
		{
			ID:         "xai-api-key",
			Provider:   "xai",
			FileName:   "xai-api-key.json",
			Attributes: map[string]string{"api_key": "xai-test"},
		},
		{
			ID:         "codex-api-key",
			Provider:   "codex",
			FileName:   "api-key.json",
			Attributes: map[string]string{"api_key": "sk-test"},
		},
		{
			ID:       "unsupported",
			Provider: "openai",
			FileName: "openai.json",
		},
	}
	for _, auth := range seedAuths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("failed to register %s: %v", auth.ID, errRegister)
		}
	}

	all := h.quotaTargets(nil, true)
	gotIDs := authIDs(all)
	gotSet := make(map[string]bool, len(gotIDs))
	for _, id := range gotIDs {
		gotSet[id] = true
	}
	if len(gotSet) != 3 || !gotSet["codex-oauth"] || !gotSet["claude"] || !gotSet["xai"] {
		t.Fatalf("all targets = %#v, want codex-oauth, claude, and xai", gotIDs)
	}

	selected := h.quotaTargets([]string{"claude.json"}, false)
	if got := authIDs(selected); len(got) != 1 || got[0] != "claude" {
		t.Fatalf("selected targets = %#v, want claude", got)
	}
}

func TestRefreshXAIQuota_SavesBothBillingResponses(t *testing.T) {
	var authorizationHeader string
	var tokenAuthHeader string
	var clientVersionHeader string
	var userIDHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizationHeader = r.Header.Get("Authorization")
		tokenAuthHeader = r.Header.Get("X-XAI-Token-Auth")
		clientVersionHeader = r.Header.Get("X-Grok-Client-Version")
		userIDHeader = r.Header.Get("X-UserID")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.RawQuery {
		case "format=credits":
			_, _ = w.Write([]byte(`{"config":{"monthly_limit":15000}}`))
		default:
			_, _ = w.Write([]byte(`{"config":{"usage_percent":51}}`))
		}
	}))
	defer upstream.Close()
	restoreXAIQuotaURLs(t, upstream.URL+"?format=credits", upstream.URL)

	h, manager := newCodexQuotaTestHandler(t)
	auth := &coreauth.Auth{
		ID:       "xai-auth",
		Provider: "xai",
		FileName: "xai.json",
		Metadata: map[string]any{
			"access_token": "xai-access-token",
			"auth_kind":    "oauth",
			"sub":          "xai-user-id",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	snapshot := h.refreshQuotaForAuth(context.Background(), auth)
	if snapshot.Status != "success" || snapshot.StatusCode != http.StatusOK || snapshot.SupplementaryStatusCode != http.StatusOK {
		t.Fatalf("snapshot = %#v, want both xAI billing requests successful", snapshot)
	}
	if snapshot.Body != `{"config":{"monthly_limit":15000}}` || snapshot.SupplementaryBody != `{"config":{"usage_percent":51}}` {
		t.Fatalf("snapshot bodies = %q and %q", snapshot.Body, snapshot.SupplementaryBody)
	}
	if authorizationHeader != "Bearer xai-access-token" || tokenAuthHeader != "xai-grok-cli" || clientVersionHeader != "0.2.120" || userIDHeader != "xai-user-id" {
		t.Fatalf("xAI headers = authorization %q, token auth %q, version %q, user ID %q", authorizationHeader, tokenAuthHeader, clientVersionHeader, userIDHeader)
	}
	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected xAI auth to remain registered")
	}
	persisted := quotaSnapshotFromMetadata(t, updated.Metadata)
	if persisted.Status != "success" || persisted.Body != snapshot.Body || persisted.SupplementaryBody != snapshot.SupplementaryBody {
		t.Fatalf("persisted snapshot = %#v, want complete xAI quota snapshot", persisted)
	}
}

func TestRefreshXAIQuota_SucceedsWhenOneBillingEndpointIsAvailable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery == "format=credits" {
			http.Error(w, "credits unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config":{"usage_percent":51}}`))
	}))
	defer upstream.Close()
	restoreXAIQuotaURLs(t, upstream.URL+"?format=credits", upstream.URL)

	h, manager := newCodexQuotaTestHandler(t)
	auth := &coreauth.Auth{ID: "xai-auth", Provider: "xai", FileName: "xai.json", Metadata: map[string]any{"access_token": "xai-access-token", "auth_kind": "oauth"}}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	snapshot := h.refreshQuotaForAuth(context.Background(), auth)
	if snapshot.Status != "success" || snapshot.StatusCode != http.StatusServiceUnavailable || snapshot.SupplementaryStatusCode != http.StatusOK {
		t.Fatalf("snapshot = %#v, want available xAI billing response preserved", snapshot)
	}
}

func TestSaveCodexQuotaSnapshot_PreservesLatestTokenMetadata(t *testing.T) {
	h, manager := newCodexQuotaTestHandler(t)
	latest := testCodexAuth(t, "auth-1", "alpha.json", "acct-latest", "plus", time.Now().Add(time.Hour))
	latest.Metadata["refresh_token"] = "new-refresh-token"
	latest.Metadata["access_token"] = "new-access-token"
	if _, errRegister := manager.Register(context.Background(), latest); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	stale := latest.Clone()
	stale.Metadata["refresh_token"] = "old-refresh-token"
	stale.Metadata["access_token"] = "old-access-token"

	if errSave := h.saveCodexQuotaSnapshot(context.Background(), stale, quotaSnapshot{
		Status:    "success",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		PlanType:  "plus",
	}, time.Now()); errSave != nil {
		t.Fatalf("save snapshot returned error: %v", errSave)
	}

	updated, ok := manager.GetByID("auth-1")
	if !ok {
		t.Fatal("expected auth to remain registered")
	}
	if got := updated.Metadata["refresh_token"]; got != "new-refresh-token" {
		t.Fatalf("refresh_token = %q, want latest token", got)
	}
	if got := updated.Metadata["access_token"]; got != "new-access-token" {
		t.Fatalf("access_token = %q, want latest token", got)
	}
	if _, ok := updated.Metadata["quota"]; !ok {
		t.Fatal("expected quota snapshot to be merged")
	}
	if _, ok := updated.Metadata["codex_quota"]; ok {
		t.Fatal("expected legacy codex quota snapshot to be removed")
	}
}

func TestSaveCodexQuotaSnapshot_ReturnsPersistError(t *testing.T) {
	store := &failingAuthStore{err: errors.New("save failed")}
	manager := coreauth.NewManager(store, nil, nil)
	h := &Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}
	auth := testCodexAuth(t, "auth-1", "alpha.json", "acct-save", "plus", time.Now().Add(time.Hour))
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	errSave := h.saveCodexQuotaSnapshot(context.Background(), auth, quotaSnapshot{
		Status:    "success",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}, time.Now())
	if errSave == nil {
		t.Fatal("expected persist error")
	}
}

func TestCodexQuotaAutoRefreshStartStopIdempotent(t *testing.T) {
	h := &Handler{}

	h.StartCodexQuotaAutoRefresh()
	h.codexQuotaAutoMu.Lock()
	started := h.codexQuotaAutoCancel != nil
	h.codexQuotaAutoMu.Unlock()
	if !started {
		t.Fatal("expected auto refresh cancel function after start")
	}

	h.StartCodexQuotaAutoRefresh()
	h.StopCodexQuotaAutoRefresh()
	h.codexQuotaAutoMu.Lock()
	stopped := h.codexQuotaAutoCancel == nil
	h.codexQuotaAutoMu.Unlock()
	if !stopped {
		t.Fatal("expected auto refresh cancel function to clear after stop")
	}

	h.StopCodexQuotaAutoRefresh()
}

func TestQuotaRefreshStatusUsesBatchCompletion(t *testing.T) {
	completedAt := time.Date(2026, 5, 10, 11, 37, 37, 0, time.UTC)
	h := &Handler{}

	h.updateQuotaRefreshStatus(completedAt, map[string]quotaSnapshot{
		"alpha.json": {Status: "success", UpdatedAt: completedAt.Add(-time.Minute).Format(time.RFC3339)},
		"beta.json":  {Status: "error", UpdatedAt: completedAt.Add(time.Minute).Format(time.RFC3339)},
		"gamma.json": {Status: "skipped", UpdatedAt: completedAt.Add(2 * time.Minute).Format(time.RFC3339)},
	})

	status := h.quotaRefreshStatusSnapshot()
	if status.LastCompletedAt != completedAt.Format(time.RFC3339) {
		t.Fatalf("last completed at = %q, want %q", status.LastCompletedAt, completedAt.Format(time.RFC3339))
	}
	if status.NextRunAt != completedAt.Add(quotaRefreshInterval).Format(time.RFC3339) {
		t.Fatalf("next run at = %q, want %q", status.NextRunAt, completedAt.Add(quotaRefreshInterval).Format(time.RFC3339))
	}
	if status.Success != 1 || status.Failed != 1 {
		t.Fatalf("status counts = %d success, %d failed; want 1 success, 1 failed", status.Success, status.Failed)
	}
}

func TestQuotaAutoRefreshDoesNotRecordCanceledBatch(t *testing.T) {
	completedAt := time.Date(2026, 5, 10, 11, 37, 37, 0, time.UTC)
	h, manager := newCodexQuotaTestHandler(t)
	h.updateQuotaRefreshStatus(completedAt, map[string]quotaSnapshot{
		"previous.json": {Status: "success", UpdatedAt: completedAt.Format(time.RFC3339)},
	})

	ctx, cancel := context.WithCancel(context.Background())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"plan_type":"plus"}`))
	}))
	defer upstream.Close()
	restoreCodexQuotaURL(t, upstream.URL)

	auth := testCodexAuth(t, "auth-1", "alpha.json", "acct-1", "plus", time.Now().Add(time.Hour))
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	h.refreshAllQuotaInBackground(ctx)

	status := h.quotaRefreshStatusSnapshot()
	if status.LastCompletedAt != completedAt.Format(time.RFC3339) {
		t.Fatalf("last completed at = %q, want previous %q", status.LastCompletedAt, completedAt.Format(time.RFC3339))
	}
	if status.NextRunAt != completedAt.Add(quotaRefreshInterval).Format(time.RFC3339) {
		t.Fatalf("next run at = %q, want previous %q", status.NextRunAt, completedAt.Add(quotaRefreshInterval).Format(time.RFC3339))
	}
	if status.Success != 1 || status.Failed != 0 {
		t.Fatalf("status counts = %d success, %d failed; want previous 1 success, 0 failed", status.Success, status.Failed)
	}
}

func testJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "none", "typ": "JWT"}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(headerBytes) + "." +
		base64.RawURLEncoding.EncodeToString(payloadBytes) + "."
}

func newCodexQuotaTestHandler(t *testing.T) (*Handler, *coreauth.Manager) {
	t.Helper()
	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	return &Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}, manager
}

func restoreCodexQuotaURL(t *testing.T, value string) {
	t.Helper()
	oldValue := codexQuotaURL
	codexQuotaURL = value
	t.Cleanup(func() {
		codexQuotaURL = oldValue
	})
}

func restoreXAIQuotaURLs(t *testing.T, creditsURL string, billingURL string) {
	t.Helper()
	oldCreditsURL := xaiBillingCreditsURL
	oldBillingURL := xaiBillingURL
	xaiBillingCreditsURL = creditsURL
	xaiBillingURL = billingURL
	t.Cleanup(func() {
		xaiBillingCreditsURL = oldCreditsURL
		xaiBillingURL = oldBillingURL
	})
}

func testCodexAuth(t *testing.T, id string, fileName string, accountID string, plan string, accessTokenExpiry time.Time) *coreauth.Auth {
	t.Helper()
	return &coreauth.Auth{
		ID:       id,
		Provider: "codex",
		FileName: fileName,
		Metadata: map[string]any{
			"type":          "codex",
			"email":         id + "@example.com",
			"access_token":  testAccessToken(t, accountID, accessTokenExpiry),
			"refresh_token": "refresh-token",
			"id_token": testJWT(t, map[string]any{
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_account_id":                   accountID,
					"chatgpt_plan_type":                    plan,
					"chatgpt_subscription_active_start":    "2026-05-01T00:00:00Z",
					"chatgpt_subscription_active_until":    "2026-05-16T09:30:00Z",
					"chatgpt_subscription_last_checked_at": "2026-05-06T00:00:00Z",
				},
			}),
		},
	}
}

func testAccessToken(t *testing.T, accountID string, expiry time.Time) string {
	t.Helper()
	return testJWT(t, map[string]any{
		"exp": expiry.Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
		},
	})
}

func quotaSnapshotFromMetadata(t *testing.T, metadata map[string]any) quotaSnapshot {
	t.Helper()
	raw := metadata["quota"]
	switch typed := raw.(type) {
	case quotaSnapshot:
		return typed
	case map[string]any:
		data, errMarshal := json.Marshal(typed)
		if errMarshal != nil {
			t.Fatalf("failed to marshal snapshot: %v", errMarshal)
		}
		var snapshot quotaSnapshot
		if errUnmarshal := json.Unmarshal(data, &snapshot); errUnmarshal != nil {
			t.Fatalf("failed to unmarshal snapshot: %v", errUnmarshal)
		}
		return snapshot
	default:
		t.Fatalf("quota = %#v, want snapshot", raw)
		return quotaSnapshot{}
	}
}

func authIDs(auths []*coreauth.Auth) []string {
	out := make([]string, 0, len(auths))
	for _, auth := range auths {
		if auth != nil {
			out = append(out, auth.ID)
		}
	}
	return out
}

type failingAuthStore struct {
	memoryAuthStore
	err error
}

func (s *failingAuthStore) Save(context.Context, *coreauth.Auth) (string, error) {
	return "", s.err
}
