package cliproxy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestServiceStartUsageRuntimeWiresStatisticsAndQueue(t *testing.T) {
	prevStore := sdkAuth.GetTokenStore()
	prevQueueEnabled := redisqueue.UsageStatisticsEnabled()
	prevRetention := redisqueue.RetentionSeconds()
	prevStatsEnabled := internalusage.StatisticsEnabled()
	t.Cleanup(func() {
		sdkAuth.RegisterTokenStore(prevStore)
		redisqueue.SetUsageStatisticsEnabled(prevQueueEnabled)
		redisqueue.SetRetentionSeconds(prevRetention)
		internalusage.SetStatisticsEnabled(prevStatsEnabled)
		_ = internalusage.StopSnapshotPersistence(context.Background())
	})

	sdkAuth.RegisterTokenStore(sdkAuth.NewFileTokenStore())
	service := &Service{
		cfg: &config.Config{
			UsageStatisticsEnabled:          true,
			RedisUsageQueueRetentionSeconds: 123,
		},
		configPath: filepath.Join(t.TempDir(), "config.yaml"),
	}

	service.startUsageRuntime(context.Background())

	if !internalusage.StatisticsEnabled() {
		t.Fatal("usage statistics disabled, want enabled")
	}
	if !redisqueue.UsageStatisticsEnabled() {
		t.Fatal("redis usage queue statistics disabled, want enabled")
	}
	if got := redisqueue.RetentionSeconds(); got != 123 {
		t.Fatalf("retention seconds = %d, want 123", got)
	}
}
