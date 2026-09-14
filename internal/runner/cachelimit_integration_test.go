package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"gitea.com/gitea/runner/act/artifactcache"
	"github.com/sirupsen/logrus"
)

// TestEvictedEntryBecomesACleanMiss is the assumption the whole sweeper rests
// on: that deleting a blob behind act's back produces a clean cache miss rather
// than an error or a dangling download URL.
//
// It is checked against the real server rather than by reading its source,
// because "act deletes its own row when the file is gone" is exactly the kind
// of behaviour that changes in a patch release — and if it ever does, this
// fails loudly instead of the sweeper quietly breaking builds.
func TestEvictedEntryBecomesACleanMiss(t *testing.T) {
	dir := t.TempDir()
	quiet := logrus.New()
	quiet.SetOutput(io.Discard)

	h, err := artifactcache.StartHandler(dir, "127.0.0.1", 0, "", quiet)
	if err != nil {
		t.Fatalf("start cache server: %v", err)
	}
	defer h.Close()
	base := h.ExternalURL() + "/_apis/artifactcache"

	const token = "integration-token"
	defer h.RegisterJob(token, "feed-mob/orrery")()

	call := func(method, url string, body io.Reader) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, url, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, b
	}

	// Save a cache entry the way actions/cache does: reserve, upload, commit.
	const key, version = "orrery-test-key", "v1"
	payload := bytes.Repeat([]byte("x"), 1024)

	res, body := call(http.MethodPost, base+"/caches",
		bytes.NewReader(mustJSON(t, map[string]any{"key": key, "version": version, "cacheSize": len(payload)})))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reserve: %d %s", res.StatusCode, body)
	}
	var reserved struct {
		CacheID uint64 `json:"cacheId"`
	}
	if err := json.Unmarshal(body, &reserved); err != nil {
		t.Fatalf("reserve response %q: %v", body, err)
	}

	up, _ := http.NewRequest(http.MethodPatch,
		fmt.Sprintf("%s/caches/%d", base, reserved.CacheID), bytes.NewReader(payload))
	up.Header.Set("Authorization", "Bearer "+token)
	up.Header.Set("Content-Type", "application/octet-stream")
	up.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/*", len(payload)-1))
	if res, err := http.DefaultClient.Do(up); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("upload: %v %v", err, res)
	}

	if res, body := call(http.MethodPost, fmt.Sprintf("%s/caches/%d", base, reserved.CacheID),
		bytes.NewReader(mustJSON(t, map[string]any{"size": len(payload)}))); res.StatusCode != http.StatusOK {
		t.Fatalf("commit: %d %s", res.StatusCode, body)
	}

	find := base + "/cache?keys=" + key + "&version=" + version
	if res, body := call(http.MethodGet, find, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("the entry we just saved is not a hit: %d %s", res.StatusCode, body)
	}

	// Now evict it exactly as the sweeper does — blob only, index untouched.
	if err := os.Remove(blobPath(dir, reserved.CacheID)); err != nil {
		t.Fatalf("remove blob: %v", err)
	}

	res, body = call(http.MethodGet, find, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("after eviction the lookup returned %d %s; want 204, a clean miss", res.StatusCode, body)
	}

	// And the row is gone, so the index does not grow without bound either.
	entries, total, err := capFor(dir, 1).entries()
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 0 || total != 0 {
		t.Fatalf("act kept %d rows (%d bytes) for a blob it just reported missing", len(entries), total)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
