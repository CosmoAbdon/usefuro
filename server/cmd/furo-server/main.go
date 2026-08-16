package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cosmoabdon/furo/server/internal/store"
	"github.com/cosmoabdon/furo/server/internal/tunnel"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  furo-server serve  [--control :7835] [--http :8080] [--domain localhost] [--data-dir ./data]
  furo-server user   add <username> | ls | rm <username>        [--data-dir ./data]
  furo-server token  add <username> [--label X] | ls <username> | revoke <hash-prefix>
                                                                [--data-dir ./data]`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
	case "user":
		cmdUser(os.Args[2:])
	case "token":
		cmdToken(os.Args[2:])
	default:
		usage()
	}
}

func openStore(dataDir string) *store.Store {
	st, err := store.Open(dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open store:", err)
		os.Exit(1)
	}
	return st
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	controlAddr := fs.String("control", ":7835", "control listener address")
	httpAddr := fs.String("http", ":8080", "public HTTP listener address")
	baseDomain := fs.String("domain", "localhost", "base domain for tunnel URLs")
	dataDir := fs.String("data-dir", "./data", "data directory (sqlite)")
	pingInterval := fs.Duration("ping-interval", 30*time.Second, "heartbeat ping interval")
	pongTimeout := fs.Duration("pong-timeout", 90*time.Second, "drop session after this long without a pong")
	fs.Parse(args)

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st := openStore(*dataDir)
	defer st.Close()

	srv := tunnel.New(tunnel.Config{
		ControlAddr:  *controlAddr,
		HTTPAddr:     *httpAddr,
		BaseDomain:   *baseDomain,
		Authenticate: st.Authenticate,
		PingInterval: *pingInterval,
		PongTimeout:  *pongTimeout,
		Log:          log,
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

func cmdUser(args []string) {
	if len(args) < 1 {
		usage()
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("user", flag.ExitOnError)
	dataDir := fs.String("data-dir", "./data", "data directory (sqlite)")

	switch sub {
	case "add":
		if len(args) < 1 {
			usage()
		}
		username := args[0]
		fs.Parse(args[1:])
		st := openStore(*dataDir)
		defer st.Close()
		if err := st.CreateUser(username); err != nil {
			fatal(err)
		}
		token, err := st.CreateToken(username, "default")
		if err != nil {
			fatal(err)
		}
		// Wildcard cert emission for *.username.<base> lands in M3.
		fmt.Printf("user %s created\ntoken: %s\n(store it now — it will not be shown again)\n", username, token)
	case "ls":
		fs.Parse(args)
		st := openStore(*dataDir)
		defer st.Close()
		users, err := st.Users()
		if err != nil {
			fatal(err)
		}
		for _, u := range users {
			fmt.Printf("%-24s tokens=%d created=%s\n", u.Username, u.TokenCount, u.CreatedAt)
		}
	case "rm":
		if len(args) < 1 {
			usage()
		}
		username := args[0]
		fs.Parse(args[1:])
		st := openStore(*dataDir)
		defer st.Close()
		if err := st.DeleteUser(username); err != nil {
			fatal(err)
		}
		fmt.Printf("user %s removed\n", username)
	default:
		usage()
	}
}

func cmdToken(args []string) {
	if len(args) < 1 {
		usage()
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	dataDir := fs.String("data-dir", "./data", "data directory (sqlite)")

	switch sub {
	case "add":
		if len(args) < 1 {
			usage()
		}
		username := args[0]
		label := fs.String("label", "", `token label ("notebook", "ci", ...)`)
		fs.Parse(args[1:])
		st := openStore(*dataDir)
		defer st.Close()
		token, err := st.CreateToken(username, *label)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("token: %s\n(store it now — it will not be shown again)\n", token)
	case "ls":
		if len(args) < 1 {
			usage()
		}
		username := args[0]
		fs.Parse(args[1:])
		st := openStore(*dataDir)
		defer st.Close()
		tokens, err := st.Tokens(username)
		if err != nil {
			fatal(err)
		}
		for _, t := range tokens {
			status := "active"
			if t.Revoked {
				status = "revoked"
			}
			fmt.Printf("%s  %-8s label=%q created=%s\n", t.HashPrefix, status, t.Label, t.CreatedAt)
		}
	case "revoke":
		if len(args) < 1 {
			usage()
		}
		prefix := args[0]
		fs.Parse(args[1:])
		st := openStore(*dataDir)
		defer st.Close()
		if err := st.RevokeToken(prefix); err != nil {
			fatal(err)
		}
		fmt.Println("token revoked")
	default:
		usage()
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
