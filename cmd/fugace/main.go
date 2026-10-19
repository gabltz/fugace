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
	"github.com/gabltz/fugace/internal/server"
	"github.com/gabltz/fugace/internal/transport"
	"github.com/gabltz/fugace/internal/web"
	webui "github.com/gabltz/fugace/web"
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
	_, _ = fmt.Fprintf(stdout, "fugace %s %s tls=%s\n", version, a.addrs(), cfg.TLS)
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
	ircLn   net.Listener // nil when disabled
	chat    *server.Server
}

func listen(cfg *config.Config) (*app, error) {
	chat, err := server.New(server.Options{
		Version:         version,
		Password:        cfg.Password,
		AdminPassword:   cfg.AdminPassword,
		Channels:        cfg.Channels,
		HistoryMaxBytes: cfg.HistoryMaxBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("chat server: %w", err)
	}
	var lc net.ListenConfig
	httpLn, err := lc.Listen(context.Background(), "tcp", cfg.HTTPAddr)
	if err != nil {
		return nil, fmt.Errorf("listen http: %w", err)
	}
	static, err := web.NewStatic(webui.Static, "static")
	if err != nil {
		_ = httpLn.Close()
		return nil, fmt.Errorf("web client: %w", err)
	}
	handler := web.NewHandler(web.Options{Static: static, WS: transport.WSHandler(chat)})
	a := &app{httpLn: httpLn, httpSrv: web.NewServer(handler), chat: chat}
	if cfg.IRCAddr != "" {
		if a.ircLn, err = lc.Listen(context.Background(), "tcp", cfg.IRCAddr); err != nil {
			_ = httpLn.Close()
			return nil, fmt.Errorf("listen irc: %w", err)
		}
	}
	return a, nil
}

// addrs describes the listeners for the startup line (no user data).
func (a *app) addrs() string {
	out := "http=" + a.httpLn.Addr().String()
	if a.ircLn != nil {
		out += " irc=" + a.ircLn.Addr().String()
	}
	return out
}

// serve blocks until ctx is done, then shuts down within shutdownTimeout.
func (a *app) serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	httpErr := make(chan error, 1)
	go func() { httpErr <- a.httpSrv.Serve(a.httpLn) }()
	ircDone := make(chan struct{})
	go func() {
		defer close(ircDone)
		if a.ircLn != nil {
			_ = transport.ServeIRC(ctx, a.ircLn, a.chat) // returns once ctx is done
		}
	}()

	var failure error
	select {
	case err := <-httpErr:
		failure = fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
	}
	cancel()
	<-ircDone

	sctx, scancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer scancel()
	_ = a.chat.Shutdown(sctx) // deadline reached: remaining clients were aborted
	if err := a.httpSrv.Shutdown(sctx); err != nil {
		_ = a.httpSrv.Close() // deadline reached: drop the remaining connections
	}
	if failure != nil {
		return failure
	}
	if err := <-httpErr; !errors.Is(err, http.ErrServerClosed) {
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
