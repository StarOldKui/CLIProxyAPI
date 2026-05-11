package auth

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const quotaSnapshotMetadataKey = "quota"
const quotaSnapshotFallbackCooldown = 5 * time.Minute

func quotaSnapshotCooldown(auth *Auth, model string, now time.Time) (time.Time, bool) {
	if auth == nil || len(auth.Metadata) == 0 {
		return time.Time{}, false
	}
	raw, ok := auth.Metadata[quotaSnapshotMetadataKey]
	if !ok || raw == nil {
		return time.Time{}, false
	}

	snapshot, ok := quotaSnapshotBytes(raw)
	if !ok {
		return time.Time{}, false
	}
	status := strings.TrimSpace(gjson.GetBytes(snapshot, "status").String())
	if !strings.EqualFold(status, "success") {
		return time.Time{}, false
	}
	body, ok := quotaSnapshotBody(snapshot)
	if !ok {
		return time.Time{}, false
	}

	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "antigravity":
		return antigravityQuotaSnapshotCooldown(body, model, now)
	case "claude":
		return claudeQuotaSnapshotCooldown(body, model, now)
	case "codex":
		return codexQuotaSnapshotCooldown(body, now)
	case "gemini-cli":
		return geminiCliQuotaSnapshotCooldown(body, model, now)
	case "kimi":
		return kimiQuotaSnapshotCooldown(body, now)
	default:
		return time.Time{}, false
	}
}

func quotaSnapshotBytes(raw any) ([]byte, bool) {
	switch value := raw.(type) {
	case json.RawMessage:
		return []byte(value), len(value) > 0
	case []byte:
		return value, len(value) > 0
	case string:
		trimmed := strings.TrimSpace(value)
		return []byte(trimmed), trimmed != ""
	default:
		data, err := json.Marshal(raw)
		if err != nil || len(data) == 0 {
			return nil, false
		}
		return data, true
	}
}

func quotaSnapshotBody(snapshot []byte) ([]byte, bool) {
	body := gjson.GetBytes(snapshot, "body")
	if !body.Exists() {
		return nil, false
	}
	if body.IsObject() || body.IsArray() {
		return []byte(body.Raw), true
	}
	trimmed := strings.TrimSpace(body.String())
	if trimmed == "" || !gjson.Valid(trimmed) {
		return nil, false
	}
	return []byte(trimmed), true
}

func claudeQuotaSnapshotCooldown(body []byte, model string, now time.Time) (time.Time, bool) {
	root := gjson.ParseBytes(body)
	exhausted := false
	var resets []time.Time
	for _, key := range claudeQuotaWindowKeys(model) {
		window := root.Get(key)
		if !window.Exists() {
			continue
		}
		usage, ok := quotaNumber(window.Get("utilization"))
		if !ok || usage < 100 {
			continue
		}
		exhausted = true
		reset, ok := quotaTime(window.Get("resets_at"), now)
		if ok {
			resets = append(resets, reset)
		}
	}
	return quotaSnapshotBlock(exhausted, resets, now)
}

func codexQuotaSnapshotCooldown(body []byte, now time.Time) (time.Time, bool) {
	root := gjson.ParseBytes(body)

	rateLimitForce := strings.EqualFold(
		strings.TrimSpace(quotaField(root, "rate_limit_reached_type.type", "rateLimitReachedType.type").String()),
		"rate_limit_reached",
	)
	return codexRateLimitCooldown(quotaField(root, "rate_limit", "rateLimit"), rateLimitForce, now)
}

func codexRateLimitCooldown(limit gjson.Result, force bool, now time.Time) (time.Time, bool) {
	if !limit.Exists() || !limit.IsObject() {
		return time.Time{}, false
	}
	limitReached := force
	if value, ok := quotaBool(quotaField(limit, "limit_reached", "limitReached")); ok && value {
		limitReached = true
	}
	if value, ok := quotaBool(quotaField(limit, "allowed")); ok && !value {
		limitReached = true
	}
	var exhaustedResets []time.Time
	var fallbackResets []time.Time
	for _, window := range []gjson.Result{
		quotaField(limit, "primary_window", "primaryWindow"),
		quotaField(limit, "secondary_window", "secondaryWindow"),
	} {
		if !window.Exists() {
			continue
		}
		reset, hasReset := codexWindowReset(window, now)
		if hasReset {
			fallbackResets = append(fallbackResets, reset)
		}
		if used, ok := quotaNumber(quotaField(window, "used_percent", "usedPercent")); ok && used >= 100 {
			limitReached = true
			if hasReset {
				exhaustedResets = append(exhaustedResets, reset)
			}
		}
	}

	if !limitReached {
		return time.Time{}, false
	}
	if reset, ok := latestQuotaReset(exhaustedResets); ok {
		return quotaSnapshotRetryAt(reset, now), true
	}
	if reset, ok := latestQuotaReset(fallbackResets); ok {
		return quotaSnapshotRetryAt(reset, now), true
	}
	return quotaSnapshotFallbackRetryAt(now), true
}

