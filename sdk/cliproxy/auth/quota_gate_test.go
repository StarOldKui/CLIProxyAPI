package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestQuotaSnapshotCooldown_ProviderSnapshots(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	nextRefresh := now.Add(quotaSnapshotFallbackCooldown)
	shortReset := now.Add(time.Hour)
	longReset := now.Add(7 * 24 * time.Hour)

	tests := []struct {
		name      string
		auth      *Auth
		model     string
		wantReset time.Time
		wantBlock bool
	}{
		{
			name: "codex weekly exhausted uses longest exhausted reset",
			auth: quotaGateTestAuth("codex", fmt.Sprintf(`{
				"rate_limit": {
					"allowed": false,
					"limit_reached": true,
					"primary_window": {"used_percent": 79, "reset_at": %d},
					"secondary_window": {"used_percent": 100, "reset_at": %d}
				}
			}`, shortReset.Unix(), longReset.Unix())),
			wantReset: longReset,
			wantBlock: true,
		},
		{
			name: "codex used percent exhausted blocks without explicit limit flag",
			auth: quotaGateTestAuth("codex", `{
				"rate_limit": {
					"allowed": true,
					"primary_window": {"used_percent": 100, "reset_after_seconds": 3600}
				}
			}`),
			wantReset: shortReset,
			wantBlock: true,
		},
		{
			name: "codex past reset stays blocked until refreshed snapshot recovers",
			auth: quotaGateTestAuth("codex", fmt.Sprintf(`{
				"rateLimit": {
					"allowed": false,
					"limitReached": true,
					"primaryWindow": {"usedPercent": 100, "resetAt": %d}
				}
			}`, now.Add(-time.Hour).Unix())),
			wantReset: nextRefresh,
			wantBlock: true,
		},
		{
			name: "codex restored snapshot does not block",
			auth: quotaGateTestAuth("codex", `{
				"rate_limit": {
					"allowed": true,
					"limit_reached": false,
					"primary_window": {"used_percent": 35}
				}
			}`),
			wantBlock: false,
		},
		{
			name: "codex code review exhausted does not block normal model",
			auth: quotaGateTestAuth("codex", fmt.Sprintf(`{
				"rate_limit": {
					"allowed": true,
					"primary_window": {"used_percent": 35, "reset_at": %d}
				},
				"code_review_rate_limit": {
					"allowed": false,
					"primary_window": {"used_percent": 100, "reset_at": %d}
				}
			}`, shortReset.Unix(), longReset.Unix())),
			wantBlock: false,
		},
		{
			name: "claude weekly exhausted",
			auth: quotaGateTestAuth("claude", fmt.Sprintf(`{
				"five_hour": {"utilization": 0, "resets_at": %q},
				"seven_day": {"utilization": 100, "resets_at": %q}
			}`, shortReset.Format(time.RFC3339Nano), longReset.Format(time.RFC3339Nano))),
			wantReset: longReset,
			wantBlock: true,
		},
		{
			name:      "claude exhausted without reset stays blocked until next refresh",
			auth:      quotaGateTestAuth("claude", `{"seven_day":{"utilization":100}}`),
			wantReset: nextRefresh,
			wantBlock: true,
		},
		{
			name: "claude opus exhausted does not block sonnet",
			auth: quotaGateTestAuth("claude", fmt.Sprintf(`{
				"five_hour": {"utilization": 0, "resets_at": %q},
				"seven_day_opus": {"utilization": 100, "resets_at": %q}
			}`, shortReset.Format(time.RFC3339Nano), longReset.Format(time.RFC3339Nano))),
			model:     "claude-sonnet-4",
			wantBlock: false,
		},
		{
			name: "claude opus exhausted blocks opus",
			auth: quotaGateTestAuth("claude", fmt.Sprintf(`{
				"seven_day_opus": {"utilization": 100, "resets_at": %q}
			}`, longReset.Format(time.RFC3339Nano))),
			model:     "claude-opus-4",
			wantReset: longReset,
			wantBlock: true,
		},
		{
			name: "antigravity depleted model",
			auth: quotaGateTestAuth("antigravity", fmt.Sprintf(`{
				"models": {
					"claude-sonnet-4": {"quotaInfo": {"remainingFraction": 0, "resetTime": %q}}
				}
			}`, shortReset.Format(time.RFC3339Nano))),
			wantReset: shortReset,
			wantBlock: true,
		},
		{
			name: "antigravity depleted unmatched model does not block",
			auth: quotaGateTestAuth("antigravity", fmt.Sprintf(`{
				"models": {
					"claude-opus-4": {"quotaInfo": {"remainingFraction": 0, "resetTime": %q}}
				}
			}`, shortReset.Format(time.RFC3339Nano))),
			model:     "claude-sonnet-4",
			wantBlock: false,
		},
		{
			name: "gemini cli depleted bucket",
			auth: quotaGateTestAuth("gemini-cli", fmt.Sprintf(`{
				"buckets": [{"remaining_fraction": 0, "reset_time": %q}]
			}`, shortReset.Format(time.RFC3339Nano))),
			wantReset: shortReset,
			wantBlock: true,
		},
		{
			name: "gemini cli depleted unmatched model bucket does not block",
			auth: quotaGateTestAuth("gemini-cli", fmt.Sprintf(`{
				"buckets": [{"model": "gemini-3-pro", "remaining_fraction": 0, "reset_time": %q}]
			}`, shortReset.Format(time.RFC3339Nano))),
			model:     "gemini-3-flash",
			wantBlock: false,
		},
		{
			name:      "kimi depleted usage",
			auth:      quotaGateTestAuth("kimi", `{"usage":{"used":100,"limit":100,"reset_in":3600}}`),
			wantReset: shortReset,
			wantBlock: true,
		},
		{
			name: "error snapshot ignored",
			auth: &Auth{
				Provider: "codex",
				Metadata: map[string]any{
					"quota": map[string]any{
						"status": "error",
						"body":   `{"rate_limit":{"allowed":false}}`,
					},
				},
			},
			wantBlock: false,
		},
		{
			name:      "unknown provider ignored",
			auth:      quotaGateTestAuth("unknown", fmt.Sprintf(`{"usage":{"remaining":0,"reset_at":%q}}`, shortReset.Format(time.RFC3339Nano))),
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotReset, gotBlock := quotaSnapshotCooldown(tt.auth, tt.model, now)
			if gotBlock != tt.wantBlock {
				t.Fatalf("quotaSnapshotCooldown() block = %v, want %v", gotBlock, tt.wantBlock)
			}
			if tt.wantBlock && !gotReset.Equal(tt.wantReset) {
				t.Fatalf("quotaSnapshotCooldown() reset = %v, want %v", gotReset, tt.wantReset)
			}
		})
	}
}

