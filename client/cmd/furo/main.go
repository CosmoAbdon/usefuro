package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/cosmoabdon/furo/client/internal/clientcfg"
	"github.com/cosmoabdon/furo/client/internal/inspector"
	"github.com/cosmoabdon/furo/client/internal/proxy"
	"github.com/cosmoabdon/furo/client/internal/tunnel"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  furo login <token> [--server addr] [--ca file] [--insecure] [--plaintext]
  furo http <port>   [--name X | -n X] [--server addr] [--token T]
                     [--ca file] [--insecure] [--plaintext]

login stores settings in ~/.config/furo/config.yml; http flags override them.
Without --name the server assigns a random one.`)
	os.Exit(2)
}

// version is set by GoReleaser via ldflags.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "login":
		cmdLogin(os.Args[2:])
	case "http":
		cmdHTTP(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("furo", version)
	default:
		usage()
	}
}

func cmdLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	server := fs.String("server", "127.0.0.1:7835", "server control address")
	ca := fs.String("ca", "", "CA bundle for self-signed servers")
	insecure := fs.Bool("insecure", false, "skip TLS verification")
	plaintext := fs.Bool("plaintext", false, "no TLS on the control connection (dev)")

	if len(args) < 1 || args[0] == "-h" || args[0] == "--help" {
		usage()
	}
	token := args[0]
	fs.Parse(args[1:])

	path, err := clientcfg.Save(clientcfg.File{
		Server:    *server,
		Token:     token,
		CA:        *ca,
		Insecure:  *insecure,
		Plaintext: *plaintext,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("logged in — settings saved to %s\n", path)
}

func cmdHTTP(args []string) {
	saved, err := clientcfg.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error reading config:", err)
		os.Exit(1)
	}
	if saved.Server == "" {
		saved.Server = "127.0.0.1:7835"
	}

	fs := flag.NewFlagSet("http", flag.ExitOnError)
	name := fs.String("name", "", "tunnel name (empty → server-generated)")
	server := fs.String("server", saved.Server, "server control address")
	token := fs.String("token", saved.Token, "auth token (furo_...)")
	ca := fs.String("ca", saved.CA, "CA bundle for self-signed servers")
	insecure := fs.Bool("insecure", saved.Insecure, "skip TLS verification")
	plaintext := fs.Bool("plaintext", saved.Plaintext, "no TLS on the control connection (dev)")
	inspectorPort := fs.Int("inspector-port", 4040, "inspector base port (auto-increments when busy)")
	noInspector := fs.Bool("no-inspector", false, "disable the local inspector")
	fs.StringVar(name, "n", "", "tunnel name (shorthand)")

	// Accept "furo http 3003 --flags" (port first, then flags).
	if len(args) < 1 || args[0] == "-h" || args[0] == "--help" {
		usage()
	}
	port := args[0]
	fs.Parse(args[1:])

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *token == "" {
		log.Error("no token — run `furo login <token>` or pass --token")
		os.Exit(1)
	}

	var ring *proxy.Ring
	if !*noInspector {
		ring = proxy.NewRing()
		insp := inspector.New(ring, log)
		url, err := insp.Start(*inspectorPort)
		if err != nil {
			log.Error("inspector failed to start", "err", err)
			os.Exit(1)
		}
		defer insp.Close()
		fmt.Printf("Inspector: %s\n", url)
	}

	c, err := tunnel.New(tunnel.Config{
		ServerAddr: *server,
		Token:      *token,
		Name:       *name,
		LocalAddr:  "127.0.0.1:" + port,
		Plaintext:  *plaintext,
		CAFile:     *ca,
		Insecure:   *insecure,
		Ring:       ring,
		Log:        log,
	})
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}
	if err := c.Run(); err != nil {
		log.Error("tunnel ended", "err", err)
		os.Exit(1)
	}
}
