package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	var out io.Writer = io.Discard
	if cfg.Logging {
		out = os.Stdout
	}
	logger := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	store, err := openStore(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	srv := &Server{
		cfg:      cfg,
		store:    store,
		sessions: &Sessions{secret: []byte(cfg.SessionSecret)},
		hub:      newHub(),
		start:    time.Now(),
	}
	slog.Info("server starting", "listen", cfg.Listen, "dataDir", cfg.DataDir, "logging", cfg.Logging)
	if err := http.ListenAndServe(cfg.Listen, srv.routes()); err != nil {
		slog.Error("server failed", "err", err)
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