func TestFillFirstSelectorPick_QuotaSnapshotCooldownFallback(t *testing.T) {
	t.Parallel()

	selector := &FillFirstSelector{}
	reset := time.Now().Add(time.Hour)
	high := quotaGateTestAuth("codex", fmt.Sprintf(`{
		"rate_limit": {
			"allowed": false,
			"limit_reached": true,
			"primary_window": {"used_percent": 100, "reset_at": %d}
		}
	}`, reset.Unix()))
	high.ID = "high"
	high.Attributes = map[string]string{"priority": "10"}
	low := &Auth{ID: "low", Provider: "codex", Attributes: map[string]string{"priority": "0"}}

	got, err := selector.Pick(context.Background(), "codex", "gpt-5.5", cliproxyexecutor.Options{}, []*Auth{high, low})
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "low" {
		t.Fatalf("Pick() auth.ID = %q, want low", got.ID)
	}
}

func TestRoundRobinSelectorPick_QuotaSnapshotCooldownFallback(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	reset := time.Now().Add(time.Hour)
	blocked := quotaGateTestAuth("claude", fmt.Sprintf(`{
		"seven_day": {"utilization": 100, "resets_at": %q}
	}`, reset.Format(time.RFC3339Nano)))
	blocked.ID = "a"
	available := &Auth{ID: "b", Provider: "claude"}

	got, err := selector.Pick(context.Background(), "claude", "claude-sonnet-4", cliproxyexecutor.Options{}, []*Auth{blocked, available})
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "b" {
		t.Fatalf("Pick() auth.ID = %q, want b", got.ID)
	}
}

func TestSessionAffinitySelectorPick_QuotaSnapshotCooldownReselectsCachedAuth(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelector(&FillFirstSelector{})
	opts := cliproxyexecutor.Options{
		Headers: http.Header{"X-Session-ID": []string{"session-a"}},
	}
	blocked := &Auth{ID: "a", Provider: "codex"}
	available := &Auth{ID: "b", Provider: "codex"}

	first, err := selector.Pick(context.Background(), "codex", "gpt-5.5", opts, []*Auth{blocked, available})
	if err != nil {
		t.Fatalf("Pick() first error = %v", err)
	}
	if first.ID != "a" {
		t.Fatalf("Pick() first auth.ID = %q, want a", first.ID)
	}

	reset := time.Now().Add(time.Hour)
	blocked.Metadata = quotaGateTestAuth("codex", fmt.Sprintf(`{
		"rate_limit": {
			"allowed": false,
			"limit_reached": true,
			"primary_window": {"used_percent": 100, "reset_at": %d}
		}
	}`, reset.Unix())).Metadata

	second, err := selector.Pick(context.Background(), "codex", "gpt-5.5", opts, []*Auth{blocked, available})
	if err != nil {
		t.Fatalf("Pick() second error = %v", err)
	}
	if second.ID != "b" {
		t.Fatalf("Pick() second auth.ID = %q, want b", second.ID)
	}
}

func TestSelectorPick_AllQuotaSnapshotCooldownReturnsModelCooldownError(t *testing.T) {
	t.Parallel()

	selector := &FillFirstSelector{}
	reset := time.Now().Add(time.Hour)
	body := fmt.Sprintf(`{
		"seven_day": {"utilization": 100, "resets_at": %q}
	}`, reset.Format(time.RFC3339Nano))
	auths := []*Auth{
		quotaGateTestAuth("claude", body),
		quotaGateTestAuth("claude", body),
	}
	auths[0].ID = "a"
	auths[1].ID = "b"

	_, err := selector.Pick(context.Background(), "claude", "claude-sonnet-4", cliproxyexecutor.Options{}, auths)
	if err == nil {
		t.Fatalf("Pick() error = nil")
	}
	var cooldown *modelCooldownError
	if !errors.As(err, &cooldown) {
		t.Fatalf("Pick() error = %T, want *modelCooldownError", err)
	}
	if cooldown.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("StatusCode() = %d, want %d", cooldown.StatusCode(), http.StatusTooManyRequests)
	}
}

func quotaGateTestAuth(provider, body string) *Auth {
	return &Auth{
		ID:       provider + "-auth",
		Provider: provider,
		Metadata: map[string]any{
			"quota": map[string]any{
				"status":      "success",
				"status_code": http.StatusOK,
				"body":        body,
				"updated_at":  "2026-05-11T00:00:00Z",
			},
		},
	}
}