func codexWindowReset(window gjson.Result, now time.Time) (time.Time, bool) {
	if reset, ok := quotaTime(quotaField(window, "reset_at", "resetAt"), now); ok {
		return reset, true
	}
	if seconds, ok := quotaNumber(quotaField(window, "reset_after_seconds", "resetAfterSeconds")); ok && seconds > 0 {
		return now.Add(time.Duration(seconds * float64(time.Second))), true
	}
	return time.Time{}, false
}

func claudeQuotaWindowKeys(model string) []string {
	keys := []string{
		"five_hour",
		"seven_day",
		"seven_day_oauth_apps",
		"seven_day_cowork",
		"iguana_necktie",
	}
	normalized := strings.ToLower(canonicalModelKey(model))
	if strings.Contains(normalized, "opus") {
		keys = append(keys, "seven_day_opus")
	}
	if strings.Contains(normalized, "sonnet") {
		keys = append(keys, "seven_day_sonnet")
	}
	return keys
}

func antigravityQuotaSnapshotCooldown(body []byte, model string, now time.Time) (time.Time, bool) {
	models := gjson.ParseBytes(body).Get("models")
	if !models.IsObject() {
		return time.Time{}, false
	}
	exhausted := false
	var resets []time.Time
	models.ForEach(func(key, item gjson.Result) bool {
		if !quotaMatchesModel(key.String(), model) {
			return true
		}
		quota := quotaField(item, "quotaInfo", "quota_info")
		if !quota.Exists() {
			return true
		}
		if !quotaRemainingDepleted(quotaField(quota, "remainingFraction", "remaining_fraction", "remaining")) {
			return true
		}
		exhausted = true
		if reset, ok := quotaTime(quotaField(quota, "resetTime", "reset_time"), now); ok {
			resets = append(resets, reset)
		}
		return true
	})
	return quotaSnapshotBlock(exhausted, resets, now)
}

func quotaMatchesModel(quotaModel, requestedModel string) bool {
	requested := strings.ToLower(canonicalModelKey(requestedModel))
	quota := strings.ToLower(strings.TrimSpace(quotaModel))
	if requested == "" || quota == "" {
		return true
	}
	return quota == requested || strings.Contains(quota, requested) || strings.Contains(requested, quota)
}

func geminiCliQuotaSnapshotCooldown(body []byte, model string, now time.Time) (time.Time, bool) {
	buckets := gjson.ParseBytes(body).Get("buckets")
	if !buckets.IsArray() {
		return time.Time{}, false
	}
	exhausted := false
	var resets []time.Time
	buckets.ForEach(func(_, bucket gjson.Result) bool {
		bucketModel := quotaField(bucket, "model", "model_name", "modelName", "id").String()
		if bucketModel != "" && !quotaMatchesModel(bucketModel, model) {
			return true
		}
		remainingFraction := quotaRemainingDepleted(quotaField(bucket, "remainingFraction", "remaining_fraction"))
		remainingAmount := quotaRemainingDepleted(quotaField(bucket, "remainingAmount", "remaining_amount"))
		if !remainingFraction && !remainingAmount {
			return true
		}
		exhausted = true
		if reset, ok := quotaTime(quotaField(bucket, "resetTime", "reset_time"), now); ok {
			resets = append(resets, reset)
		}
		return true
	})
	return quotaSnapshotBlock(exhausted, resets, now)
}

func kimiQuotaSnapshotCooldown(body []byte, now time.Time) (time.Time, bool) {
	root := gjson.ParseBytes(body)
	var resets []time.Time
	exhausted := false
	if reset, ok := kimiQuotaItemCooldown(root.Get("usage"), gjson.Result{}, now); ok {
		exhausted = true
		resets = append(resets, reset)
	}
	limits := root.Get("limits")
	if limits.IsArray() {
		limits.ForEach(func(_, item gjson.Result) bool {
			detail := item.Get("detail")
			if !detail.IsObject() {
				detail = item
			}
			if reset, ok := kimiQuotaItemCooldown(detail, item, now); ok {
				exhausted = true
				resets = append(resets, reset)
			}
			return true
		})
	}
	return quotaSnapshotBlock(exhausted, resets, now)
}

