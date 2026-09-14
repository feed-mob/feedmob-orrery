package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/timshannon/bolthold"
	"go.etcd.io/bbolt"

	"gitea.com/gitea/runner/act/artifactcache"
)

// cacheLimit caps the cache directory's size.
//
// act's cache server evicts on time alone — 7 days unused, 30 days since
// creation (act/artifactcache/handler.go:731) — and never on total size. On
// GitHub that gap does not exist: the hosted cache is capped at 10 GB per
// repository and evicts least-recently-used above it, so a workflow can ask for
// `mode=max` layer caching and never think about storage.
//
// Move the same workflow to a self-hosted runner and the ceiling disappears.
// The steady state becomes "everything cached in the last 30 days", which for a
// buildx `mode=max` build is tens of gigabytes that only grow. A runner sharing
// a disk with anything else fills it, and what fails is not the build — it is
// whatever else was writing to that disk.
//
// This is the same shape as the finding that started this project: twelve of
// thirteen workflows had no `timeout-minutes` not because anyone forgot, but
// because the platform's default let them. A cache with no ceiling is that
// again, so the ceiling belongs here rather than in every workflow file.
type cacheLimit struct {
	dir string // the directory handed to artifactcache.StartHandler
	max int64  // bytes; zero disables the cap
	log *slog.Logger
}

// graceWindow is how recently an entry must have been used to be spared. A job
// that is running right now has just touched the entries it is about to
// download; deleting one mid-restore turns a size sweep into a failed build.
const graceWindow = 10 * time.Minute

// sweep deletes least-recently-used entries until the total fits under the cap.
//
// Only blob files are removed, never database rows: act checks the file's
// existence on lookup and, finding it gone, deletes its own row and reports a
// clean miss (act/artifactcache/handler.go:347-353). Letting it discover the
// deletion is safer than writing to a database whose schema is not ours.
func (c *cacheLimit) sweep(ctx context.Context, reason string) {
	if c == nil || c.max <= 0 {
		return
	}
	entries, total, err := c.entries()
	if err != nil {
		// Not fatal. A cache that cannot be measured is a cache that grows,
		// which is worse than it sounds but still better than a runner that
		// refuses to take work.
		c.log.Warn("cannot measure the cache; size cap not enforced", "err", err, "reason", reason)
		return
	}
	if total <= c.max {
		return
	}

	// Least recently used first, which is what GitHub's own cache does. Sorting
	// by write time instead would evict a small, hot entry that happens to be
	// old — the opposite of what a cache is for.
	sort.Slice(entries, func(i, j int) bool { return entries[i].UsedAt < entries[j].UsedAt })

	cutoff := time.Now().Add(-graceWindow).Unix()
	freed, removed := int64(0), 0
	for _, e := range entries {
		if total-freed <= c.max {
			break
		}
		if ctx.Err() != nil {
			// Shutting down. A partial trim is fine — the next sweep resumes
			// from wherever this one stopped.
			break
		}
		if e.UsedAt > cutoff {
			// Everything from here on is newer still. Stop rather than skip:
			// if the recent entries alone exceed the cap, the cap is too small
			// for the workload and deleting live entries will not fix that.
			break
		}
		if err := os.Remove(blobPath(c.dir, e.ID)); err != nil && !os.IsNotExist(err) {
			continue
		}
		freed += e.Size
		removed++
	}
	if removed == 0 {
		c.log.Warn("cache is over its cap but everything in it is in use",
			"reason", reason, "size_mb", total>>20, "cap_mb", c.max>>20)
		return
	}
	c.log.Info("cache trimmed to its cap", "reason", reason,
		"removed", removed, "freed_mb", freed>>20,
		"size_mb", (total-freed)>>20, "cap_mb", c.max>>20)
}

// entries reads every complete cache record and the total they occupy.
func (c *cacheLimit) entries() ([]artifactcache.Cache, int64, error) {
	path := filepath.Join(c.dir, "bolt.db")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, 0, nil // nothing cached yet
	}
	// The same options act opens with, because both processes share this file
	// and bbolt takes an exclusive lock. act opens and closes per request with
	// a 5s timeout, so this waits at most one request — and sweeps run between
	// tasks, when nothing is holding it.
	db, err := bolthold.Open(path, 0o644, &bolthold.Options{
		Encoder: json.Marshal,
		Decoder: json.Unmarshal,
		Options: &bbolt.Options{Timeout: 5 * time.Second},
	})
	if err != nil {
		return nil, 0, fmt.Errorf("open cache index: %w", err)
	}
	defer db.Close()

	var all []artifactcache.Cache
	if err := db.Find(&all, bolthold.Where("Complete").Eq(true)); err != nil {
		return nil, 0, fmt.Errorf("read cache index: %w", err)
	}
	var total int64
	for _, e := range all {
		total += e.Size
	}
	return all, total, nil
}

// blobPath mirrors act's on-disk layout (act/artifactcache/storage.go:110).
// The modulus is 0xff and not 0x100 — copied deliberately rather than
// corrected, because the two must agree and act's is the one that wrote them.
func blobPath(dir string, id uint64) string {
	return filepath.Join(dir, "cache",
		fmt.Sprintf("%02x", id%0xff), strconv.FormatUint(id, 10))
}
