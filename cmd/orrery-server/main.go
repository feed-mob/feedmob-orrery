// Command orrery-server is Orrery's control plane.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gitea.com/gitea/runner/act/common"
	"github.com/feed-mob/feedmob-orrery/internal/gate"

	"github.com/feed-mob/feedmob-orrery/internal/server"
	"github.com/feed-mob/feedmob-orrery/internal/store"
)

func main() {
	var (
		addr        = flag.String("addr", ":8080", "listen address")
		dbPath      = flag.String("db", "orrery.db", "path to the sqlite database")
		regToken    = flag.String("registration-token", os.Getenv("ORRERY_REGISTRATION_TOKEN"), "shared secret a runner presents once to register")
		grace       = flag.Duration("stop-grace", 30*time.Second, "how long a runner has to acknowledge a stop before it is force-terminated")
		secretsFile = flag.String("secrets", "", "optional KEY=VALUE file injected into every task; values are never persisted")
		secretsDir  = flag.String("secrets-dir", "",
			"directory of <environment>.env files overlaid on -secrets for jobs declaring that `environment:`; a staging deploy should not hold production's credentials")
		forgeURL   = flag.String("forge-url", "https://github.com", "where the code being built lives; becomes github.server_url, which actions/checkout clones from")
		forgeAPI   = flag.String("forge-api-url", "", "forge REST API root; derived from -forge-url when empty (api.github.com for github.com, else <forge>/api/v3 — a Gitea forge must set /api/v1 here)")
		forgeToken = flag.String("forge-token", os.Getenv("ORRERY_FORGE_TOKEN"), "token used to read workflow files and write commit statuses; without it webhooks cannot read a private repo and results are not reported back")
		hookSecret = flag.String("webhook-secret", os.Getenv("ORRERY_WEBHOOK_SECRET"), "shared secret GitHub signs webhook deliveries with; empty disables the webhook endpoint")
		publicURL  = flag.String("public-url", os.Getenv("ORRERY_PUBLIC_URL"), "where humans reach this server; used as the target of commit statuses")
		apiToken   = flag.String("api-token", os.Getenv("ORRERY_API_TOKEN"),
			"token every human-facing API call and the dashboard must present")
		noAuth = flag.Bool("insecure-no-auth", false,
			"serve the API and dashboard with no authentication; anyone who can reach the port can run arbitrary workflows with this server's secrets")
		allowedActions = flag.String("allowed-actions", os.Getenv("ORRERY_ALLOWED_ACTIONS"),
			"comma-separated `uses:` patterns a workflow may pull in (actions/*, feed-mob/*, or an exact owner/repo@ref); empty allows everything")
		requireSHA = flag.Bool("require-action-sha", false,
			"refuse a `uses:` pinned to a tag or branch; a tag is a name someone else can move (tj-actions, 2025-03)")
		rateLimit = flag.Int("rate-limit", 60,
			"most webhook deliveries one repository may turn into runs per -rate-window; 0 disables the cap")
		rateWindow = flag.Duration("rate-window", time.Minute, "the window -rate-limit counts over")
		retention  = flag.Duration("retention", 30*24*time.Hour,
			"how long a finished run and its logs are kept; 0 keeps everything")
		repos = flag.String("repos", os.Getenv("ORRERY_REPOS"),
			"comma-separated owner/repo this server will build; empty means any repository a signed webhook names")
		notifyHook = flag.String("notify-webhook", os.Getenv("ORRERY_NOTIFY_WEBHOOK"),
			"chat webhook that receives a message when a workflow's verdict changes (Slack-shaped {\"text\"}); only changes are sent, not every run")
		defaultConc = flag.String("default-concurrency", "${{ github.workflow }}@${{ github.ref }}",
			"concurrency group applied to a workflow that declares none; runs sharing a group queue rather than race. Empty restores GitHub's behaviour of no limit")
		artifactDir = flag.String("artifact-dir", "",
			"directory for the shared artifact store; empty leaves each runner storing artifacts on its own disk, "+
				"which only works while there is one runner")
		artifactAddr = flag.String("artifact-addr", ":34567",
			"listen address for the shared artifact store; job containers must be able to route to it")
		artifactHost = flag.String("artifact-host", os.Getenv("ORRERY_ARTIFACT_HOST"),
			"host:port job containers reach the artifact store on; derived from -artifact-addr when empty, "+
				"which is wrong whenever this server is behind NAT or in a container itself")

		verbose = flag.Bool("v", false, "debug logging")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if *apiToken == "" && !*noAuth {
		// Refusing to start is the point. POST /api/runs takes a workflow file
		// and runs it on a runner with whatever secrets this server injects,
		// so an unauthenticated port is remote code execution — and that is
		// not a thing anyone should be able to switch on by forgetting a flag.
		log.Error("refusing to start without authentication: set -api-token (or ORRERY_API_TOKEN), " +
			"or pass -insecure-no-auth if this really is a throwaway local instance")
		os.Exit(2)
	}
	if *noAuth {
		log.Warn("running with NO authentication: anyone who can reach this port can run arbitrary " +
			"workflows with this server's secrets")
	}
	if *regToken == "" {
		log.Error("a registration token is required; set -registration-token or ORRERY_REGISTRATION_TOKEN")
		os.Exit(2)
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// Teach the scheduler what `if:` means. Without it every dependent of a
	// failed job is skipped, so `if: always()` and `if: failure()` jobs — the
	// ones that exist precisely to run after a failure — never run.
	vars := envWithPrefix("ORRERY_VAR_")
	st.UseJobGate(func(run *store.Run, payload, key string, upstream map[string]string, runCancelled bool) (bool, error) {
		return gate.Decide(payload, key, upstream, gate.Env{
			Github: gate.GithubFor(run.Repo, run.Ref, run.SHA, run.Actor, run.Event,
				run.WorkflowName, strconv.FormatInt(run.ID, 10),
				strconv.FormatInt(run.RunNumber, 10), run.EventPayload),
			Vars:         vars,
			RunCancelled: runCancelled,
		})
	})

	secrets, err := loadSecrets(*secretsFile)
	if err != nil {
		log.Error("read secrets", "path", *secretsFile, "err", err)
		os.Exit(1)
	}
	envSecrets, err := loadEnvSecrets(*secretsDir)
	if err != nil {
		log.Error("read per-environment secrets", "dir", *secretsDir, "err", err)
		os.Exit(1)
	}
	srv := server.New(st, server.Config{
		RegistrationToken:  *regToken,
		StopGrace:          *grace,
		Secrets:            secrets,
		EnvSecrets:         envSecrets,
		Vars:               vars,
		Forge:              server.Forge{URL: *forgeURL, APIURL: *forgeAPI},
		ForgeToken:         *forgeToken,
		WebhookSecret:      *hookSecret,
		PublicURL:          *publicURL,
		DefaultConcurrency: *defaultConc,
		NotifyWebhook:      *notifyHook,
		APIToken:           *apiToken,
		Repos:              splitList(*repos),
		Actions:            server.ActionPolicy{Allow: splitList(*allowedActions), RequireSHA: *requireSHA},
		RateLimit:          *rateLimit,
		RateWindow:         *rateWindow,
		Retention:          *retention,
	}, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *artifactDir != "" {
		if err := os.MkdirAll(*artifactDir, 0o755); err != nil {
			log.Error("artifact dir", "path", *artifactDir, "err", err)
			os.Exit(1)
		}
		host := *artifactHost
		if host == "" {
			host = reachableHost(*artifactAddr)
		}
		store, err := server.NewArtifactStore(*artifactDir, host, log)
		if err != nil {
			log.Error("artifact store", "err", err)
			os.Exit(1)
		}
		defer store.Close()
		srv.UseArtifactStore(store)

		as := &http.Server{
			Addr:              *artifactAddr,
			Handler:           store.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = as.Shutdown(shutdownCtx)
		}()
		go func() {
			// Its own listener rather than a path under the main mux: act
			// builds the URLs it hands back to upload-artifact out of the
			// request's Host header with no prefix of its own, so anything
			// mounted under a path would answer once and then send the client
			// to a URL that does not exist.
			if err := as.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("artifact store serve", "err", err)
			}
		}()
		log.Info("shared artifact store listening", "addr", *artifactAddr, "url", store.URL(), "dir", *artifactDir)
	} else {
		log.Warn("no -artifact-dir: each runner stores artifacts on its own disk, so a download-artifact " +
			"finds nothing unless the same runner happened to run the upload")
	}

	go srv.RunReaper(ctx)
	go srv.RunScheduler(ctx)
	go srv.RunPruner(ctx)

	hs := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdownCtx)
	}()

	// Names only — a log line that echoes a secret is a leaked secret.
	// Names and booleans only — a log line that echoes a secret is a leaked
	// secret, and that includes the forge token and the webhook secret.
	log.Info("orrery server listening", "addr", *addr, "db", *dbPath,
		"stop_grace", *grace, "forge", *forgeURL,
		"forge_token", *forgeToken != "", "webhooks", *hookSecret != "",
		"default_concurrency", *defaultConc, "notify", *notifyHook != "",
		"retention", *retention,
		"auth", *apiToken != "", "environments", keysOfEnv(envSecrets),
		"secrets", keysOf(secrets))
	log.Info("dashboard", "url", orDefault(*publicURL, dashboardURL(*addr)))
	if *hookSecret == "" {
		log.Warn("webhook endpoint disabled: set -webhook-secret to accept forge events")
	}
	if *forgeToken == "" {
		log.Warn("no forge token: results will not be reported back to the forge")
	}
	if *allowedActions == "" && !*requireSHA {
		log.Warn("no -allowed-actions: a workflow may pull in any third-party action, " +
			"on a runner that holds this server's secrets")
	}
	if *repos == "" {
		log.Warn("no -repos list: any repository a signed webhook names will be built with this " +
			"server's secrets; every repo pointing here shares the webhook secret")
	}
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
	log.Info("orrery server stopped")
}

