package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gitea.com/gitea/runner/act/artifactcache"
	"gitea.com/gitea/runner/act/artifacts"
	"gitea.com/gitea/runner/act/common"
	"github.com/sirupsen/logrus"
)

// services are the two HTTP servers a job's own steps talk back to:
// `actions/upload-artifact` and `actions/cache`.
//
// They run in the runner process and bind to an address the job container can
// route to. That is act's design and Gitea's deployment, and it carries one
// consequence worth stating plainly: artifacts and caches are local to the
// runner that produced them. With one runner that is invisible. With several,
// a download-artifact in a downstream job only finds the upload if the same
// runner happened to take both jobs. A central store is the fix, and it is not
// this change.
type services struct {
	artifactDir  string
	artifactAddr string
	artifactPort string
	cache        *artifactcache.Handler
	// token is what steps present to the cache server. One per runner process,
	// registered against the job's repository for as long as the job runs, so a
	// leaked token stops working when the job ends.
	token string
	stop  []func()
}

// startServices brings up whichever of the two the operator asked for.
func startServices(ctx context.Context, opts Options, log *slog.Logger) (*services, error) {
	s := &services{token: newToken()}

	ip := opts.ServiceAddr
	if ip == "" {
		addr := common.GetOutboundIP()
		if addr == nil {
			// Loopback still works for host-mode jobs; a container job would
			// not reach it, and the error it produces then names this line.
			log.Warn("cannot determine an outbound IP; artifact and cache servers will only be reachable from the host")
			ip = "127.0.0.1"
		} else {
			ip = addr.String()
		}
	}

	if !opts.NoArtifacts {
		s.artifactDir = filepath.Join(opts.WorkDir, "artifacts")
		if err := os.MkdirAll(s.artifactDir, 0o755); err != nil {
			return nil, fmt.Errorf("artifact dir: %w", err)
		}
		s.artifactAddr = ip
		s.artifactPort = strconv.Itoa(opts.ArtifactPort)
		cancel := artifacts.Serve(ctx, s.artifactDir, s.artifactAddr, s.artifactPort)
		s.stop = append(s.stop, func() { cancel() })
		// act reads this from the process environment before falling back to
		// its own config, and it is also what steps send as their bearer token.
		if os.Getenv("ACTIONS_RUNTIME_TOKEN") == "" {
			_ = os.Setenv("ACTIONS_RUNTIME_TOKEN", s.token)
		} else {
			s.token = os.Getenv("ACTIONS_RUNTIME_TOKEN")
		}
		log.Info("artifact server listening",
			"url", fmt.Sprintf("http://%s:%s/", s.artifactAddr, s.artifactPort), "dir", s.artifactDir)
	}

	if !opts.NoCache {
		dir := filepath.Join(opts.WorkDir, "cache")
		h, err := artifactcache.StartHandler(dir, ip, uint16(opts.CachePort), "", logrusFor(log))
		if err != nil {
			return nil, fmt.Errorf("cache server: %w", err)
		}
		s.cache = h
		s.stop = append(s.stop, func() { _ = h.Close() })
		log.Info("cache server listening", "url", h.ExternalURL(), "dir", dir)
	}
	return s, nil
}

// beginJob registers this job's cache credential and returns the environment
// its steps need. The returned func revokes the credential.
func (s *services) beginJob(repo string) (map[string]string, func()) {
	env := map[string]string{}
	if s == nil {
		return env, func() {}
	}
	if s.cache != nil {
		// The trailing slash is load-bearing: actions/cache concatenates this
		// with `_apis/artifactcache/…`, so without it every request goes to
		// `…:56200_apis/…` and the step reports a cache miss it can never fix.
		env["ACTIONS_CACHE_URL"] = strings.TrimSuffix(s.cache.ExternalURL(), "/") + "/"
		env["ACTIONS_RUNTIME_TOKEN"] = s.token
		// ACTIONS_RESULTS_URL is deliberately not set. It advertises the newer
		// twirp cache and artifact services, which this server does not
		// implement; setting it would turn a clear "pin to v3" into an action
		// that fails halfway through.
		return env, s.cache.RegisterJob(s.token, repo)
	}
	return env, func() {}
}

func (s *services) close() {
	if s == nil {
		return
	}
	for _, fn := range s.stop {
		fn()
	}
}

func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// logrusFor bridges act's logger to ours at a level that keeps the cache
// server's per-request chatter out of the runner's log.
func logrusFor(log *slog.Logger) logrus.FieldLogger {
	l := logrus.New()
	l.SetLevel(logrus.WarnLevel)
	l.SetOutput(slogWriter{log})
	return l
}

type slogWriter struct{ log *slog.Logger }

func (w slogWriter) Write(p []byte) (int, error) {
	w.log.Warn("artifactcache", "msg", string(p))
	return len(p), nil
}

// The three accessors below exist so actExecutor can stay usable with no
// services at all — a unit test, or a runner started with both switched off.
func (s *services) artifactPathOr(def string) string {
	if s == nil {
		return def
	}
	return s.artifactDir
}

func (s *services) artifactAddrOr(def string) string {
	if s == nil {
		return def
	}
	return s.artifactAddr
}

func (s *services) artifactPortOr(def string) string {
	if s == nil {
		return def
	}
	return s.artifactPort
}
