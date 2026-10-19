package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gabltz/fugace/internal/config"
)

func noEnv(string) (string, bool) { return "", false }

func TestRunCommands(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		env        map[string]string
		code       int
		stdout     string
		stderrPart string
	}{
		{"version", []string{"version"}, nil, exitOK, "fugace dev\n", ""},
		{"unknown command", []string{"nope"}, nil, exitUsage, "", "usage:"},
		{"too many args", []string{"healthcheck", "x"}, nil, exitUsage, "", "usage:"},
		{"invalid config", []string{"healthcheck"}, map[string]string{config.EnvTLS: "bogus"}, exitUsage, "", config.EnvTLS},
		{"healthcheck nobody listening", []string{"healthcheck"}, map[string]string{config.EnvHTTPAddr: "127.0.0.1:1"}, exitFailure, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			lookup := func(k string) (string, bool) { v, ok := tt.env[k]; return v, ok }
			if code := run(tt.args, lookup, &out, &errb); code != tt.code {
				t.Fatalf("code = %d, want %d (stderr %q)", code, tt.code, errb.String())
			}
			if out.String() != tt.stdout {
				t.Errorf("stdout = %q, want %q", out.String(), tt.stdout)
			}
			if !strings.Contains(errb.String(), tt.stderrPart) {
				t.Errorf("stderr = %q, want it to contain %q", errb.String(), tt.stderrPart)
			}
			if tt.stderrPart == "" && errb.Len() != 0 {
				t.Errorf("unexpected stderr %q", errb.String())
			}
		})
	}
}

func TestHealthURL(t *testing.T) {
	tests := []struct {
		addr, want string
		ok         bool
	}{
		{":8080", "http://127.0.0.1:8080/healthz", true},
		{"0.0.0.0:80", "http://127.0.0.1:80/healthz", true},
		{"[::]:80", "http://127.0.0.1:80/healthz", true},
		{"[::1]:8080", "http://[::1]:8080/healthz", true},
		{"10.1.2.3:8080", "http://10.1.2.3:8080/healthz", true},
		{"nope", "", false},
	}
	for _, tt := range tests {
		got, err := healthURL(tt.addr)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("healthURL(%q) = %q, %v", tt.addr, got, err)
		}
	}
}

func TestServeHealthcheckShutdown(t *testing.T) {
	cfg, err := config.Load(noEnv)
	if err != nil {
		t.Fatal(err)
	}
	cfg.HTTPAddr = "127.0.0.1:0"
	a, err := listen(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.serve(ctx) }()

	if err := healthcheck(a.httpLn.Addr().String()); err != nil {
		t.Fatalf("healthcheck: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(shutdownTimeout + time.Second):
		t.Fatal("shutdown took too long")
	}
	if err := healthcheck(a.httpLn.Addr().String()); err == nil {
		t.Fatal("healthcheck should fail after shutdown")
	}
}

func TestListenError(t *testing.T) {
	cfg := &config.Config{HTTPAddr: "256.0.0.1:1"}
	if _, err := listen(cfg); err == nil {
		t.Fatal("expected listen error")
	}
}
