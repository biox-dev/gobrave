package wsl

import (
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

func TestDownloadedFalseWhenMissing(t *testing.T) {
	l := newLayout(t.TempDir())
	if l.downloaded() {
		t.Error("downloaded() should be false when rootfs is absent")
	}
	if l.installed() {
		t.Error("installed() should be false when nothing exists")
	}
}
