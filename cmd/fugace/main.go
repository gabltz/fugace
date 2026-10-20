// Command fugace is an ephemeral, log-free, IRC-compatible chat server.
//
// This is the only package allowed to write to stdout/stderr: one startup
// line and fatal startup errors, never any user data.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gabltz/fugace/internal/config"
	"github.com/gabltz/fugace/internal/files"
	"github.com/gabltz/fugace/internal/limits"
	"github.com/gabltz/fugace/internal/server"
	"github.com/gabltz/fugace/internal/status"
	"github.com/gabltz/fugace/internal/tlsmgr"
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
	switch cmd {
	case "version":
		_, _ = fmt.Fprintln(stdout, "fugace", version)
		return exitOK
	case "healthcheck":
		// Silent by design: the exit code is the whole answer.
		ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
		defer cancel()
		if status.Healthy(ctx, status.Addr) != nil {
			return exitFailure
		}
		return exitOK
	case "status":
		ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
		defer cancel()
		r, err := status.Fetch(ctx, status.Addr)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "fugace: status unavailable (is the server running?)")
			return exitFailure
		}
		_, _ = io.WriteString(stdout, status.Format(r))
		return exitOK
	case "":
	default:
		usage(stderr)
		return exitUsage
	}

	cfg, err := config.Load(lookup)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fugace: invalid configuration:\n%v\n", err)
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := listen(cfg, status.Addr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fugace: %v\n", err)
		return exitFailure
	}
	_, _ = fmt.Fprintf(stdout, "fugace %s %s tls=%s\n", version, a.addrs(), a.tlsMode())
	if err := a.serve(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "fugace: %v\n", err)
		return exitFailure
	}
	return exitOK
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: fugace [healthcheck|status|version]")
}

// httpService is one HTTP(S) listener.
type httpService struct {
	name string
	ln   net.Listener
	srv  *http.Server
}

type app struct {
	cfg    *config.Config
	files  *files.Store
	chat   *server.Server
	tls    *tlsmgr.Manager // nil when FUGACE_TLS=off
	http   []httpService   // http, https, status
	irc    []net.Listener  // irc, ircs (when enabled)
	ircTag []string
}

// listen opens every listener; on error, those already open are closed.
func listen(cfg *config.Config, statusAddr string) (_ *app, err error) {
	a := &app{cfg: cfg, files: files.NewStore(cfg.FilesMaxBytes, cfg.FileTTL, nil)}
	tokens := files.NewTokens(limits.UploadBurst, limits.UploadInterval)
	avatars := files.NewAvatars()
	defer func() {
		if err != nil {
			a.closeListeners() // a, not the nil being returned
		}
	}()
	if a.chat, err = server.New(server.Options{
		Version:         version,
		Password:        cfg.Password,
		AdminPassword:   cfg.AdminPassword,
		Channels:        cfg.Channels,
		HistoryMaxBytes: cfg.HistoryMaxBytes,
		Uploads:         tokens,
		Account:         accountHooks{files: a.files, avatars: avatars},
	}); err != nil {
		return nil, fmt.Errorf("chat server: %w", err)
	}
	static, err := web.NewStatic(webui.Static, "static")
	if err != nil {
		return nil, fmt.Errorf("web client: %w", err)
	}
	fileHandler := files.NewHandler(files.HandlerOptions{
		Store: a.files, Tokens: tokens, MaxUpload: cfg.MaxUploadBytes, PublicURL: publicURL(cfg),
	})
	avatarHandler := files.NewAvatarHandler(fileHandler, avatars, a.chat.OwnerOf, a.chat.AvatarChanged)
	appHandler := web.NewHandler(web.Options{
		Static: static, WS: transport.WSHandler(a.chat, cfg.TrustedProxies), Files: fileHandler, Avatar: avatarHandler,
	})

	if cfg.TLS == config.TLSOff {
		if err := a.addHTTP("http", cfg.HTTPAddr, appHandler, nil); err != nil {
			return nil, err
		}
	} else {
		if a.tls, err = newTLS(cfg); err != nil {
			return nil, err
		}
		redirect := web.NewRedirectHandler(web.RedirectOptions{Challenge: a.tls.HTTPChallenge, PublicURL: cfg.PublicURL})
		if err := a.addHTTP("http", cfg.HTTPAddr, redirect, nil); err != nil {
			return nil, err
		}
		if err := a.addHTTP("https", cfg.HTTPSAddr, web.HSTS(a.tls.Valid, appHandler), a.tls.HTTPSConfig()); err != nil {
			return nil, err
		}
	}
	if err := a.addIRC("irc", cfg.IRCAddr, nil); err != nil {
		return nil, err
	}
	if a.tls != nil {
		if err := a.addIRC("ircs", cfg.IRCSAddr, a.tls.IRCConfig()); err != nil {
			return nil, err
		}
	}
	if err := a.addHTTP("status", statusAddr, status.Handler(a.report), nil); err != nil {
		return nil, err
	}
	return a, nil
}

// accountHooks connects the chat core to the files and pictures an author
// owns (ADR 0012).
type accountHooks struct {
	files   *files.Store
	avatars *files.Avatars
}

