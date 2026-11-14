package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRegisteredSectionsExposeEffectiveValues(t *testing.T) {
	cfg := &Config{
		Database:  &DatabaseConfig{Driver: "mysql", Host: "db", Port: "3306", User: "root", Name: "brave"},
		Container: DefaultContainerConfig(),
	}

	for _, key := range []string{"database", "container"} {
		section, ok := FindSection(key)
		if !ok {
			t.Fatalf("expected section %q to be registered", key)
		}
		if section.Effective(cfg) == nil {
			t.Fatalf("expected effective value for section %q", key)
		}
	}

	if _, ok := FindSection("not-exist"); ok {
		t.Fatalf("expected unknown section to be rejected")
	}
}

// TestSectionDecodeNormalizesDatabaseLogLevel 校验可视化配置保存 database 段时，
// log_level 会被归一化，非法值不会落盘。
func TestSectionDecodeNormalizesDatabaseLogLevel(t *testing.T) {
	section, ok := FindSection("database")
	if !ok {
		t.Fatal("database section is not registered")
	}

	cases := []struct {
		raw  string
		want string
	}{
		{`{"driver": "mysql", "host": "db", "port": "3306", "user": "root", "name": "brave"}`, DefaultDatabaseLogLevel},
		{`{"driver": "mysql", "host": "db", "port": "3306", "user": "root", "name": "brave", "log_level": ""}`, DefaultDatabaseLogLevel},
		{`{"driver": "mysql", "host": "db", "port": "3306", "user": "root", "name": "brave", "log_level": "INFO"}`, DatabaseLogLevelInfo},
		{`{"driver": "mysql", "host": "db", "port": "3306", "user": "root", "name": "brave", "log_level": "warning"}`, DatabaseLogLevelWarn},
		{`{"driver": "mysql", "host": "db", "port": "3306", "user": "root", "name": "brave", "log_level": "nonsense"}`, DefaultDatabaseLogLevel},
	}
	for _, tc := range cases {
		value, err := section.Decode(json.RawMessage(tc.raw))
		if err != nil {
			t.Fatalf("Decode(%s): %v", tc.raw, err)
		}
		db, ok := value.(*DatabaseConfig)
		if !ok {
			t.Fatalf("unexpected decode type %T", value)
		}
		if db.LogLevel != tc.want {
			t.Errorf("Decode(%s).LogLevel = %q, want %q", tc.raw, db.LogLevel, tc.want)
		}
	}
}

func TestSectionDecodeAndApplyContainer(t *testing.T) {
	section, ok := FindSection("container")
	if !ok {
		t.Fatalf("container section not registered")
	}

	cfg := &Config{Container: DefaultContainerConfig()}

	// 归一化：runtime 别名大写、重复项去重、清理策略兜底。
	raw := json.RawMessage(`{
		"runtimes": ["K8s", "docker", "k8s"],
		"default_runtime": "docker",
		"dag_node_cleanup_on_failed": "UNKNOWN",
		"dag_node_cleanup_on_dag_finished": "delete",
		"create_queue_max_concurrency": 5,
		"create_queue_max_pending": 20,
		"kubernetes": {"namespace": "  ", "kubeconfig": ""}
	}`)

	value, err := section.Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := section.Apply(cfg, value); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := cfg.Container.Runtimes; len(got) != 2 || got[0] != "k8s" || got[1] != "docker" {
		t.Fatalf("expected deduped runtimes [k8s docker], got %v", got)
	}
	if cfg.Container.DagNodeCleanupOnFailed != "stop" {
		t.Fatalf("expected invalid cleanup policy to fall back to stop, got %q", cfg.Container.DagNodeCleanupOnFailed)
	}
	if cfg.Container.Kubernetes.Namespace != "default" {
		t.Fatalf("expected empty namespace to fall back to default, got %q", cfg.Container.Kubernetes.Namespace)
	}
}

func TestSectionDecodeRejectsInvalidContainer(t *testing.T) {
	section, _ := FindSection("container")

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "default_runtime missing from runtimes",
			raw:  `{"default_runtime": "docker", "runtimes": ["k8s"]}`,
			want: "must be included in runtimes",
		},
		{
			name: "negative queue concurrency",
			raw:  `{"default_runtime": "docker", "runtimes": ["docker"], "create_queue_max_concurrency": -1}`,
			want: "create_queue_max_concurrency",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := section.Decode(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error to contain %q, got %q", tc.want, err.Error())
			}
		})
	}
}

// TestSectionDecodeContainerNetworkAndProvision 校验可视化配置保存 container 段时，
// network_mode 会被归一化/校验，docker.provision 会补齐默认值。
func TestSectionDecodeContainerNetworkAndProvision(t *testing.T) {
	section, ok := FindSection("container")
	if !ok {
		t.Fatalf("container section not registered")
	}

	value, err := section.Decode(json.RawMessage(`{
		"default_runtime": "docker",
		"runtimes": ["docker"],
		"network_mode": " HOST_PORT ",
		"docker": {"provision": {"enabled": true, "distro": "  ", "endpoint": "  "}}
	}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	cc, ok := value.(*ContainerConfig)
	if !ok {
		t.Fatalf("unexpected decode type %T", value)
	}
	if cc.NetworkMode != ContainerNetworkModeHostPort {
		t.Fatalf("expected host_port, got %q", cc.NetworkMode)
	}
	if cc.Docker == nil || cc.Docker.Provision == nil {
		t.Fatalf("expected docker.provision to be normalized")
	}
	if cc.Docker.Provision.Distro != DefaultProvisionDistro {
		t.Fatalf("expected empty distro to fall back to default, got %q", cc.Docker.Provision.Distro)
	}
	// Endpoint 不在 config 层注入默认值：留空表示由平台实现决定。
	if cc.Docker.Provision.Endpoint != "" {
		t.Fatalf("expected empty endpoint to stay empty, got %q", cc.Docker.Provision.Endpoint)
	}

	// docker 段整段缺失时也要补齐，避免落盘后变成空段。
	value, err = section.Decode(json.RawMessage(`{"default_runtime": "docker", "runtimes": ["docker"]}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if cc := value.(*ContainerConfig); cc.Docker == nil || cc.Docker.Provision == nil {
		t.Fatalf("expected default docker.provision when docker section is absent")
	}

	// 非法 network_mode 直接拒绝保存。
	if _, err := section.Decode(json.RawMessage(`{"network_mode": "bridge"}`)); err == nil {
		t.Fatalf("expected error for unsupported network_mode")
	}
}

func TestSectionDecodeRejectsUnknownDatabaseDriver(t *testing.T) {
	section, _ := FindSection("database")

	_, err := section.Decode(json.RawMessage(`{"driver": "oracle"}`))
	if err == nil {
		t.Fatalf("expected error for unsupported driver")
	}
	if !strings.Contains(err.Error(), "unsupported database driver") {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := section.Decode(json.RawMessage(`{"driver": "SQLite"}`)); err != nil {
		t.Fatalf("expected driver to be normalized to lowercase, got %v", err)
	}
}
