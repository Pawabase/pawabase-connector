// Command pawabase-connector lets a Pawabase project use your own Postgres database without Pawabase ever holding its password.
//
// It runs next to the database, dials out to Pawabase (no inbound port), and answers Pawabase's connections by logging into the
// database itself with the address only it knows.
//
//	pawabase-connector enroll --server https://api.example.com --token pbce_…   # once: exchange the token for this connector's key
//	pawabase-connector run                                                      # in the foreground
//	pawabase-connector service install                                          # as a system service (systemd, launchd, Windows)
//	pawabase-connector status                                                   # what it is set up to do, and whether it works
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/kardianos/service"
	"golang.org/x/term"

	"github.com/Pawabase/pawabase-connector/internal/config"
	"github.com/Pawabase/pawabase-connector/internal/tunnel"
)

const usage = `pawabase-connector — use your own Postgres database with Pawabase, without sharing its password.

Usage:
  pawabase-connector enroll --server URL --token TOKEN [--database-url URL] [--name NAME]
  pawabase-connector run
  pawabase-connector status
  pawabase-connector service install|uninstall|start|stop|restart|status
  pawabase-connector version

The database address is asked for here (hidden) and kept in this machine's configuration, readable only by its owner.
DATABASE_URL in the environment overrides it. Configuration: %s
`

func main() {
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, config.Path())
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "enroll":
		err = enroll(os.Args[2:])
	case "run":
		err = run()
	case "status":
		err = status()
	case "service":
		err = serviceCommand(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("pawabase-connector %s (%s/%s)\n", tunnel.Version, runtime.GOOS, runtime.GOARCH)
	case "help", "--help", "-h":
		fmt.Printf(usage, config.Path())
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n"+usage, os.Args[1], config.Path())
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// ── enroll ──────────────────────────────────────────────────────────────────

func enroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("PAWABASE_SERVER"), "Pawabase's address, as Studio showed it")
	token := fs.String("token", os.Getenv("PAWABASE_CONNECTOR_TOKEN"), "the one-time enrollment token (pbce_…)")
	databaseURL := fs.String("database-url", "", "the database address; asked for (hidden) when left out")
	name := fs.String("name", "", "a name for this connector")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" || *token == "" {
		return errors.New("--server and --token are required (Studio shows both)")
	}
	dbURL := *databaseURL
	if dbURL == "" {
		dbURL = firstEnv("PAWABASE_CONNECTOR_DATABASE_URL", "DATABASE_URL")
	}
	if dbURL == "" {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("no terminal to ask for the database address: pass --database-url or set DATABASE_URL")
		}
		fmt.Print("Database address (postgres://user:password@host:5432/dbname): ")
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return err
		}
		dbURL = strings.TrimSpace(string(raw))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fmt.Println("Checking the database…")
	info, err := tunnel.Check(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("could not log into the database: %w", err)
	}
	fmt.Printf("  %s as %s (Postgres %s)\n", info["database"], info["user"], info["server_version"])

	if err := config.CheckWritable(); err != nil {
		return err
	}
	cfg, err := exchange(ctx, strings.TrimRight(*server, "/"), *token)
	if err != nil {
		return err
	}
	cfg.DatabaseURL = dbURL
	if *name != "" {
		cfg.Name = *name
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("Enrolled as %s. Saved to %s (only its owner can read it).\n", cfg.ConnectorID, config.Path())
	fmt.Println("Next: `pawabase-connector service install` to run it in the background, or `pawabase-connector run` to try it now.")
	return nil
}

