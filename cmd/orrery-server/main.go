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
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

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
		forgeURL    = flag.String("forge-url", "https://github.com", "where the code being built lives; becomes github.server_url, which actions/checkout clones from")
		forgeAPI    = flag.String("forge-api-url", "", "forge REST API root; derived from -forge-url when empty (api.github.com for github.com, else <forge>/api/v3 — a Gitea forge must set /api/v1 here)")
		forgeToken  = flag.String("forge-token", os.Getenv("ORRERY_FORGE_TOKEN"), "token used to read workflow files and write commit statuses; without it webhooks cannot read a private repo and results are not reported back")
		hookSecret  = flag.String("webhook-secret", os.Getenv("ORRERY_WEBHOOK_SECRET"), "shared secret GitHub signs webhook deliveries with; empty disables the webhook endpoint")
		publicURL   = flag.String("public-url", os.Getenv("ORRERY_PUBLIC_URL"), "where humans reach this server; used as the target of commit statuses")
		apiToken    = flag.String("api-token", os.Getenv("ORRERY_API_TOKEN"),
			"token every human-facing API call and the dashboard must present")
		noAuth = flag.Bool("insecure-no-auth", false,
			"serve the API and dashboard with no authentication; anyone who can reach the port can run arbitrary workflows with this server's secrets")
		retention = flag.Duration("retention", 30*24*time.Hour,
			"how long a finished run and its logs are kept; 0 keeps everything")
		repos = flag.String("repos", os.Getenv("ORRERY_REPOS"),
			"comma-separated owner/repo this server will build; empty means any repository a signed webhook names")
		notifyHook = flag.String("notify-webhook", os.Getenv("ORRERY_NOTIFY_WEBHOOK"),
			"chat webhook that receives a message when a workflow's verdict changes (Slack-shaped {\"text\"}); only changes are sent, not every run")
		defaultConc = flag.String("default-concurrency", "${{ github.workflow }}@${{ github.ref }}",
			"concurrency group applied to a workflow that declares none; runs sharing a group queue rather than race. Empty restores GitHub's behaviour of no limit")
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
	srv := server.New(st, server.Config{
		RegistrationToken:  *regToken,
		StopGrace:          *grace,
		Secrets:            secrets,
		Vars:               vars,
		Forge:              server.Forge{URL: *forgeURL, APIURL: *forgeAPI},
		ForgeToken:         *forgeToken,
		WebhookSecret:      *hookSecret,
		PublicURL:          *publicURL,
		DefaultConcurrency: *defaultConc,
		NotifyWebhook:      *notifyHook,
		APIToken:           *apiToken,
		Repos:              splitList(*repos),
		Retention:          *retention,
	}, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
		"auth", *apiToken != "",
		"secrets", keysOf(secrets))
	log.Info("dashboard", "url", orDefault(*publicURL, dashboardURL(*addr)))
	if *hookSecret == "" {
		log.Warn("webhook endpoint disabled: set -webhook-secret to accept forge events")
	}
	if *forgeToken == "" {
		log.Warn("no forge token: results will not be reported back to the forge")
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
