package doris

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestFlattenAttrs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{
			name: "empty object",
			raw:  `{}`,
			want: map[string]string{},
		},
		{
			name: "flat string keys",
			raw:  `{"env":"prod","region":"eu"}`,
			want: map[string]string{"env": "prod", "region": "eu"},
		},
		{
			name: "nested object re-joined with dots (the Doris shape)",
			raw:  `{"service":{"name":"nginx"},"host":{"name":"node-1"}}`,
			want: map[string]string{"service.name": "nginx", "host.name": "node-1"},
		},
		{
			name: "deeply nested",
			raw:  `{"a":{"b":{"c":"deep"}}}`,
			want: map[string]string{"a.b.c": "deep"},
		},
		{
			name: "integer value",
			raw:  `{"http":{"status":200}}`,
			want: map[string]string{"http.status": "200"},
		},
		{
			name: "float value formatted without trailing zeros",
			raw:  `{"ratio":1.5}`,
			want: map[string]string{"ratio": "1.5"},
		},
		{
			name: "bool value",
			raw:  `{"tls":{"enabled":true}}`,
			want: map[string]string{"tls.enabled": "true"},
		},
		{
			name: "null value is skipped",
			raw:  `{"present":"yes","absent":null}`,
			want: map[string]string{"present": "yes"},
		},
		{
			name: "array value marshalled back to JSON",
			raw:  `{"tags":["a","b"]}`,
			want: map[string]string{"tags": `["a","b"]`},
		},
		{
			name: "malformed JSON falls back to empty map",
			raw:  `not-valid-json`,
			want: map[string]string{},
		},
		{
			name: "JSON null document falls back to empty map",
			raw:  `null`,
			want: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := flattenAttrs(tt.raw)
			if got == nil {
				t.Fatal("flattenAttrs must never return nil")
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("flattenAttrs(%q)\n  got  %v\n  want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestAttrConds(t *testing.T) {
	t.Run("nil and empty produce no predicates", func(t *testing.T) {
		if conds, args := attrConds(nil); conds != nil || args != nil {
			t.Fatalf("nil attrs: want no conds/args, got %v / %v", conds, args)
		}
		if conds, args := attrConds(map[string]string{}); conds != nil || args != nil {
			t.Fatalf("empty attrs: want no conds/args, got %v / %v", conds, args)
		}
	})

	t.Run("valid key is pushed down as parameterised predicate", func(t *testing.T) {
		conds, args := attrConds(map[string]string{"service.name": "nginx"})
		if len(conds) != 1 || len(args) != 1 {
			t.Fatalf("want 1 cond/arg, got %d / %d", len(conds), len(args))
		}
		if want := "CAST(attributes['service.name'] AS STRING) = ?"; conds[0] != want {
			t.Fatalf("cond:\n  got  %q\n  want %q", conds[0], want)
		}
		if args[0] != "nginx" {
			t.Fatalf("arg: want nginx, got %v", args[0])
		}
	})

	t.Run("dotted, colon, slash and hyphen keys are allowed", func(t *testing.T) {
		attrs := map[string]string{
			"service.name": "a",
			"k8s.pod:name": "b",
			"path/like":    "c",
			"with-hyphen":  "d",
			"under_score":  "e",
			"MixedCase123": "f",
		}
		conds, args := attrConds(attrs)
		if len(conds) != len(attrs) || len(args) != len(attrs) {
			t.Fatalf("want %d conds/args, got %d / %d", len(attrs), len(conds), len(args))
		}
		for _, c := range conds {
			if !strings.HasPrefix(c, "CAST(attributes['") || !strings.HasSuffix(c, "'] AS STRING) = ?") {
				t.Fatalf("unexpected predicate shape: %q", c)
			}
		}
	})

	t.Run("injection-unsafe keys are not pushed down", func(t *testing.T) {
		// Keys with quotes, spaces, or brackets must never reach the SQL string;
		// the in-Go attrsMatch remains the correctness net for them.
		unsafe := map[string]string{
			"a'; DROP TABLE telemetry;--": "x",
			"has space":                   "y",
			"bracket]":                    "z",
			"":                            "w",
		}
		conds, args := attrConds(unsafe)
		if conds != nil || args != nil {
			t.Fatalf("unsafe keys must be skipped, got conds=%v args=%v", conds, args)
		}
	})

	t.Run("mixed safe and unsafe keys pushes only the safe ones", func(t *testing.T) {
		conds, args := attrConds(map[string]string{
			"service.name": "nginx",
			"bad key":      "ignored",
		})
		if len(conds) != 1 || len(args) != 1 {
			t.Fatalf("want only the safe key pushed, got %d conds / %d args", len(conds), len(args))
		}
		if args[0] != "nginx" {
			t.Fatalf("arg: want nginx, got %v", args[0])
		}
	})
}

func TestDSN(t *testing.T) {
	got := dsn(Params{
		Addr:     "127.0.0.1:9030",
		User:     "root",
		Password: "secret",
	}, "aos")
	if got == "" {
		t.Fatal("want non-empty DSN")
	}
	for _, want := range []string{"127.0.0.1:9030", "root", "aos"} {
		if !strings.Contains(got, want) {
			t.Errorf("DSN does not contain %q: %s", want, got)
		}
	}
}

// TestConnect_cancelledContext verifies that Connect returns quickly when the
// context is already cancelled and the database is unreachable. It exercises
// Connect's retry-then-bail path and openDB's connection-refused path without
// requiring a running database instance.
func TestConnect_cancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so the retry loop exits after the first openDB failure

	_, err := Connect(ctx, Params{
		Addr:     "127.0.0.1:1", // loopback, nothing listens on port 1 — immediate ECONNREFUSED
		User:     "root",
		Password: "",
		DB:       "test",
	}, nil)
	if err == nil {
		t.Fatal("expected error from cancelled context or unreachable host")
	}
}
