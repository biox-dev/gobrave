package mcp

import (
	"reflect"
	"testing"

	"github.com/biox-dev/gobrave/internal/config"
)

func TestServersFromConfig(t *testing.T) {
	disabled := false
	cfg := &config.MCPConfig{
		Enabled: true,
		Servers: map[string]config.MCPServerConfig{
			"fs": {
				Command: "npx",
				Args:    []string{"-y", "pkg"},
				Env:     map[string]string{"B": "2", "A": "1"},
			},
			"remote": {
				URL:     "http://127.0.0.1:3001/sse",
				Headers: map[string]string{"Authorization": "Bearer t"},
			},
			"off": {
				Enabled: &disabled,
				Command: "npx",
			},
		},
	}

	got := ServersFromConfig(cfg)
	if len(got) != 2 {
		t.Fatalf("server count = %d, want 2 (%#v)", len(got), got)
	}

	fs := got["fs"]
	if fs.Type != TypeStdio {
		t.Fatalf("fs type = %q, want stdio", fs.Type)
	}
	if !reflect.DeepEqual(fs.Env, []string{"A=1", "B=2"}) {
		t.Fatalf("fs env = %#v, want sorted KEY=VALUE", fs.Env)
	}

	remote := got["remote"]
	if remote.Type != TypeSSE {
		t.Fatalf("remote type = %q, want sse", remote.Type)
	}
	if remote.Headers["Authorization"] != "Bearer t" {
		t.Fatalf("remote headers = %#v", remote.Headers)
	}

	if _, ok := got["off"]; ok {
		t.Fatalf("disabled server should be skipped")
	}
}

func TestServersFromConfigDisabled(t *testing.T) {
	cfg := &config.MCPConfig{
		Enabled: false,
		Servers: map[string]config.MCPServerConfig{
			"fs": {Command: "npx"},
		},
	}
	if got := ServersFromConfig(cfg); len(got) != 0 {
		t.Fatalf("expected no servers when disabled, got %#v", got)
	}
	if got := ServersFromConfig(nil); len(got) != 0 {
		t.Fatalf("expected no servers for nil config, got %#v", got)
	}
}
