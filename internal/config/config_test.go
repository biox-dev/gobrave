package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeConfig 写入一个临时 config.yml，并把 CLI 配置路径指向它。
func writeConfig(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	withConfigPath(t, path)
}

// TestLoadConfigOnlyOverridesKeysPresentInFile 锁定合并语义：
// config.yml 中未出现的键必须保留默认值，只有显式出现的键才被覆盖。
func TestLoadConfigOnlyOverridesKeysPresentInFile(t *testing.T) {
	writeConfig(t, `server:
  port: 9099
container:
  default_runtime: k8s
llm:
  model: deepseek-v4-pro
`)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.Server.Port != 9099 {
		t.Errorf("port = %d, want 9099", cfg.Server.Port)
	}
	// server 段只声明了 port，其余字段应保持默认值。
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("server not merged: %+v", *cfg.Server)
	}
	if cfg.Server.ShutdownTimeout != 30*time.Second {
		t.Errorf("shutdown_timeout = %v, want 30s", cfg.Server.ShutdownTimeout)
	}
	// 文件中完全没有出现的段应保持默认值。
	if cfg.Database.Driver != "sqlite" || cfg.Database.SSLMode != "disable" {
		t.Errorf("database not defaulted: %+v", *cfg.Database)
	}
	if cfg.Realtime.Transport != "ws" || cfg.Realtime.AckMaxRetries != 3 {
		t.Errorf("realtime not defaulted: %+v", *cfg.Realtime)
	}
	if cfg.Proxy.BraveAPI != "http://localhost:5000" {
		t.Errorf("proxy not defaulted: %+v", *cfg.Proxy)
	}
	// container 段只声明了 default_runtime，布尔默认值不能被零值覆盖。
	if cfg.Container.DefaultRuntime != "k8s" {
		t.Errorf("default_runtime = %q, want k8s", cfg.Container.DefaultRuntime)
	}

	if cfg.Container.CreateQueueMaxConcurrency != 3 || cfg.Container.CreateQueueMaxPending != 50 {
		t.Errorf("container queue defaults lost: %+v", *cfg.Container)
	}
	if cfg.Container.Kubernetes.Namespace != "default" {
		t.Errorf("kubernetes not defaulted: %+v", *cfg.Container.Kubernetes)
	}
	// llm 段只声明了 model，provider/cli_url 默认值应保留。
	if cfg.LLM.Model != "deepseek-v4-pro" || cfg.LLM.CLIURL != "localhost:4321" {
		t.Errorf("llm not merged: %+v", *cfg.LLM)
	}
	if cfg.LLM.Provider == nil {
		t.Error("llm.provider should not be nil")
	}
}

// TestLoadConfigEmptySectionFallsBackToDefaults 校验空段（如 `container:`）回退到默认值。
func TestLoadConfigEmptySectionFallsBackToDefaults(t *testing.T) {
	writeConfig(t, "container:\nserver:\n")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Container == nil || cfg.Container.DefaultRuntime != "docker" {
		t.Fatalf("container = %+v, want defaults", cfg.Container)
	}
	if cfg.Container.CreateQueueMaxConcurrency != 3 {
		t.Errorf("create_queue_max_concurrency = %d, want 3", cfg.Container.CreateQueueMaxConcurrency)
	}
	if cfg.Server.Port != 8082 || cfg.Server.Host != "0.0.0.0" {
		t.Errorf("server = %+v, want defaults", *cfg.Server)
	}
}

// TestApplyConfigDataBlankFileKeepsDefaults 校验空文件 / 纯空白文件不修改默认值。
func TestApplyConfigDataBlankFileKeepsDefaults(t *testing.T) {
	for _, data := range []string{"", "   ", "\n# only a comment\n"} {
		cfg := defaultConfig()
		if err := applyConfigData(cfg, []byte(data)); err != nil {
			t.Fatalf("applyConfigData(%q): %v", data, err)
		}
		if cfg.Server.Port != 8082 || cfg.Server.Host != "0.0.0.0" {
			t.Errorf("data %q changed defaults: %+v", data, *cfg.Server)
		}
		if cfg.Container.DefaultRuntime != "docker" {
			t.Errorf("data %q changed container defaults: %+v", data, *cfg.Container)
		}
	}
}

