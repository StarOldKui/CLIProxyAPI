package usage

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestSnapshotPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage", "usage.json")
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  7,
			OutputTokens: 11,
			TotalTokens:  18,
		},
	})

	if err := StartSnapshotPersistence(context.Background(), stats, path, nil); err != nil {
		t.Fatalf("start persistence: %v", err)
	}
	if err := FlushSnapshotPersistence(context.Background()); err != nil {
		t.Fatalf("flush persistence: %v", err)
	}
	if err := StopSnapshotPersistence(context.Background()); err != nil {
		t.Fatalf("stop persistence: %v", err)
	}

	loaded := NewRequestStatistics()
	if err := StartSnapshotPersistence(context.Background(), loaded, path, nil); err != nil {
		t.Fatalf("load persistence: %v", err)
	}
	t.Cleanup(func() {
		_ = StopSnapshotPersistence(context.Background())
	})

	snapshot := loaded.Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("total requests = %d, want 1", snapshot.TotalRequests)
	}
	if snapshot.TotalTokens != 18 {
		t.Fatalf("total tokens = %d, want 18", snapshot.TotalTokens)
	}
}

func TestSnapshotPersistenceKeepsDirtyWhenMarkedDuringFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage", "usage.json")
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  1,
			OutputTokens: 1,
			TotalTokens:  2,
		},
	})

	store := &blockingSnapshotStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	p := &snapshotPersistence{
		stats:     stats,
		localPath: path,
		store:     store,
	}
	p.dirty.Store(true)

	errCh := make(chan error, 1)
	go func() {
		errCh <- p.flush(context.Background(), true)
	}()

	<-store.started
	p.dirty.Store(true)
	close(store.release)

	if err := <-errCh; err != nil {
		t.Fatalf("flush: %v", err)
	}
	if !p.dirty.Load() {
		t.Fatal("dirty flag was cleared while a newer write was pending")
	}
}

func TestSnapshotPersistenceSerializesFlushes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage", "usage.json")
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  1,
			OutputTokens: 1,
			TotalTokens:  2,
		},
	})

	store := &serialSnapshotStore{
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	p := &snapshotPersistence{
		stats:     stats,
		localPath: path,
		store:     store,
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- p.flush(context.Background(), false)
	}()
	<-store.started
	go func() {
		errCh <- p.flush(context.Background(), false)
	}()

	select {
	case <-store.started:
		t.Fatal("second flush started before first flush completed")
	case <-time.After(20 * time.Millisecond):
	}

	close(store.release)
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("flush %d: %v", i+1, err)
		}
	}
}

type blockingSnapshotStore struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (s *blockingSnapshotStore) LoadUsageSnapshot(context.Context) ([]byte, error) {
	return nil, nil
}

func (s *blockingSnapshotStore) SaveUsageSnapshot(context.Context, []byte) error {
	s.once.Do(func() {
		close(s.started)
	})
	<-s.release
	return nil
}

type serialSnapshotStore struct {
	started chan struct{}
	release chan struct{}
}

func (s *serialSnapshotStore) LoadUsageSnapshot(context.Context) ([]byte, error) {
	return nil, nil
}

func (s *serialSnapshotStore) SaveUsageSnapshot(context.Context, []byte) error {
	s.started <- struct{}{}
	<-s.release
	return nil
}
