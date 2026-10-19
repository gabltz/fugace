// Command fugace is an ephemeral, log-free, IRC-compatible chat server.
//
// This is the only package allowed to write to stdout/stderr: one startup
// line and fatal startup errors, never any user data.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/gabltz/fugace/internal/config"
	"github.com/gabltz/fugace/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// Process exit codes.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2 // invalid configuration or command line
)

const (
	shutdownTimeout    = 5 * time.Second
	healthcheckTimeout = 2 * time.Second
)

func main() {
	// An unexpected crash must not dump goroutine stacks (they may hold user data).
	debug.SetTraceback("none")
	os.Exit(run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

func run(args []string, lookup config.LookupFunc, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		usage(stderr)
		return exitUsage
	}
	cmd := ""
	if len(args) == 1 {
		cmd = args[0]
	}
	if cmd == "version" {
		_, _ = fmt.Fprintln(stdout, "fugace", version)
		return exitOK
	}
	if cmd != "" && cmd != "healthcheck" {
		usage(stderr)
		return exitUsage
	}

	cfg, err := config.Load(lookup)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fugace: invalid configuration:\n%v\n", err)
		return exitUsage
	}

	if cmd == "healthcheck" {
		// Silent by design: the exit code is the whole answer.
		if healthcheck(cfg.HTTPAddr) != nil {
			return exitFailure
		}
		return exitOK
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := listen(cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fugace: %v\n", err)
		return exitFailure
	}
	_, _ = fmt.Fprintf(stdout, "fugace %s http=%s tls=%s\n", version, a.httpLn.Addr(), cfg.TLS)
	if err := a.serve(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "fugace: %v\n", err)
		return exitFailure
	}
	return exitOK
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: fugace [healthcheck|version]")
}

type app struct {
	httpLn  net.Listener
	httpSrv *http.Server
}

func listen(cfg *config.Config) (*app, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", cfg.HTTPAddr)
	if err != nil {
		return nil, fmt.Errorf("listen http: %w", err)
	}
	return &app{httpLn: ln, httpSrv: web.NewServer(web.NewHandler())}, nil
}

// serve blocks until ctx is done, then shuts down within shutdownTimeout.
func (a *app) serve(ctx context.Context) error {
	errc := make(chan error, 1)
	go func() { errc <- a.httpSrv.Serve(a.httpLn) }()

	select {
	case err := <-errc:
		return fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := a.httpSrv.Shutdown(sctx); err != nil {
		// Deadline reached: drop the remaining connections.
		_ = a.httpSrv.Close()
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve http: %w", err)
	}
	return nil
}

// healthURL derives a loopback URL for the local HTTP listener.
func healthURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("parse address: %w", err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + web.HealthPath, nil
}

func healthcheck(addr string) error {
	u, err := healthURL(addr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(len(web.HealthBody))+1))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != web.HealthBody {
		return fmt.Errorf("unhealthy: status %d", resp.StatusCode)
	}
	return nil
}
