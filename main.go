package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
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
	}
	fmt.Println("listening on", cfg.Listen)
	if err := http.ListenAndServe(cfg.Listen, srv.routes()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
