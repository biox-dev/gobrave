package wsl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeOutputUTF16LE(t *testing.T) {
	// "Ubuntu\r\n" 的 UTF-16LE 编码。
	raw := []byte{'U', 0, 'b', 0, 'u', 0, 'n', 0, 't', 0, 'u', 0, '\r', 0, '\n', 0}
	if got := decodeOutput(raw); got != "Ubuntu\r\n" {
		t.Fatalf("decodeOutput(utf16le) = %q, want %q", got, "Ubuntu\r\n")
	}
}

func TestDecodeOutputUTF8(t *testing.T) {
	raw := []byte("Ubuntu\n")
	if got := decodeOutput(raw); got != "Ubuntu\n" {
		t.Fatalf("decodeOutput(utf8) = %q, want %q", got, "Ubuntu\n")
	}
}

func TestSplitDistroListTrimsNULAndBlank(t *testing.T) {
	got := splitDistroList("Ubuntu\x00\r\n\r\n  \r\ngobrave-ubuntu-docker\x00\r\n")
	want := []string{"Ubuntu", "gobrave-ubuntu-docker"}
	if len(got) != len(want) {
		t.Fatalf("splitDistroList len = %d (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("splitDistroList[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestContainsDistroCaseInsensitive(t *testing.T) {
	names := []string{"Ubuntu", "docker-desktop"}
	if !containsDistro(names, "ubuntu") {
		t.Error("containsDistro should be case-insensitive")
	}
	if containsDistro(names, "debian") {
		t.Error("containsDistro should reject unknown distro")
	}
}

func TestToDaemonPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{`D:\workspace\gobrave\.gobrave`, "/mnt/d/workspace/gobrave/.gobrave"},
		{`C:/Users/foo`, "/mnt/c/Users/foo"},
		{`D:\`, "/mnt/d"},
		{"", ""},
		{"/already/posix", "/already/posix"},
	}
	for _, c := range cases {
		if got := toDaemonPath(c.in); got != c.want {
			t.Errorf("toDaemonPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLayoutPaths(t *testing.T) {
	// 用 POSIX base 断言结构与顺序，避免依赖运行平台的分隔符
	// （filepath.Join 在 Windows 用 \、在 Linux 用 /）。
	l := newLayout("/data/.gobrave")
	if got, want := l.Root(), filepath.Join("/data/.gobrave", "wsl"); got != want {
		t.Errorf("Root = %q, want %q", got, want)
	}
	if got, want := l.RootfsPath(), filepath.Join(l.Root(), "cache", DefaultRootfsName); got != want {
		t.Errorf("RootfsPath = %q, want %q", got, want)
	}
	if got, want := l.DistroDir(), filepath.Join(l.Root(), "distro"); got != want {
		t.Errorf("DistroDir = %q, want %q", got, want)
	}
	// 所有派生路径都必须位于 wsl 根目录之下。
	for _, p := range []string{l.RootfsPath(), l.DistroDir(), l.StatePath()} {
		if !strings.HasPrefix(p, l.Root()) {
			t.Errorf("%q should be under %q", p, l.Root())
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l := newLayout(dir)
	if err := l.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	in := &state{Distro: DefaultDistro, Version: "Ubuntu 24.04", Port: 2375}
	if err := writeState(l.StatePath(), in); err != nil {
		t.Fatalf("writeState: %v", err)
	}
	out, err := readState(l.StatePath())
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if out.Distro != in.Distro || out.Version != in.Version || out.Port != in.Port {
		t.Errorf("state roundtrip mismatch: got %+v, want %+v", out, in)
	}
	if !l.installed() {
		t.Error("layout.installed() should be true after writing state + distro dir")
	}
}

func TestConfigEndpointPrefersExplicit(t *testing.T) {
	// 与 Linux 实现一致：显式配置优先。
	if got := (Config{Endpoint: "tcp://192.168.1.50:2376"}).endpoint(); got != "tcp://192.168.1.50:2376" {
		t.Errorf("endpoint() = %q, want explicit value", got)
	}
	if got := (Config{Endpoint: "  "}).endpoint(); got != DefaultEndpoint {
		t.Errorf("endpoint() = %q, want default %q", got, DefaultEndpoint)
	}
}

func TestConfigPortFollowsEndpoint(t *testing.T) {
	cases := []struct {
		endpoint string
		want     int
	}{
		{"tcp://localhost:2375", 2375},
		{"tcp://192.168.1.50:2376", 2376},
		{"unix:///var/run/docker.sock", DefaultDaemonPort},
		{"", DefaultDaemonPort},
	}
	for _, c := range cases {
		if got := (Config{Endpoint: c.endpoint}).port(); got != c.want {
			t.Errorf("port(%q) = %d, want %d", c.endpoint, got, c.want)
		}
	}
}

func TestProvisionerPortIgnoresStaleState(t *testing.T) {
	// state.json 里的旧端口不再覆盖配置：改 endpoint 后无需删 state 即可生效。
	p := New(Config{Endpoint: "tcp://localhost:2376"})
	if got := p.port(&state{Port: 2375}); got != 2376 {
		t.Errorf("port(stale state) = %d, want 2376", got)
	}
}

func TestEndpointHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"tcp://localhost:2375", "localhost"},
		{"tcp://172.24.1.10:2375", "172.24.1.10"},
		{"http://example.com:2375", "example.com"},
		{"", "localhost"},
		{"not a url", "localhost"},
	}
	for _, c := range cases {
		if got := endpointHost(c.in); got != c.want {
			t.Errorf("endpointHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStopDaemonScriptUsesSystemd(t *testing.T) {
	// dockerd 由发行版自己维护，gobrave 只经 systemd 请求停止。
	if !strings.Contains(stopDaemonScript, "systemctl stop") {
		t.Errorf("stopDaemonScript must stop docker via systemctl, got:\n%s", stopDaemonScript)
	}
	if strings.Contains(stopDaemonScript, "pkill") {
		t.Error("stopDaemonScript must not kill dockerd directly")
	}
}

func TestDownloadedFalseWhenMissing(t *testing.T) {
	l := newLayout(t.TempDir())
	if l.downloaded() {
		t.Error("downloaded() should be false when rootfs is absent")
	}
	if l.installed() {
		t.Error("installed() should be false when nothing exists")
	}
}

func TestInstalledTrueForAdoptedDistro(t *testing.T) {
	// 接管场景：发行版 vhdx 由 WSL 自己管理，只有 distro-external 占位目录。
	// installed() 必须认可它，否则 status 永远停在 not_installed。
	dir := t.TempDir()
	l := newLayout(dir)
	if err := l.EnsureAdoptDirs(); err != nil {
		t.Fatalf("EnsureAdoptDirs: %v", err)
	}
	if l.installed() {
		t.Error("installed() should be false before state.json is written")
	}

	st := &state{Distro: "Ubuntu", Version: "Ubuntu 24.04", Port: 2375, Adopted: true}
	if err := writeState(l.StatePath(), st); err != nil {
		t.Fatalf("writeState: %v", err)
	}
	if !l.installed() {
		t.Error("installed() should be true for adopted distro with state.json")
	}
	// 确认没有创建 distro 目录：接管不能污染导入目录。
	if entries, err := filepath.Glob(filepath.Join(l.DistroDir(), "*")); err == nil && len(entries) > 0 {
		t.Errorf("distro dir should stay empty for adopted distro, got %v", entries)
	}
}

func TestInstalledRequiresDistroDirForImportedDistro(t *testing.T) {
	dir := t.TempDir()
	l := newLayout(dir)
	if err := l.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	// 非接管：state.json 存在但 distro 目录被删，视为未安装。
	st := &state{Distro: DefaultDistro, Port: 2375}
	if err := writeState(l.StatePath(), st); err != nil {
		t.Fatalf("writeState: %v", err)
	}
	if err := os.RemoveAll(l.DistroDir()); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if l.installed() {
		t.Error("installed() should be false for imported distro once its dir is gone")
	}
}
