package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/ocg-connector/server"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig(nil, env(map[string]string{"DORIS_PASSWORD": "secret"}))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.listen != "127.0.0.1:8080" {
		t.Errorf("listen = %q", cfg.listen)
	}
	if cfg.doris.Addr != "127.0.0.1:9030" || cfg.doris.User != "root" || cfg.doris.DB != "aos" {
		t.Errorf("doris defaults = %+v", cfg.doris)
	}
	if cfg.doris.Password != "secret" {
		t.Error("password not taken from env")
	}
	if cfg.reflection {
		t.Error("reflection should default false")
	}
	if len(cfg.logSources) != 0 {
		t.Errorf("logSources = %v, want none", cfg.logSources)
	}
}

func TestParseConfigFlags(t *testing.T) {
	cfg, err := parseConfig([]string{
		"-listen", "0.0.0.0:9000",
		"-doris-addr", "db:9030", "-doris-user", "reader", "-doris-db", "sat",
		"-reflection",
	}, env(nil))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.listen != "0.0.0.0:9000" || cfg.doris.Addr != "db:9030" ||
		cfg.doris.User != "reader" || cfg.doris.DB != "sat" || !cfg.reflection {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestParseConfigLogSources(t *testing.T) {
	cfg, err := parseConfig([]string{
		"-log-source", "satellite:slog=/var/log/satellite.log",
		"-log-source", "doris:log4j=/var/log/doris.log",
	}, env(nil))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.logSources) != 2 {
		t.Fatalf("logSources = %d, want 2", len(cfg.logSources))
	}
	if cfg.logSources[0].Component != "satellite" || cfg.logSources[1].Path != "/var/log/doris.log" {
		t.Errorf("logSources = %+v", cfg.logSources)
	}
	if cfg.logSources[0].ParseTime == nil || cfg.logSources[1].ParseTime == nil {
		t.Error("parsers should be set")
	}
}

func TestParseConfigBadLogSource(t *testing.T) {
	for _, bad := range []string{"noequals", "=/path", "component=", "noformat=/path", "app:unknown=/path"} {
		if _, err := parseConfig([]string{"-log-source", bad}, env(nil)); err == nil {
			t.Errorf("parseConfig(%q): expected error", bad)
		}
	}
}

func TestLogSourceFlagString(t *testing.T) {
	var f logSourceFlag
	if err := f.Set("a:slog=/x"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if f.String() != "1 sources" {
		t.Errorf("String() = %q", f.String())
	}
}

func TestServeGracefulShutdown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serve(ctx, "127.0.0.1:0", true, server.New(nil, nil, logger), logger); err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestServeListenError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := serve(context.Background(), "127.0.0.1", false, server.New(nil, nil, logger), logger); err == nil {
		t.Fatal("expected listen error for addr without port")
	}
}