// exchange trades the one-time token for this connector's key.
func exchange(ctx context.Context, server, token string) (*config.Config, error) {
	host, _ := os.Hostname()
	body, _ := json.Marshal(map[string]string{"token": token, "os": runtime.GOOS, "arch": runtime.GOARCH, "hostname": host, "version": tunnel.Version})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/connect/v1/enroll", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach Pawabase at %s: %w", server, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Pawabase refused the token (%d): %s", resp.StatusCode, problemText(raw))
	}
	var out struct {
		Connector string `json:"connector"`
		Key       string `json:"key"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Key == "" {
		return nil, errors.New("Pawabase answered the enrollment in a way this connector does not understand")
	}
	return &config.Config{Server: server, ConnectorID: out.Connector, Key: out.Key, Name: out.Name}, nil
}

// ── run ─────────────────────────────────────────────────────────────────────

// loadOrEnroll reads the configuration; in a container it enrolls first from PAWABASE_SERVER and PAWABASE_CONNECTOR_TOKEN.
func loadOrEnroll() (*config.Config, error) {
	cfg, err := config.Load()
	if !errors.Is(err, config.ErrNotEnrolled) {
		return cfg, err
	}
	server, token := os.Getenv("PAWABASE_SERVER"), os.Getenv("PAWABASE_CONNECTOR_TOKEN")
	dbURL := firstEnv("PAWABASE_CONNECTOR_DATABASE_URL", "DATABASE_URL")
	if server == "" || token == "" || dbURL == "" {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := config.CheckWritable(); err != nil {
		return nil, err
	}
	cfg, err = exchange(ctx, strings.TrimRight(server, "/"), token)
	if err != nil {
		return nil, err
	}
	cfg.DatabaseURL = dbURL
	if err := cfg.Save(); err != nil {
		return nil, err
	}
	log.Printf("enrolled as %s", cfg.ConnectorID)
	return cfg, nil
}

func run() error {
	cfg, err := loadOrEnroll()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := log.New(os.Stderr, "", log.LstdFlags)
	logger.Printf("pawabase-connector %s: connecting to %s for %s", tunnel.Version, cfg.Server, cfg.RedactedDatabase())
	return tunnel.Run(ctx, cfg, logger)
}

// ── status ──────────────────────────────────────────────────────────────────

func status() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	fmt.Printf("connector   %s\nserver      %s\ndatabase    %s\nconfig      %s\n", cfg.ConnectorID, cfg.Server, cfg.RedactedDatabase(), config.Path())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if info, err := tunnel.Check(ctx, cfg.DatabaseURL); err != nil {
		fmt.Printf("database    NOT reachable: %v\n", err)
	} else {
		fmt.Printf("database    ok: %s as %s (Postgres %s)\n", info["database"], info["user"], info["server_version"])
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.Server, "/")+"/health", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil {
		fmt.Printf("pawabase    NOT reachable: %v\n", err)
	} else {
		resp.Body.Close()
		fmt.Printf("pawabase    reachable (%d)\n", resp.StatusCode)
	}
	if svc, err := newService(); err == nil {
		if st, err := svc.Status(); err == nil {
			fmt.Printf("service     %s\n", map[service.Status]string{service.StatusRunning: "running", service.StatusStopped: "stopped"}[st])
		} else {
			fmt.Println("service     not installed")
		}
	}
	return nil
}

// ── service ─────────────────────────────────────────────────────────────────

type program struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *program) Start(s service.Service) error {
	cfg, err := loadOrEnroll()
	if err != nil {
		return err
	}
	logger, err := serviceLogger()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel, p.done = cancel, make(chan struct{})
	go func() {
		defer close(p.done)
		logger.Printf("pawabase-connector %s: connecting to %s for %s", tunnel.Version, cfg.Server, cfg.RedactedDatabase())
		if err := tunnel.Run(ctx, cfg, logger); err != nil {
			logger.Printf("stopped: %v", err)
		}
	}()
	return nil
}

func (p *program) Stop(s service.Service) error {
	if p.cancel != nil {
		p.cancel()
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
		}
	}
	return nil
}

// serviceLogger writes to connector.log next to the configuration (a service has no terminal).
func serviceLogger() (*log.Logger, error) {
	if service.Interactive() {
		return log.New(os.Stderr, "", log.LstdFlags), nil
	}
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(config.Dir(), "connector.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return log.New(f, "", log.LstdFlags), nil
}

func newService() (service.Service, error) {
	return service.New(&program{}, &service.Config{
		Name:        "pawabase-connector",
		DisplayName: "Pawabase database connector",
		Description: "Lets a Pawabase project use this database without Pawabase holding its password.",
		Arguments:   []string{"service", "run"},
		EnvVars:     map[string]string{"PAWABASE_CONNECTOR_CONFIG": config.Path()},
		Option:      service.KeyValue{"Restart": "always", "RestartSec": 5, "KeepAlive": true, "RunAtLoad": true},
	})
}

func serviceCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("service install|uninstall|start|stop|restart|status")
	}
	svc, err := newService()
	if err != nil {
		return err
	}
	switch args[0] {
	case "run":
		return svc.Run()
	case "status":
		st, err := svc.Status()
		if err != nil {
			return err
		}
		fmt.Println(map[service.Status]string{service.StatusRunning: "running", service.StatusStopped: "stopped"}[st])
		return nil
	case "install":
		if _, err := config.Load(); err != nil {
			return err
		}
		if err := svc.Install(); err != nil {
			return fmt.Errorf("install the service (run as root or Administrator): %w", err)
		}
		if err := svc.Start(); err != nil {
			return err
		}
		fmt.Printf("Installed and started. Logs: %s\n", filepath.Join(config.Dir(), "connector.log"))
		return nil
	case "uninstall":
		_ = svc.Stop()
		return svc.Uninstall()
	case "start", "stop", "restart":
		return service.Control(svc, args[0])
	}
	return fmt.Errorf("unknown service command %q", args[0])
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// problemText is the message of an error answer: a bare JSON string, {"detail": …}, {"message": …}, or the raw body.
func problemText(raw []byte) string {
	var text string
	if json.Unmarshal(raw, &text) == nil && text != "" {
		return text
	}
	var problem struct {
		Detail  any    `json:"detail"`
		Message string `json:"message"`
		Error   any    `json:"error"`
	}
	if json.Unmarshal(raw, &problem) == nil {
		for _, v := range []any{problem.Message, problem.Detail, problem.Error} {
			if s := fmt.Sprint(v); v != nil && s != "" {
				return s
			}
		}
	}
	return strings.TrimSpace(string(raw))
}
