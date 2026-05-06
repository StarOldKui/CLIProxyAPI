package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	defaultSnapshotFlushInterval = 30 * time.Second
	snapshotFileMode             = 0o600
	snapshotDirMode              = 0o700
)

// SnapshotStore mirrors the usage snapshot to a remote persistence backend.
type SnapshotStore interface {
	LoadUsageSnapshot(ctx context.Context) ([]byte, error)
	SaveUsageSnapshot(ctx context.Context, data []byte) error
}

type snapshotFile struct {
	Version int                `json:"version"`
	SavedAt time.Time          `json:"saved_at"`
	Usage   StatisticsSnapshot `json:"usage"`
}

type snapshotPersistence struct {
	stats         *RequestStatistics
	localPath     string
	store         SnapshotStore
	flushInterval time.Duration
	cancel        context.CancelFunc
	dirty         atomic.Bool
	flushMu       sync.Mutex
}

var (
	persistenceMu sync.Mutex
	persistence   *snapshotPersistence
)

// DefaultSnapshotPath returns the default local usage snapshot path for a config file.
func DefaultSnapshotPath(configFilePath string) string {
	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath == "" {
		return filepath.Join("usage", "usage.json")
	}
	return filepath.Join(filepath.Dir(configFilePath), "usage", "usage.json")
}

// StartSnapshotPersistence loads an existing snapshot and starts batched snapshot flushing.
func StartSnapshotPersistence(ctx context.Context, stats *RequestStatistics, localPath string, store SnapshotStore) error {
	if stats == nil {
		stats = GetRequestStatistics()
	}
	localPath = strings.TrimSpace(localPath)
	if localPath == "" {
		return fmt.Errorf("usage snapshot path is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	persistenceMu.Lock()
	if persistence != nil && persistence.cancel != nil {
		persistence.cancel()
	}
	runCtx, cancel := context.WithCancel(ctx)
	p := &snapshotPersistence{
		stats:         stats,
		localPath:     localPath,
		store:         store,
		flushInterval: defaultSnapshotFlushInterval,
		cancel:        cancel,
	}
	persistence = p
	persistenceMu.Unlock()

	if err := p.load(runCtx); err != nil {
		log.WithError(err).Warn("usage: failed to load persisted snapshot")
	}
	go p.run(runCtx)
	return nil
}

// FlushSnapshotPersistence writes pending usage statistics immediately.
func FlushSnapshotPersistence(ctx context.Context) error {
	persistenceMu.Lock()
	p := persistence
	persistenceMu.Unlock()
	if p == nil {
		return nil
	}
	return p.flush(ctx, false)
}

// StopSnapshotPersistence stops background flushing after one final write.
func StopSnapshotPersistence(ctx context.Context) error {
	persistenceMu.Lock()
	p := persistence
	persistence = nil
	persistenceMu.Unlock()
	if p == nil {
		return nil
	}
	if p.cancel != nil {
		p.cancel()
	}
	return p.flush(ctx, false)
}

func markPersistenceDirty() {
	persistenceMu.Lock()
	p := persistence
	persistenceMu.Unlock()
	if p != nil {
		p.dirty.Store(true)
	}
}

func (p *snapshotPersistence) run(ctx context.Context) {
	ticker := time.NewTicker(p.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.flush(ctx, true); err != nil {
				log.WithError(err).Warn("usage: failed to persist snapshot")
			}
		}
	}
}

func (p *snapshotPersistence) load(ctx context.Context) error {
	data, err := p.loadBytes(ctx)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	snapshot, err := decodeSnapshot(data)
	if err != nil {
		return err
	}
	result := p.stats.MergeSnapshot(snapshot)
	if result.Added > 0 {
		log.Infof("usage: loaded persisted snapshot entries: %d", result.Added)
	}
	p.dirty.Store(false)
	return nil
}

func (p *snapshotPersistence) loadBytes(ctx context.Context) ([]byte, error) {
	if p.store != nil {
		data, err := p.store.LoadUsageSnapshot(ctx)
		if err == nil && len(data) > 0 {
			return data, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return os.ReadFile(p.localPath)
}

func (p *snapshotPersistence) flush(ctx context.Context, onlyDirty bool) error {
	if p == nil || p.stats == nil {
		return nil
	}
	p.flushMu.Lock()
	defer p.flushMu.Unlock()

	wasDirty := p.dirty.Swap(false)
	if onlyDirty && !wasDirty {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	payload := snapshotFile{
		Version: 1,
		SavedAt: time.Now().UTC(),
		Usage:   p.stats.Snapshot(),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		p.dirty.Store(true)
		return fmt.Errorf("marshal usage snapshot: %w", err)
	}
	data = append(data, '\n')
	if err = writeSnapshotFile(p.localPath, data); err != nil {
		p.dirty.Store(true)
		return err
	}
	if p.store != nil {
		if err = p.store.SaveUsageSnapshot(ctx, data); err != nil {
			p.dirty.Store(true)
			return err
		}
	}
	return nil
}

func decodeSnapshot(data []byte) (StatisticsSnapshot, error) {
	var file snapshotFile
	if err := json.Unmarshal(data, &file); err == nil && file.Usage.APIs != nil {
		return file.Usage, nil
	}
	var direct StatisticsSnapshot
	if err := json.Unmarshal(data, &direct); err != nil {
		return StatisticsSnapshot{}, fmt.Errorf("decode usage snapshot: %w", err)
	}
	return direct, nil
}

func writeSnapshotFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), snapshotDirMode); err != nil {
		return fmt.Errorf("prepare usage snapshot directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".usage-*.tmp")
	if err != nil {
		return fmt.Errorf("create usage snapshot temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write usage snapshot temp file: %w", err)
	}
	if err = tmp.Chmod(snapshotFileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod usage snapshot temp file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close usage snapshot temp file: %w", err)
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace usage snapshot: %w", err)
	}
	return nil
}