// TestGitConfigDefaultsAndResolve 校验 git 段的默认值与身份解析回退逻辑。
func TestGitConfigDefaultsAndResolve(t *testing.T) {
	// 默认配置应带上内置的 git 身份。
	cfg := defaultConfig()
	if cfg.Git == nil {
		t.Fatal("defaultConfig().Git is nil")
	}
	if cfg.Git.User != DefaultGitUser || cfg.Git.Email != DefaultGitEmail {
		t.Fatalf("default git = %+v, want %s <%s>", *cfg.Git, DefaultGitUser, DefaultGitEmail)
	}

	// 空 git 段（yaml 里的 `git:`）应回退到默认值而不是 nil。
	cfg = defaultConfig()
	if err := applyConfigData(cfg, []byte("git:\n")); err != nil {
		t.Fatalf("applyConfigData: %v", err)
	}
	if cfg.Git == nil {
		t.Fatal("empty git section produced nil")
	}
	if cfg.Git.User != DefaultGitUser || cfg.Git.Email != DefaultGitEmail {
		t.Fatalf("empty git section = %+v, want defaults", *cfg.Git)
	}

	// 显式配置应按配置取值。
	cfg = defaultConfig()
	if err := applyConfigData(cfg, []byte("git:\n  user: alice\n  email: alice@example.com\n")); err != nil {
		t.Fatalf("applyConfigData: %v", err)
	}
	if name, email := ResolveGitIdentity(cfg); name != "alice" || email != "alice@example.com" {
		t.Fatalf("ResolveGitIdentity = %s <%s>, want alice <alice@example.com>", name, email)
	}

	// 部分字段缺失或仅含空白时逐项回退。
	cfg = defaultConfig()
	cfg.Git = &GitConfig{User: "  ", Email: ""}
	if name, email := ResolveGitIdentity(cfg); name != DefaultGitUser || email != DefaultGitEmail {
		t.Fatalf("ResolveGitIdentity fallback = %s <%s>, want defaults", name, email)
	}

	// nil 配置也要安全返回默认身份。
	if name, email := ResolveGitIdentity(nil); name != DefaultGitUser || email != DefaultGitEmail {
		t.Fatalf("ResolveGitIdentity(nil) = %s <%s>, want defaults", name, email)
	}
}

// TestResolveDatabaseLogLevel 校验 database.log_level 的默认值与归一化规则：
// 未配置 / 空值 / 非法值都收敛到 warn，别名 warning 视作 warn，大小写与空白被忽略。
func TestResolveDatabaseLogLevel(t *testing.T) {
	// 默认配置应为 warn。
	if got := ResolveDatabaseLogLevel(defaultConfig()); got != DatabaseLogLevelWarn {
		t.Fatalf("default level = %q, want %q", got, DatabaseLogLevelWarn)
	}

	cases := []struct {
		raw  string
		want string
	}{
		{"", DatabaseLogLevelWarn},
		{"  ", DatabaseLogLevelWarn},
		{"warn", DatabaseLogLevelWarn},
		{"WARN", DatabaseLogLevelWarn},
		{" warning ", DatabaseLogLevelWarn},
		{"info", DatabaseLogLevelInfo},
		{"Info", DatabaseLogLevelInfo},
		{"error", DatabaseLogLevelError},
		{"silent", DatabaseLogLevelSilent},
		{"verbose", DatabaseLogLevelWarn}, // 非法值回退
		{"trace", DatabaseLogLevelWarn},   // 非法值回退（GORM 无 trace 级别）
	}
	for _, tc := range cases {
		cfg := defaultConfig()
		cfg.Database.LogLevel = tc.raw
		if got := ResolveDatabaseLogLevel(cfg); got != tc.want {
			t.Errorf("ResolveDatabaseLogLevel(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}

	// nil / 缺段必须安全返回默认级别。
	if got := ResolveDatabaseLogLevel(nil); got != DefaultDatabaseLogLevel {
		t.Errorf("ResolveDatabaseLogLevel(nil) = %q, want %q", got, DefaultDatabaseLogLevel)
	}
	if got := ResolveDatabaseLogLevel(&Config{}); got != DefaultDatabaseLogLevel {
		t.Errorf("ResolveDatabaseLogLevel(no database) = %q, want %q", got, DefaultDatabaseLogLevel)
	}
}

// TestLoadConfigDatabaseLogLevel 校验 database.log_level 的 YAML 合并语义：
// 未声明时保留默认值，显式声明时按文件取值。
func TestLoadConfigDatabaseLogLevel(t *testing.T) {
	// 只声明 driver，log_level 应保持默认 warn。
	writeConfig(t, "database:\n  driver: sqlite\n")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Database.LogLevel != DefaultDatabaseLogLevel {
		t.Errorf("log_level = %q, want %q", cfg.Database.LogLevel, DefaultDatabaseLogLevel)
	}

	// 显式声明时按文件取值。
	writeConfig(t, "database:\n  driver: sqlite\n  log_level: info\n")
	cfg, err = LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := ResolveDatabaseLogLevel(cfg); got != DatabaseLogLevelInfo {
		t.Errorf("log_level = %q, want %q", got, DatabaseLogLevelInfo)
	}
}
