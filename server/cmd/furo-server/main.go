package main

import (
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/cosmoabdon/furo/server/internal/tunnel"
)

func main() {
	controlAddr := flag.String("control", ":7835", "control listener address")
	httpAddr := flag.String("http", ":8080", "public HTTP listener address")
	baseDomain := flag.String("domain", "localhost", "base domain for tunnel URLs")
	token := flag.String("token", "dev", "shared auth token (M1 only)")
	username := flag.String("username", "dev", "hardcoded username (M1 only)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	srv := tunnel.New(tunnel.Config{
		ControlAddr: *controlAddr,
		HTTPAddr:    *httpAddr,
		BaseDomain:  *baseDomain,
		AuthToken:   *token,
		Username:    *username,
		Log:         log,
	})
	if err := srv.Start(); err != nil {
		log.Error("start failed", "err", err)
		os.Exit(1)
	}
	defer srv.Close()

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
	log.Info("shutting down")
}
