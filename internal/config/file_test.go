package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withConfigPath 把 CLI 配置路径指向临时文件，返回恢复函数。
func withConfigPath(t *testing.T, path string) {
	t.Helper()
	prev := cliFlags
	cliFlags = &CLIFlags{ConfigPath: path}
	t.Cleanup(func() { cliFlags = prev })
}

func TestSaveConfigSectionCreatesFileAndPreservesOtherSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.yml")
	withConfigPath(t, path)

	// 文件不存在时：LoadConfigFile 不报错，标记 Exists=false。
	doc, err := LoadConfigFile()
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if doc.Exists {
		t.Fatalf("expected Exists=false for missing file")
	}
	if doc.Section("database") != nil {
		t.Fatalf("expected nil section for missing file")
	}

	// 首次保存应创建文件及父目录。
	if _, err := SaveConfigSection("database", &DatabaseConfig{
		Driver: "postgres",
		Host:   "127.0.0.1",
		Port:   "5432",
		User:   "postgres",
		Name:   "gobrave",
	}); err != nil {
		t.Fatalf("SaveConfigSection (create): %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected config file to be created: %v", err)
	}

	// 已有文件时：只替换 database 段，其余键与原顺序保持。
	original := `server:
  port: 8084
  host: 0.0.0.0
realtime:
  transport: ws
  ack_max_retries: 3
database:
  driver: sqlite
  path: /tmp/gobrave.db
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, err := SaveConfigSection("database", &DatabaseConfig{
		Driver: "mysql",
		Host:   "db.internal",
		Port:   "3306",
		User:   "root",
		Name:   "brave",
	}); err != nil {
		t.Fatalf("SaveConfigSection (update): %v", err)
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	got := string(saved)

	// database 段被替换为新的驱动。
	if !strings.Contains(got, "driver: mysql") || strings.Contains(got, "driver: sqlite") {
		t.Fatalf("expected database section replaced, got:\n%s", got)
	}
	// 其他段的键顺序保持不变（未被 map 排序打乱）。
	serverIdx := strings.Index(got, "port: 8084")
	hostIdx := strings.Index(got, "host: 0.0.0.0")
	if serverIdx < 0 || hostIdx < 0 || serverIdx > hostIdx {
		t.Fatalf("expected server section key order preserved, got:\n%s", got)
	}
	transportIdx := strings.Index(got, "transport: ws")
	ackIdx := strings.Index(got, "ack_max_retries: 3")
	if transportIdx < 0 || ackIdx < 0 || transportIdx > ackIdx {
		t.Fatalf("expected realtime section key order preserved, got:\n%s", got)
	}
	// 顶层段顺序保持不变。
	realtimeIdx := strings.Index(got, "realtime:")
	databaseIdx := strings.Index(got, "database:")
	if realtimeIdx < 0 || databaseIdx < 0 || realtimeIdx > databaseIdx {
		t.Fatalf("expected top-level section order preserved, got:\n%s", got)
	}

	// 重新加载可以读回写入的值。
	doc, err = LoadConfigFile()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !doc.Exists {
		t.Fatalf("expected Exists=true after save")
	}
	if doc.Section("database") == nil {
		t.Fatalf("expected database section after save")
	}
}

func TestSaveConfigSectionKeepsUnmodeledKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	withConfigPath(t, path)

	// create_queue_enabled 是旧版本存在、当前结构体未建模的键，保存时不能被丢掉。
	original := `container:
  create_queue_enabled: true
  default_runtime: docker
  runtimes:
  - docker
  unknown_future_key: 42
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	container := DefaultContainerConfig()
	container.Runtimes = []string{"docker", "k8s"}
	if _, err := SaveConfigSection("container", container); err != nil {
		t.Fatalf("SaveConfigSection: %v", err)
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	got := string(saved)

	for _, want := range []string{"create_queue_enabled: true", "unknown_future_key: 42"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q to be preserved, got:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "- k8s") {
		t.Fatalf("expected new runtimes to be written, got:\n%s", got)
	}
}
