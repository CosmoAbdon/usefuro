package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/cosmoabdon/furo/client/internal/tunnel"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  furo http <port> [--name X | -n X] [--server addr] [--token T]

Without --name the server assigns a random one.
login/start/status land in later milestones.`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "http":
		cmdHTTP(os.Args[2:])
	default:
		usage()
	}
}

func cmdHTTP(args []string) {
	fs := flag.NewFlagSet("http", flag.ExitOnError)
	name := fs.String("name", "", "tunnel name (empty → server-generated)")
	server := fs.String("server", "127.0.0.1:7835", "server control address")
	token := fs.String("token", "", "auth token (furo_...)")
	fs.StringVar(name, "n", "", "tunnel name (shorthand)")

	// Accept "furo http 3003 --flags" (port first, then flags).
	if len(args) < 1 || args[0] == "-h" || args[0] == "--help" {
		usage()
	}
	port := args[0]
	fs.Parse(args[1:])

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *token == "" {
		log.Error("missing --token (furo login lands in a later milestone)")
		os.Exit(1)
	}

	c := tunnel.New(tunnel.Config{
		ServerAddr: *server,
		Token:      *token,
		Name:       *name,
		LocalAddr:  "127.0.0.1:" + port,
		Log:        log,
	})
	if err := c.Run(); err != nil {
		log.Error("tunnel ended", "err", err)
		os.Exit(1)
	}
}
