// Command orrery-server is Orrery's control plane.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

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
		verbose     = flag.Bool("v", false, "debug logging")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

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

	secrets, err := loadSecrets(*secretsFile)
	if err != nil {
		log.Error("read secrets", "path", *secretsFile, "err", err)
		os.Exit(1)
	}
	srv := server.New(st, server.Config{
		RegistrationToken: *regToken,
		StopGrace:         *grace,
		Secrets:           secrets,
		Vars:              envWithPrefix("ORRERY_VAR_"),
		Forge:             server.Forge{URL: *forgeURL, APIURL: *forgeAPI},
	}, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go srv.RunReaper(ctx)

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
	log.Info("orrery server listening", "addr", *addr, "db", *dbPath,
		"stop_grace", *grace, "forge", *forgeURL, "secrets", keysOf(secrets))
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
