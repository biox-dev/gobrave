package config

import "testing"

func TestNormalizeMCPConfig(t *testing.T) {
	disabled := false
	cfg := &MCPConfig{
		Servers: map[string]MCPServerConfig{
			" fs ": {
				Command: " npx ",
				Args:    []string{"-y", "pkg"},
			},
			"remote": {
				URL: " http://127.0.0.1:3001/sse ",
			},
			"off": {
				Enabled: &disabled,
			},
		},
	}

	if err := normalizeMCPConfig(cfg); err != nil {
		t.Fatalf("normalizeMCPConfig() error = %v", err)
	}
	if cfg.TimeoutSeconds != DefaultMCPTimeoutSeconds {
		t.Fatalf("timeout = %d, want %d", cfg.TimeoutSeconds, DefaultMCPTimeoutSeconds)
	}

	fs, ok := cfg.Servers["fs"]
	if !ok {
		t.Fatalf("server name was not trimmed: %#v", cfg.Servers)
	}
	if fs.Type != "stdio" {
		t.Fatalf("fs type = %q, want stdio", fs.Type)
	}
	if fs.Command != "npx" {
		t.Fatalf("fs command = %q, want npx", fs.Command)
	}

	remote := cfg.Servers["remote"]
	if remote.Type != "sse" {
		t.Fatalf("remote type = %q, want sse", remote.Type)
	}
	if remote.URL != "http://127.0.0.1:3001/sse" {
		t.Fatalf("remote url = %q", remote.URL)
	}

	if off, ok := cfg.Servers["off"]; !ok || off.Type != "stdio" {
		t.Fatalf("disabled server should be kept with inferred type, got %#v", off)
	}
}

func TestNormalizeMCPConfigRejectsInvalidServers(t *testing.T) {
	cases := []struct {
		name   string
		server MCPServerConfig
	}{
		{name: "stdio without command", server: MCPServerConfig{Type: "stdio"}},
		{name: "sse without url", server: MCPServerConfig{Type: "sse"}},
		{name: "unsupported type", server: MCPServerConfig{Type: "ws", Command: "npx"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &MCPConfig{Servers: map[string]MCPServerConfig{"bad": tc.server}}
			if err := normalizeMCPConfig(cfg); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestMCPConfigTimeoutNilSafe(t *testing.T) {
	var cfg *MCPConfig
	if got := cfg.Timeout().Seconds(); got != DefaultMCPTimeoutSeconds {
		t.Fatalf("nil timeout = %v, want %d", got, DefaultMCPTimeoutSeconds)
	}
	cfg = &MCPConfig{TimeoutSeconds: 5}
	if got := cfg.Timeout().Seconds(); got != 5 {
		t.Fatalf("timeout = %v, want 5", got)
	}
}
