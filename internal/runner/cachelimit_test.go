package runner

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/timshannon/bolthold"
	"go.etcd.io/bbolt"

	"gitea.com/gitea/runner/act/artifactcache"
)

// seedCache writes cache records and their blobs the way act's server would,
// so these tests exercise the real on-disk layout rather than a mock of it.
func seedCache(t *testing.T, dir string, entries []artifactcache.Cache) {
	t.Helper()
	db, err := bolthold.Open(filepath.Join(dir, "bolt.db"), 0o644, &bolthold.Options{
		Encoder: json.Marshal,
		Decoder: json.Unmarshal,
		Options: &bbolt.Options{Timeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	for _, e := range entries {
		if err := db.Insert(e.ID, e); err != nil {
			t.Fatalf("insert %d: %v", e.ID, err)
		}
		p := blobPath(dir, e.ID)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, 16), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func capFor(dir string, maxBytes int64) *cacheLimit {
	return &cacheLimit{dir: dir, max: maxBytes, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func blobExists(dir string, id uint64) bool {
	_, err := os.Stat(blobPath(dir, id))
	return err == nil
}

func TestCacheCapEvictsLeastRecentlyUsedFirst(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-24 * time.Hour).Unix()
	// Three 4 MB entries, all outside the grace window. Sizes are equal so the
	// only thing that can decide the order is UsedAt.
	seedCache(t, dir, []artifactcache.Cache{
		{ID: 1, Size: 4 << 20, Complete: true, UsedAt: old - 300, CreatedAt: old},
		{ID: 2, Size: 4 << 20, Complete: true, UsedAt: old - 200, CreatedAt: old},
		{ID: 3, Size: 4 << 20, Complete: true, UsedAt: old - 100, CreatedAt: old},
	})

	capFor(dir, 9<<20).sweep(context.Background(), "test")

	// 12 MB into a 9 MB cap: exactly one has to go, and it must be the oldest.
	if blobExists(dir, 1) {
		t.Error("the least recently used entry survived")
	}
	if !blobExists(dir, 2) || !blobExists(dir, 3) {
		t.Error("a more recently used entry was evicted")
	}
}

func TestCacheCapSparesEntriesAJobMayBeUsing(t *testing.T) {
	// Deleting a blob a running job is about to download turns a size sweep
	// into a failed build. Everything inside the grace window is off limits,
	// even when that leaves the cache over its cap.
	dir := t.TempDir()
	now := time.Now().Unix()
	seedCache(t, dir, []artifactcache.Cache{
		{ID: 1, Size: 8 << 20, Complete: true, UsedAt: now - 60, CreatedAt: now},
		{ID: 2, Size: 8 << 20, Complete: true, UsedAt: now - 30, CreatedAt: now},
	})

	capFor(dir, 1<<20).sweep(context.Background(), "test")

	if !blobExists(dir, 1) || !blobExists(dir, 2) {
		t.Fatal("an entry used in the last few minutes was evicted")
	}
}

func TestCacheCapLeavesTheIndexForActToHeal(t *testing.T) {
	// The sweeper deletes blobs and never database rows: act notices the
	// missing file on lookup, drops its own row and reports a clean miss. A
	// sweeper that wrote to act's schema would be one upgrade away from
	// corrupting it.
	dir := t.TempDir()
	old := time.Now().Add(-24 * time.Hour).Unix()
	seedCache(t, dir, []artifactcache.Cache{
		{ID: 1, Size: 8 << 20, Complete: true, UsedAt: old, CreatedAt: old},
	})

	capFor(dir, 1<<20).sweep(context.Background(), "test")

	if blobExists(dir, 1) {
		t.Fatal("the blob was not evicted")
	}
	entries, _, err := capFor(dir, 1<<20).entries()
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("the sweeper removed %d index rows; it must remove none", 1-len(entries))
	}
}

func TestCacheCapDoesNothingWhenUnderOrDisabled(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-24 * time.Hour).Unix()
	seedCache(t, dir, []artifactcache.Cache{
		{ID: 1, Size: 1 << 20, Complete: true, UsedAt: old, CreatedAt: old},
	})

	capFor(dir, 100<<20).sweep(context.Background(), "under the cap")
	if !blobExists(dir, 1) {
		t.Fatal("evicted while under the cap")
	}
	// max == 0 is act's own behaviour: no ceiling at all.
	capFor(dir, 0).sweep(context.Background(), "disabled")
	if !blobExists(dir, 1) {
		t.Fatal("evicted with the cap disabled")
	}
}

func TestCacheCapOnAnEmptyDirIsNotAnError(t *testing.T) {
	// A runner's first start has no cache and no index. That must be quiet,
	// not a warning on every boot.
	capFor(t.TempDir(), 1<<20).sweep(context.Background(), "first start")
}