// loadSecrets reads KEY=VALUE lines, then overlays anything in the environment
// under ORRERY_SECRET_. Values live only in this process and in the task it
// dispatches; nothing writes them to the database.
func loadSecrets(path string) (map[string]string, error) {
	out := map[string]string{}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				return nil, fmt.Errorf("malformed line %q: want KEY=VALUE", line)
			}
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	for k, v := range envWithPrefix("ORRERY_SECRET_") {
		out[k] = v
	}
	return out, nil
}

// readSecretFile reads a KEY=VALUE file without the ORRERY_SECRET_ overlay.
//
// Per-environment files are themselves an overlay; applying the process
// environment to each of them would put the same value in every environment,
// which is the opposite of what separate environments are for.
func readSecretFile(path string) (map[string]string, error) {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("malformed line %q: want KEY=VALUE", line)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

// dashboardURL turns a listen address into something clickable. ":8080" is a
// valid address and "http://8080/" is not a URL.
func dashboardURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr + "/"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envWithPrefix(prefix string) map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(k, prefix) {
			continue
		}
		out[strings.TrimPrefix(k, prefix)] = v
	}
	return out
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// loadEnvSecrets reads <environment>.env from a directory.
//
// One file per environment rather than one file with prefixes: the files can
// then have different owners and modes, which is the only way "staging's
// credentials are not production's" survives contact with a real machine.
func loadEnvSecrets(dir string) (map[string]map[string]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".env") {
			continue
		}
		secrets, err := readSecretFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[strings.TrimSuffix(name, ".env")] = secrets
	}
	return out, nil
}

// keysOfEnv logs which environments have their own secrets, never the values.
func keysOfEnv(m map[string]map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// reachableHost turns a listen address into one a job container can dial.
//
// A container cannot reach "localhost" — that is its own loopback — so a
// wildcard bind is resolved to this host's outbound IP, the same address the
// runner-local servers advertise. It is a guess, and the machine where it
// guesses wrong (behind NAT, or this server itself in a container) is exactly
// the machine whose operator should set -artifact-host.
func reachableHost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		if ip := common.GetOutboundIP(); ip != nil {
			host = ip.String()
		} else {
			host = "127.0.0.1"
		}
	}
	return net.JoinHostPort(host, port)
}