func kimiQuotaItemCooldown(item, fallback gjson.Result, now time.Time) (time.Time, bool) {
	if !item.Exists() || !item.IsObject() {
		return time.Time{}, false
	}
	depleted := false
	if remaining, ok := quotaNumber(item.Get("remaining")); ok && remaining <= 0 {
		depleted = true
	}
	used, hasUsed := quotaNumber(item.Get("used"))
	limit, hasLimit := quotaNumber(item.Get("limit"))
	if hasUsed && hasLimit && limit > 0 && used >= limit {
		depleted = true
	}
	if !depleted {
		return time.Time{}, false
	}
	if reset, ok := kimiQuotaReset(item, now); ok {
		return reset, true
	}
	if reset, ok := kimiQuotaReset(fallback, now); ok {
		return reset, true
	}
	return quotaSnapshotFallbackRetryAt(now), true
}

func kimiQuotaReset(item gjson.Result, now time.Time) (time.Time, bool) {
	if !item.Exists() {
		return time.Time{}, false
	}
	if reset, ok := quotaTime(quotaField(item, "reset_at", "resetAt", "reset_time", "resetTime"), now); ok {
		return reset, true
	}
	if seconds, ok := quotaNumber(quotaField(item, "reset_in", "resetIn", "ttl")); ok && seconds > 0 {
		return now.Add(time.Duration(seconds * float64(time.Second))), true
	}
	return time.Time{}, false
}

func quotaField(value gjson.Result, paths ...string) gjson.Result {
	for _, path := range paths {
		result := value.Get(path)
		if result.Exists() {
			return result
		}
	}
	return gjson.Result{}
}

func quotaRemainingDepleted(value gjson.Result) bool {
	remaining, ok := quotaNumber(value)
	return ok && remaining <= 0
}

func latestQuotaReset(resets []time.Time) (time.Time, bool) {
	var latest time.Time
	for _, reset := range resets {
		if reset.IsZero() {
			continue
		}
		if latest.IsZero() || reset.After(latest) {
			latest = reset
		}
	}
	return latest, !latest.IsZero()
}

func quotaSnapshotBlock(exhausted bool, resets []time.Time, now time.Time) (time.Time, bool) {
	if !exhausted {
		return time.Time{}, false
	}
	if reset, ok := latestQuotaReset(resets); ok {
		return quotaSnapshotRetryAt(reset, now), true
	}
	return quotaSnapshotFallbackRetryAt(now), true
}

func quotaSnapshotRetryAt(reset, now time.Time) time.Time {
	if reset.After(now) {
		return reset
	}
	return quotaSnapshotFallbackRetryAt(now)
}

func quotaSnapshotFallbackRetryAt(now time.Time) time.Time {
	return now.Add(quotaSnapshotFallbackCooldown)
}

func quotaBool(value gjson.Result) (bool, bool) {
	if !value.Exists() {
		return false, false
	}
	if value.Type == gjson.True {
		return true, true
	}
	if value.Type == gjson.False {
		return false, true
	}
	if value.Type == gjson.String {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value.String()))
		if err == nil {
			return parsed, true
		}
	}
	return false, false
}

func quotaNumber(value gjson.Result) (float64, bool) {
	if !value.Exists() {
		return 0, false
	}
	if value.Type == gjson.Number {
		return value.Float(), true
	}
	if value.Type == gjson.String {
		raw := strings.TrimSpace(value.String())
		raw = strings.TrimSuffix(raw, "%")
		parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func quotaTime(value gjson.Result, now time.Time) (time.Time, bool) {
	if !value.Exists() {
		return time.Time{}, false
	}
	if value.Type == gjson.Number {
		seconds := value.Int()
		if seconds <= 0 {
			return time.Time{}, false
		}
		if seconds > 1_000_000_000_000 {
			return time.UnixMilli(seconds), true
		}
		return time.Unix(seconds, 0), true
	}
	raw := strings.TrimSpace(value.String())
	if raw == "" {
		return time.Time{}, false
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
		if seconds > 1_000_000_000_000 {
			return time.UnixMilli(seconds), true
		}
		return time.Unix(seconds, 0), true
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, true
	}
	if duration, err := time.ParseDuration(raw); err == nil && duration > 0 {
		return now.Add(duration), true
	}
	return time.Time{}, false
}
