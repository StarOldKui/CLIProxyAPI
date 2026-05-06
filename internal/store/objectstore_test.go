package store

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestUsageSnapshotTimestampUsesSavedAt(t *testing.T) {
	savedAt := time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
	fallback := savedAt.Add(-time.Hour)

	got := usageSnapshotTimestamp([]byte(`{"saved_at":"2026-05-06T10:00:00Z"}`), fallback)

	if !got.Equal(savedAt) {
		t.Fatalf("timestamp = %v, want %v", got, savedAt)
	}
}

func TestLocalUsageSnapshotNewerUsesSavedAtBeforeMtime(t *testing.T) {
	localData := []byte(`{"saved_at":"2026-05-06T10:00:00Z"}`)
	remoteData := []byte(`{"saved_at":"2026-05-06T09:00:00Z"}`)
	localModified := time.Date(2026, 5, 6, 8, 0, 0, 0, time.UTC)
	remoteModified := time.Date(2026, 5, 6, 11, 0, 0, 0, time.UTC)

	if !localUsageSnapshotNewer(localData, localModified, remoteData, remoteModified) {
		t.Fatal("expected local snapshot to win by saved_at")
	}
}

func TestLocalUsageSnapshotNewerFallsBackToMtime(t *testing.T) {
	localModified := time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
	remoteModified := time.Date(2026, 5, 6, 9, 0, 0, 0, time.UTC)

	if !localUsageSnapshotNewer([]byte(`{}`), localModified, []byte(`{}`), remoteModified) {
		t.Fatal("expected local snapshot to win by mtime fallback")
	}
}

func TestObjectTokenStoreLoadUsageSnapshotPrefersNewerLocalMirror(t *testing.T) {
	localData := []byte(`{"saved_at":"2026-05-06T10:00:00Z","usage":{"apis":{}}}`)
	remoteData := []byte(`{"saved_at":"2026-05-06T09:00:00Z","usage":{"apis":{}}}`)
	store := newObjectStoreSnapshotTestStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			t.Fatalf("unexpected remote GET for newer local mirror")
		}
		writeObjectStoreSnapshotHeaders(w, remoteData, time.Date(2026, 5, 6, 9, 0, 0, 0, time.UTC))
		w.WriteHeader(http.StatusOK)
	})
	if err := writeUsageSnapshotMirror(store.UsageSnapshotPath(), localData); err != nil {
		t.Fatalf("write local mirror: %v", err)
	}

	got, err := store.LoadUsageSnapshot(context.Background())
	if err != nil {
		t.Fatalf("load usage snapshot: %v", err)
	}
	if string(got) != string(localData) {
		t.Fatalf("snapshot = %s, want local %s", got, localData)
	}
}

func TestObjectTokenStoreLoadUsageSnapshotRefreshesLocalMirrorWhenRemoteIsNewer(t *testing.T) {
	localData := []byte(`{"saved_at":"2026-05-06T09:00:00Z","usage":{"apis":{}}}`)
	remoteData := []byte(`{"saved_at":"2026-05-06T10:00:00Z","usage":{"apis":{}}}`)
	store := newObjectStoreSnapshotTestStore(t, func(w http.ResponseWriter, r *http.Request) {
		writeObjectStoreSnapshotHeaders(w, remoteData, time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC))
		if r.Method == http.MethodGet {
			_, _ = w.Write(remoteData)
		}
	})
	if err := writeUsageSnapshotMirror(store.UsageSnapshotPath(), localData); err != nil {
		t.Fatalf("write local mirror: %v", err)
	}

	got, err := store.LoadUsageSnapshot(context.Background())
	if err != nil {
		t.Fatalf("load usage snapshot: %v", err)
	}
	if string(got) != string(remoteData) {
		t.Fatalf("snapshot = %s, want remote %s", got, remoteData)
	}
	mirror, err := os.ReadFile(store.UsageSnapshotPath())
	if err != nil {
		t.Fatalf("read local mirror: %v", err)
	}
	if string(mirror) != string(remoteData) {
		t.Fatalf("mirror = %s, want remote %s", mirror, remoteData)
	}
}

func TestObjectTokenStoreLoadUsageSnapshotFallsBackToLocalMirrorOnRemoteStatError(t *testing.T) {
	localData := []byte(`{"saved_at":"2026-05-06T10:00:00Z","usage":{"apis":{}}}`)
	store := newObjectStoreSnapshotTestStore(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
	})
	if err := writeUsageSnapshotMirror(store.UsageSnapshotPath(), localData); err != nil {
		t.Fatalf("write local mirror: %v", err)
	}

	got, err := store.LoadUsageSnapshot(context.Background())
	if err != nil {
		t.Fatalf("load usage snapshot: %v", err)
	}
	if string(got) != string(localData) {
		t.Fatalf("snapshot = %s, want local %s", got, localData)
	}
}

func newObjectStoreSnapshotTestStore(t *testing.T, handler http.HandlerFunc) *ObjectTokenStore {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Query().Has("location") {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`))
			return
		}
		if r.URL.Path != "/bucket/usage/usage.json" {
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	client, err := minio.New(parsed.Host, &minio.Options{
		Creds:        credentials.NewStaticV4("access", "secret", ""),
		Secure:       false,
		Region:       "us-east-1",
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatalf("new minio client: %v", err)
	}
	root := filepath.Join(t.TempDir(), "objectstore")
	return &ObjectTokenStore{
		client:    client,
		cfg:       ObjectStoreConfig{Bucket: "bucket"},
		spoolRoot: root,
	}
}

func writeObjectStoreSnapshotHeaders(w http.ResponseWriter, data []byte, modified time.Time) {
	w.Header().Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("ETag", `"test-etag"`)
	w.Header().Set("Content-Type", "application/json")
}
