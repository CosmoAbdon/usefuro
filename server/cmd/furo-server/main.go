package main

import (
	"bufio"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cosmoabdon/furo/server/internal/config"
	"github.com/cosmoabdon/furo/server/internal/store"
	"github.com/cosmoabdon/furo/server/internal/tlsmgr"
	"github.com/cosmoabdon/furo/server/internal/tunnel"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  furo-server init   [--config config.yml]     interactive setup wizard
  furo-server serve  [--config config.yml] [--control :7835] [--http :8080]
                     [--domain X] [--tls off|self-signed|acme] [--data-dir ./data]
  furo-server user   add <username> | ls | rm <username>
  furo-server token  add <username> [--label X] | ls <username> | revoke <hash-prefix>

user/token accept --config (to find data_dir/TLS) or --data-dir directly.`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "init":
		cmdInit(os.Args[2:])
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

// loadConfig merges: defaults ← config file (if present) ← explicit flags.
func loadConfig(fs *flag.FlagSet, configPath string) config.Config {
	cfg := config.Default()
	if _, err := os.Stat(configPath); err == nil {
		loaded, err := config.Load(configPath)
		if err != nil {
			fatal(err)
		}
		cfg = loaded
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "domain":
			cfg.BaseDomain = f.Value.String()
		case "tls":
			cfg.TLS = f.Value.String()
		case "data-dir":
			cfg.DataDir = f.Value.String()
		}
	})
	return cfg
}

func openStore(dataDir string) *store.Store {
	st, err := store.Open(dataDir)
	if err != nil {
		fatal(err)
	}
	return st
}

// ---- init ----

func cmdInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	configPath := fs.String("config", "config.yml", "where to write the config")
	skipDNS := fs.Bool("skip-dns-check", false, "skip wildcard DNS validation")
	fs.Parse(args)

	in := bufio.NewReader(os.Stdin)
	ask := func(prompt, def string) string {
		fmt.Printf("%s [%s]: ", prompt, def)
		line, _ := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return def
		}
		return line
	}

	cfg := config.Default()
	fmt.Println("furo-server setup — answers are written to", *configPath)
	cfg.BaseDomain = ask("base domain (tunnels live under *.<user>.<base>)", "proxy.example.com")
	cfg.TLS = ask("tls mode (off | self-signed | acme)", config.TLSACME)
	if cfg.TLS == config.TLSACME {
		cfg.ACMEEmail = ask("ACME e-mail (Let's Encrypt)", "")
		cfg.DNSProvider = ask("DNS provider (cloudflare)", "cloudflare")
		cfg.DNSToken = ask("DNS API token (tip: use ${FURO_DNS_TOKEN} and export it)", "${FURO_DNS_TOKEN}")
	}
	cfg.ControlPort = askInt(ask, "control port", cfg.ControlPort)
	cfg.HTTPPort = askInt(ask, "public HTTPS port", 443)
	cfg.AdminToken = ask("admin token for the web UI (tip: ${FURO_ADMIN_TOKEN})", "${FURO_ADMIN_TOKEN}")
	cfg.DataDir = ask("data directory (sqlite + certs)", "/var/lib/furo")

	if err := cfg.Validate(); err != nil {
		// ${VAR} placeholders are fine at init time; only structural errors matter.
		if !strings.Contains(err.Error(), "dns_token") {
			fatal(err)
		}
	}

	if !*skipDNS && cfg.BaseDomain != "localhost" {
		checkWildcardDNS(cfg.BaseDomain)
	}

	if err := config.Save(*configPath, cfg); err != nil {
		fatal(err)
	}
	fmt.Printf("\nwrote %s\nnext steps:\n  furo-server user add <username> --config %s\n  furo-server serve --config %s\n", *configPath, *configPath, *configPath)
}

func askInt(ask func(string, string) string, prompt string, def int) int {
	for {
		s := ask(prompt, strconv.Itoa(def))
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
		fmt.Println("not a number, try again")
	}
}

// checkWildcardDNS resolves a random label under the base domain and compares
// it with this machine's public IP. Warning only — never blocks init.
func checkWildcardDNS(base string) {
	probe := fmt.Sprintf("furo-probe-%d.%s", time.Now().UnixNano()%100000, base)
	fmt.Printf("checking wildcard DNS (%s)... ", probe)
	addrs, err := lookupWithTimeout(probe, 5*time.Second)
	if err != nil || len(addrs) == 0 {
		fmt.Printf("FAILED\n  *.%s does not resolve — create a wildcard A/AAAA record pointing at this server.\n", base)
		return
	}
	pub := publicIP(5 * time.Second)
	if pub == "" {
		fmt.Printf("resolves to %v (could not detect this machine's public IP to compare)\n", addrs)
		return
	}
	for _, a := range addrs {
		if a == pub {
			fmt.Printf("ok (%s)\n", pub)
			return
		}
	}
	fmt.Printf("WARNING\n  *.%s resolves to %v but this machine's public IP is %s.\n", base, addrs, pub)
}

// ---- serve ----

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "config.yml", "config file (used when present)")
	controlAddr := fs.String("control", "", "control listener address (overrides control_port)")
	httpAddr := fs.String("http", "", "public listener address (overrides http_port)")
	fs.String("domain", "", "base domain for tunnel URLs")
	fs.String("tls", "", "tls mode: off | self-signed | acme")
	fs.String("data-dir", "", "data directory (sqlite + certs)")
	pingInterval := fs.Duration("ping-interval", 30*time.Second, "heartbeat ping interval")
	pongTimeout := fs.Duration("pong-timeout", 90*time.Second, "drop session after this long without a pong")
	fs.Parse(args)

	cfg := loadConfig(fs, *configPath)
	if *controlAddr == "" {
		*controlAddr = fmt.Sprintf(":%d", cfg.ControlPort)
	}
	if *httpAddr == "" {
		*httpAddr = fmt.Sprintf(":%d", cfg.HTTPPort)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st := openStore(cfg.DataDir)
	defer st.Close()

	mgr, err := tlsmgr.New(cfg)
	if err != nil {
		fatal(err)
	}
	tcfg := tunnel.Config{
		ControlAddr:  *controlAddr,
		HTTPAddr:     *httpAddr,
		BaseDomain:   cfg.BaseDomain,
		Authenticate: st.Authenticate,
		PingInterval: *pingInterval,
		PongTimeout:  *pongTimeout,
		Log:          log,
	}
	if mgr != nil {
		tcfg.ControlTLS = tlsmgr.ServerTLSConfig(mgr)
		tcfg.PublicTLS = tlsmgr.ServerTLSConfig(mgr)
		// Kick off issuance/renewal for base + existing users.
		go func() {
			if err := mgr.EnsureBase(); err != nil {
				log.Error("cert issuance (base)", "err", err)
			}
			users, err := st.Users()
			if err != nil {
				return
			}
			for _, u := range users {
				if err := mgr.EnsureUser(u.Username); err != nil {
					log.Error("cert issuance (user)", "username", u.Username, "err", err)
				}
			}
		}()
	}

	srv := tunnel.New(tcfg)
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

// ---- user ----

func cmdUser(args []string) {
	if len(args) < 1 {
		usage()
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("user", flag.ExitOnError)
	configPath := fs.String("config", "config.yml", "config file (used when present)")
	fs.String("data-dir", "", "data directory (sqlite)")
	fs.String("tls", "", "tls mode override")
	fs.String("domain", "", "base domain override")

	switch sub {
	case "add":
		if len(args) < 1 {
			usage()
		}
		username := args[0]
		fs.Parse(args[1:])
		cfg := loadConfig(fs, *configPath)
		st := openStore(cfg.DataDir)
		defer st.Close()
		if err := st.CreateUser(username); err != nil {
			fatal(err)
		}
		token, err := st.CreateToken(username, "default")
		if err != nil {
			fatal(err)
		}
		fmt.Printf("user %s created\ntoken: %s\n(store it now — it will not be shown again)\n", username, token)
		if cfg.TLS == config.TLSACME {
			mgr, err := tlsmgr.New(cfg)
			if err != nil {
				fatal(err)
			}
			fmt.Printf("issuing wildcard cert *.%s.%s ... ", username, cfg.BaseDomain)
			if err := mgr.EnsureUser(username); err != nil {
				fmt.Printf("FAILED: %v\n(the running server retries on demand)\n", err)
			} else {
				fmt.Println("ok")
			}
		}
	case "ls":
		fs.Parse(args)
		cfg := loadConfig(fs, *configPath)
		st := openStore(cfg.DataDir)
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
		cfg := loadConfig(fs, *configPath)
		st := openStore(cfg.DataDir)
		defer st.Close()
		if err := st.DeleteUser(username); err != nil {
			fatal(err)
		}
		fmt.Printf("user %s removed\n", username)
	default:
		usage()
	}
}

// ---- token ----

func cmdToken(args []string) {
	if len(args) < 1 {
		usage()
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	configPath := fs.String("config", "config.yml", "config file (used when present)")
	fs.String("data-dir", "", "data directory (sqlite)")

	switch sub {
	case "add":
		if len(args) < 1 {
			usage()
		}
		username := args[0]
		label := fs.String("label", "", `token label ("notebook", "ci", ...)`)
		fs.Parse(args[1:])
		cfg := loadConfig(fs, *configPath)
		st := openStore(cfg.DataDir)
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
		cfg := loadConfig(fs, *configPath)
		st := openStore(cfg.DataDir)
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
		cfg := loadConfig(fs, *configPath)
		st := openStore(cfg.DataDir)
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
