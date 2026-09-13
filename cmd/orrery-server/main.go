// Command orrery-server is Orrery's control plane.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/feed-mob/feedmob-orrery/internal/server"
	"github.com/feed-mob/feedmob-orrery/internal/store"
)

func main() {
	var (
		addr     = flag.String("addr", ":8080", "listen address")
		dbPath   = flag.String("db", "orrery.db", "path to the sqlite database")
		regToken = flag.String("registration-token", os.Getenv("ORRERY_REGISTRATION_TOKEN"), "shared secret a runner presents once to register")
		grace    = flag.Duration("stop-grace", 30*time.Second, "how long a runner has to acknowledge a stop before it is force-terminated")
		verbose  = flag.Bool("v", false, "debug logging")
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

	srv := server.New(st, server.Config{
		RegistrationToken: *regToken,
		StopGrace:         *grace,
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

	log.Info("orrery server listening", "addr", *addr, "db", *dbPath, "stop_grace", *grace)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
	log.Info("orrery server stopped")
}
