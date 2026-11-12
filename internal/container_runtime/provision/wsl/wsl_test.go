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
	for _, p := range []string{l.RootfsPath(), l.DistroDir(), l.StatePath(), l.LogPath()} {
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

func TestBuildStartScriptBindsAllInterfaces(t *testing.T) {
	// WSL2 NAT：Windows 的流量从 eth0 进入，绑 127.0.0.1 一律被拒，必须 0.0.0.0。
	script := buildStartScript(2375)
	if !strings.Contains(script, "--host=tcp://0.0.0.0:2375") {
		t.Errorf("start script must bind tcp on 0.0.0.0:%d, got:\n%s", 2375, script)
	}
	if strings.Contains(script, "tcp://127.0.0.1") {
		t.Errorf("start script must not bind loopback only, got:\n%s", script)
	}
	if strings.Contains(script, "%!") {
		t.Errorf("start script has unsubstituted format verbs:\n%s", script)
	}
	if !strings.Contains(script, "mkdir -p /var/log "+distroRoot) {
		t.Errorf("start script must create pid/log dir %s, got:\n%s", distroRoot, script)
	}
}

func TestPickDistroIPSkipsInternalIfaces(t *testing.T) {
	// 真实 `ip -4 -o addr show` 输出形态。
	out := `1: lo    inet 127.0.0.1/8 scope host lo
2: eth0    inet 172.27.140.255/18 brd 172.27.191.255 scope global eth0
3: docker0    inet 172.17.0.1/16 brd 172.17.255.255 scope global docker0
4: br-1a2b3c    inet 172.18.0.1/16 scope global br-1a2b3c`
	if got := pickDistroIP(out, "eth0"); got != "172.27.140.255" {
		t.Errorf("pickDistroIP = %q, want eth0 address 172.27.140.255", got)
	}
}

func TestPickDistroIPFallsBackWhenNoEth0(t *testing.T) {
	// 没有 eth0（例如发行版内网卡名不是 eth0）时，取第一个非内部接口。
	out := `1: lo    inet 127.0.0.1/8 scope host lo
2: enp0s1    inet 172.28.1.5/20 scope global enp0s1
3: docker0    inet 172.17.0.1/16 scope global docker0`
	if got := pickDistroIP(out, "eth0"); got != "172.28.1.5" {
		t.Errorf("pickDistroIP = %q, want 172.28.1.5", got)
	}
}

func TestPickDistroIPHostnameIShape(t *testing.T) {
	// hostname -I：一行多个纯 IP，无接口名；必须跳过 docker0 地址。
	out := "172.17.0.1 172.27.140.255 \n"
	// 无接口信息时无法识别 docker0，取第一个，但绝不能返回回环地址。
	if got := pickDistroIP("127.0.0.1 172.27.140.255\n", ""); got != "172.27.140.255" {
		t.Errorf("pickDistroIP(hostname -I) = %q, want 172.27.140.255", got)
	}
	if got := pickDistroIP(out, ""); got == "" {
		t.Error("pickDistroIP should return some IPv4 for hostname -I shape")
	}
}

func TestPickDistroIPEmpty(t *testing.T) {
	for _, in := range []string{"", "\n", "127.0.0.1/8\n"} {
		if got := pickDistroIP(in, "eth0"); got != "" {
			t.Errorf("pickDistroIP(%q) = %q, want empty", in, got)
		}
	}
}

func TestIsInternalIface(t *testing.T) {
	internal := []string{"lo", "lo:", "docker0", "docker0:", "docker_gwbridge", "br-abc123", "veth1234"}
	for _, n := range internal {
		if !isInternalIface(n) {
			t.Errorf("isInternalIface(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"eth0", "eth0:", "enp0s1", "wlan0"} {
		if isInternalIface(n) {
			t.Errorf("isInternalIface(%q) = true, want false", n)
		}
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
