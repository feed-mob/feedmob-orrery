// Command orrery-runner claims tasks from an Orrery server and runs them.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/feed-mob/feedmob-orrery/internal/protocol"
	"github.com/feed-mob/feedmob-orrery/internal/runner"
)

// credentials is what we persist after registering, so a restart does not mint
// a second runner identity.
type credentials struct {
	Token string `json:"token"`
	Name  string `json:"name"`
	UUID  string `json:"uuid"`
}

func main() {
	host, _ := os.Hostname()
	var (
		serverURL = flag.String("server", "http://127.0.0.1:8080", "orrery server URL")
		name      = flag.String("name", host, "runner name")
		labelsRaw = flag.String("labels", "self-hosted,host", "comma-separated labels this runner advertises")
		regToken  = flag.String("registration-token", os.Getenv("ORRERY_REGISTRATION_TOKEN"), "registration secret, used once")
		credPath  = flag.String("credentials", "orrery-runner.json", "where to persist the runner token")
		workDir   = flag.String("work-dir", "", "directory for job workspaces")
		verbose   = flag.Bool("v", false, "debug logging")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	labels := splitLabels(*labelsRaw)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	creds, err := loadCredentials(*credPath)
	if err != nil {
		log.Error("read credentials", "path", *credPath, "err", err)
		os.Exit(1)
	}

	cl := runner.NewClient(*serverURL, "")
	if creds == nil {
		if *regToken == "" {
			log.Error("no stored credentials and no registration token; set -registration-token or ORRERY_REGISTRATION_TOKEN")
			os.Exit(2)
		}
		res, err := cl.Register(ctx, &protocol.RegisterRequest{
			Name:         *name,
			Token:        *regToken,
			Version:      runner.Version,
			Labels:       labels,
			Capabilities: runner.Capabilities,
		})
		if err != nil {
			log.Error("register", "err", err)
			os.Exit(1)
		}
		creds = &credentials{Token: res.Runner.Token, Name: res.Runner.Name, UUID: res.Runner.UUID}
		if err := saveCredentials(*credPath, creds); err != nil {
			log.Error("save credentials", "err", err)
			os.Exit(1)
		}
		log.Info("registered", "name", creds.Name, "credentials", *credPath)
	}
	cl.SetToken(creds.Token)

	r := runner.New(cl, runner.Options{
		Name:    creds.Name,
		Labels:  labels,
		WorkDir: *workDir,
	}, log)

	if err := r.Run(ctx); err != nil {
		if errors.Is(err, runner.ErrUnauthorized) {
			// The server rejected our credential. Retrying cannot help and a
			// runner that keeps hammering is worse than one that exits loudly.
			log.Error("server rejected our token; delete the credentials file and re-register", "path", *credPath)
			os.Exit(3)
		}
		log.Error("runner stopped", "err", err)
		os.Exit(1)
	}
	log.Info("runner stopped")
}

func splitLabels(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func loadCredentials(path string) (*credentials, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if c.Token == "" {
		return nil, nil
	}
	return &c, nil
}

func saveCredentials(path string, c *credentials) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 0600: this file is a credential.
	return os.WriteFile(path, data, 0o600)
}