func (h accountHooks) AvatarVersion(owner string) int { return h.avatars.Version(owner) }

func (h accountHooks) Cleared(owner string) {
	h.files.DeleteOwner(owner)
	h.avatars.Delete(owner)
}

// Gone drops the picture of an author whose last connection ended; their
// shared files keep their normal lifetime.
func (h accountHooks) Gone(owner string) { h.avatars.Delete(owner) }

// publicURL is FUGACE_PUBLIC_URL, else https://<first FUGACE_HOST> when TLS
// is on, else nil (the request Host is used).
func publicURL(cfg *config.Config) *url.URL {
	if cfg.PublicURL != nil {
		return cfg.PublicURL
	}
	if len(cfg.Hosts) > 0 && cfg.TLS != config.TLSOff {
		h := cfg.Hosts[0].String()
		if cfg.Hosts[0].IsIP() && cfg.Hosts[0].IP.Is6() {
			h = "[" + h + "]"
		}
		return &url.URL{Scheme: "https", Host: h}
	}
	return nil
}

func newTLS(cfg *config.Config) (*tlsmgr.Manager, error) {
	opts := tlsmgr.Options{CacheDir: cfg.CertCache}
	if cfg.ACMEDirectory != nil {
		opts.ACMEDirectory = cfg.ACMEDirectory.String()
	}
	for _, h := range cfg.Hosts {
		if h.IsIP() {
			opts.IPs = append(opts.IPs, h.IP) // self-signed SAN only, never ACME (ADR 0007)
		} else {
			opts.Domains = append(opts.Domains, h.Name)
		}
	}
	m, err := tlsmgr.New(opts)
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	return m, nil
}

func (a *app) addHTTP(name, addr string, h http.Handler, tlsCfg *tls.Config) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", name, err)
	}
	if name != "status" {
		// Per-source cap before TLS: a stalled handshake counts too (T7).
		trusted := a.cfg.TrustedProxies
		exempt := func(ip netip.Addr) bool {
			return slices.ContainsFunc(trusted, func(p netip.Prefix) bool { return p.Contains(ip) })
		}
		lim, err := limits.NewListener(ln, limits.MaxHTTPConns, limits.MaxHTTPConnsPerSource, exempt)
		if err != nil {
			_ = ln.Close()
			return fmt.Errorf("listen %s: %w", name, err)
		}
		ln = lim
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	a.http = append(a.http, httpService{name: name, ln: ln, srv: web.NewServer(h)})
	return nil
}

func (a *app) addIRC(name, addr string, tlsCfg *tls.Config) error {
	if addr == "" {
		return nil // disabled
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", name, err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	a.irc = append(a.irc, ln)
	a.ircTag = append(a.ircTag, name)
	return nil
}

func (a *app) closeListeners() {
	for _, s := range a.http {
		_ = s.ln.Close()
	}
	for _, ln := range a.irc {
		_ = ln.Close()
	}
}

func (a *app) tlsMode() string {
	if a.tls == nil {
		return string(config.TLSOff)
	}
	return string(a.tls.Status().Mode)
}

func (a *app) report() status.Report {
	r := status.Report{Version: version, TLSMode: a.tlsMode(), Connections: a.chat.ConnCount()}
	if a.tls != nil {
		st := a.tls.Status()
		r.CertNotAfter = &st.NotAfter
		r.TrustedCert = st.Valid
		r.ACMELastError = st.LastError
		if !st.NextRetry.IsZero() {
			r.ACMENextRetry = &st.NextRetry
		}
	}
	return r
}

// addrs describes the listeners for the startup line (no user data).
func (a *app) addrs() string {
	var parts []string
	for _, s := range a.http {
		if s.name != "status" {
			parts = append(parts, s.name+"="+s.ln.Addr().String())
		}
	}
	for i, ln := range a.irc {
		parts = append(parts, a.ircTag[i]+"="+ln.Addr().String())
	}
	return strings.Join(parts, " ")
}

// serve blocks until ctx is done (or a listener fails), then shuts down
// within shutdownTimeout.
func (a *app) serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errc := make(chan error, len(a.http))
	for _, s := range a.http {
		wg.Go(func() {
			if err := s.srv.Serve(s.ln); !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("serve %s: %w", s.name, err)
			}
		})
	}
	for _, ln := range a.irc {
		wg.Go(func() { _ = transport.ServeIRC(ctx, ln, a.chat) }) // returns once ctx is done
	}
	if a.tls != nil {
		wg.Go(func() { a.tls.Run(ctx) })
	}
	wg.Go(func() { a.files.Run(ctx) })

	var failure error
	select {
	case failure = <-errc:
	case <-ctx.Done():
	}
	cancel()

	sctx, scancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer scancel()
	_ = a.chat.Shutdown(sctx) // deadline reached: remaining clients were aborted
	for _, s := range a.http {
		if err := s.srv.Shutdown(sctx); err != nil {
			_ = s.srv.Close() // deadline reached: drop the remaining connections
		}
	}
	wg.Wait()
	return failure
}
