package containerruntime

import (
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
)

func TestPathMapperHost(t *testing.T) {
	m := NewPathMapper(`D:\workspace\gobrave\.gobrave`, false)

	cases := []struct {
		name string
		in   string
		want string
	}{
		// storage 派生出的容器路径：补回 base_dir 的盘符。
		{"容器路径还原", "/workspace/gobrave/.gobrave/user/1/.config/code-server/settings.json",
			`D:\workspace\gobrave\.gobrave\user\1\.config\code-server\settings.json`},
		{"base 本身", "/workspace/gobrave/.gobrave", `D:\workspace\gobrave\.gobrave`},
		// 已是宿主路径：幂等，不再重复补盘符。
		{"宿主路径幂等", `D:\workspace\gobrave\.gobrave\package\brave-env.sh`,
			`D:\workspace\gobrave\.gobrave\package\brave-env.sh`},
		{"正斜杠宿主路径", "D:/workspace/gobrave/data/1", `D:\workspace\gobrave\data\1`},
		// 不在 base 下的 POSIX 路径是 daemon / 容器内部路径，保持原样。
		{"docker.sock", "/var/run/docker.sock", "/var/run/docker.sock"},
		{"无关的容器路径", "/etc/profile.d/brave-env.sh", "/etc/profile.d/brave-env.sh"},
		// base 前缀相似但不是子路径，不能误判。
		{"前缀相似", "/workspace/gobrave/.gobrave2/x", "/workspace/gobrave/.gobrave2/x"},
		{"空串", "", ""},
	}
	for _, tc := range cases {
		if got := m.Host(tc.in); got != tc.want {
			t.Errorf("%s: Host(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestPathMapperHostWithoutDrive(t *testing.T) {
	m := NewPathMapper("/home/admin/gobrave/.gobrave", false)
	if got := m.Host("/home/admin/gobrave/.gobrave/user/1"); got != "/home/admin/gobrave/.gobrave/user/1" {
		t.Errorf("Linux 宿主路径应原样返回, got %q", got)
	}
	if got := NewPathMapper("", false).Host("/var/run/docker.sock"); got != "/var/run/docker.sock" {
		t.Errorf("无 base_dir 时应原样返回, got %q", got)
	}
}

func TestPathMapperDaemon(t *testing.T) {
	wsl := NewPathMapper(`D:\gobrave`, true)
	if got := wsl.Daemon(`D:\gobrave\.gobrave`); got != "/mnt/d/gobrave/.gobrave" {
		t.Errorf("WSL daemon 应映射到 /mnt/d, got %q", got)
	}
	if got := wsl.Daemon("/var/run/docker.sock"); got != "/var/run/docker.sock" {
		t.Errorf("POSIX 路径应原样返回, got %q", got)
	}

	local := NewPathMapper(`D:\gobrave`, false)
	if got := local.Daemon(`D:\gobrave\.gobrave`); got != `D:\gobrave\.gobrave` {
		t.Errorf("非 WSL daemon 应原样返回, got %q", got)
	}
}

func TestPathMapperNormalizeVolumes(t *testing.T) {
	m := NewPathMapper(`D:\workspace\gobrave\.gobrave`, false)
	volumes := []types.ContainerVolume{
		// 模板变量展开后的 bind：Source 丢了盘符，需要还原；Target 保持容器路径。
		{Source: "/workspace/gobrave/.gobrave/user/1/.config/code-server/settings.json",
			Target: "/config/data/User/settings.json", Type: "file"},
		// base_dir 本身已经是宿主路径，归一后不变。
		{Source: `D:\workspace\gobrave\.gobrave`, Target: "/workspace/gobrave/.gobrave"},
		// named volume：Source 是卷名，不能被补盘符。
		{Source: "gobrave-data", Target: "/data", Type: "volume"},
		// daemon 路径：不能被补盘符。
		{Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
	}

	m.NormalizeVolumes(volumes)

	want := []types.ContainerVolume{
		{Source: `D:\workspace\gobrave\.gobrave\user\1\.config\code-server\settings.json`,
			Target: "/config/data/User/settings.json", Type: "file"},
		{Source: `D:\workspace\gobrave\.gobrave`, Target: "/workspace/gobrave/.gobrave"},
		{Source: "gobrave-data", Target: "/data", Type: "volume"},
		{Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
	}
	for i := range want {
		if volumes[i] != want[i] {
			t.Errorf("volume[%d] = %+v, want %+v", i, volumes[i], want[i])
		}
	}
}

func TestPathMapperJoin(t *testing.T) {
	m := NewPathMapper(`D:\workspace\gobrave\.gobrave`, false)
	cases := []struct{ base, elem, want string }{
		{"/workspace/gobrave/.gobrave", "package", "/workspace/gobrave/.gobrave/package"},
		{`D:\workspace\gobrave\.gobrave`, "data/1", "/workspace/gobrave/.gobrave/data/1"},
		{"/", "package", "/package"},
	}
	for _, tc := range cases {
		if got := m.Join(tc.base, tc.elem); got != tc.want {
			t.Errorf("Join(%q, %q) = %q, want %q", tc.base, tc.elem, got, tc.want)
		}
	}
}

func TestNilPathMapper(t *testing.T) {
	var m *PathMapper
	if got := m.Host("/a/b"); got != "/a/b" {
		t.Errorf("nil Host = %q", got)
	}
	if got := m.Daemon("/a/b"); got != "/a/b" {
		t.Errorf("nil Daemon = %q", got)
	}
	if m.WSLDaemon() {
		t.Error("nil WSLDaemon 应为 false")
	}
}
