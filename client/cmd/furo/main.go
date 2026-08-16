package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/cosmoabdon/furo/client/internal/clientcfg"
	"github.com/cosmoabdon/furo/client/internal/inspector"
	"github.com/cosmoabdon/furo/client/internal/proxy"
	"github.com/cosmoabdon/furo/client/internal/tunnel"
)

// version is set by GoReleaser via ldflags.
var version = "dev"

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  furo login <token>  [--server addr] [--ca file] [--insecure] [--plaintext]
  furo http <port>    [--name X | -n X] [connection flags]
  furo start          [--file furo.yml] [connection flags]
  furo status         [--inspector-port N]

connection flags: --server addr  --token T  --ca file  --insecure  --plaintext
                  --inspector-port N  --no-inspector
login stores settings in ~/.config/furo/config.yml; flags override them.
Without --name the server assigns a random one.`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "login":
		cmdLogin(os.Args[2:])
	case "http":
		cmdHTTP(os.Args[2:])
	case "start":
		cmdStart(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
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

// connFlags defines the connection/inspector flags shared by http and start,
// with defaults coming from the saved login config.
type connFlags struct {
	server, token, ca   *string
	insecure, plaintext *bool
	inspectorPort       *int
	noInspector         *bool
}

func addConnFlags(fs *flag.FlagSet) connFlags {
	saved, err := clientcfg.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error reading config:", err)
		os.Exit(1)
	}
	if saved.Server == "" {
		saved.Server = "127.0.0.1:7835"
	}
	return connFlags{
		server:        fs.String("server", saved.Server, "server control address"),
		token:         fs.String("token", saved.Token, "auth token (furo_...)"),
		ca:            fs.String("ca", saved.CA, "CA bundle for self-signed servers"),
		insecure:      fs.Bool("insecure", saved.Insecure, "skip TLS verification"),
		plaintext:     fs.Bool("plaintext", saved.Plaintext, "no TLS on the control connection (dev)"),
		inspectorPort: fs.Int("inspector-port", 4040, "inspector base port (auto-increments when busy)"),
		noInspector:   fs.Bool("no-inspector", false, "disable the local inspector"),
	}
}

func runTunnels(cf connFlags, specs []tunnel.TunnelSpec) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *cf.token == "" {
		log.Error("no token — run `furo login <token>` or pass --token")
		os.Exit(1)
	}

	var ring *proxy.Ring
	if !*cf.noInspector {
		ring = proxy.NewRing()
	}

	c, err := tunnel.New(tunnel.Config{
		ServerAddr: *cf.server,
		Token:      *cf.token,
		Tunnels:    specs,
		Plaintext:  *cf.plaintext,
		CAFile:     *cf.ca,
		Insecure:   *cf.insecure,
		Ring:       ring,
		Log:        log,
	})
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}

	if ring != nil {
		insp := inspector.New(ring, c.Status, log)
		url, err := insp.Start(*cf.inspectorPort)
		if err != nil {
			log.Error("inspector failed to start", "err", err)
			os.Exit(1)
		}
		defer insp.Close()
		fmt.Printf("Inspector: %s\n", url)
	}

	if err := c.Run(); err != nil {
		log.Error("tunnel ended", "err", err)
		os.Exit(1)
	}
}

func cmdHTTP(args []string) {
	fs := flag.NewFlagSet("http", flag.ExitOnError)
	name := fs.String("name", "", "tunnel name (empty → server-generated)")
	fs.StringVar(name, "n", "", "tunnel name (shorthand)")
	cf := addConnFlags(fs)

	// Accept "furo http 3003 --flags" (port first, then flags).
	if len(args) < 1 || args[0] == "-h" || args[0] == "--help" {
		usage()
	}
	port := args[0]
	fs.Parse(args[1:])

	// "localhost" (not 127.0.0.1): dev servers on Node 17+ often bind only
	// ::1 — Go tries both address families when dialing a hostname.
	runTunnels(cf, []tunnel.TunnelSpec{{Name: *name, LocalAddr: "localhost:" + port}})
}

func cmdStart(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	file := fs.String("file", "furo.yml", "tunnel definitions file")
	cf := addConnFlags(fs)
	fs.Parse(args)

	specs, err := clientcfg.LoadTunnelFile(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s: %v\n", *file, err)
		os.Exit(1)
	}
	runTunnels(cf, specs)
}

// cmdStatus scans the local inspector port range and prints every tunnel of
// every running furo process on this machine.
func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	basePort := fs.Int("inspector-port", 4040, "inspector base port to scan from")
	fs.Parse(args)

	client := &http.Client{Timeout: 500 * time.Millisecond}
	found := 0
	for port := *basePort; port < *basePort+inspector.PortAttempts; port++ {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/status", port))
		if err != nil {
			continue
		}
		var body struct {
			App     string                `json:"app"`
			Tunnels []tunnel.TunnelStatus `json:"tunnels"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil || body.App != "furo" {
			continue // some other service on this port
		}
		for _, t := range body.Tunnels {
			if found == 0 {
				fmt.Printf("%-16s %-44s %-18s %-10s %s\n", "NAME", "URL", "LOCAL", "UPTIME", "INSPECTOR")
			}
			found++
			fmt.Printf("%-16s %-44s %-18s %-10s http://localhost:%d\n",
				t.Name, t.URL, t.LocalAddr, time.Since(t.Since).Round(time.Second), port)
		}
	}
	if found == 0 {
		fmt.Println("no active tunnels on this machine")
	}
}
