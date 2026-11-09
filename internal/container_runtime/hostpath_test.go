package containerruntime

import "testing"

func TestIsWindowsHostPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{`D:\workspace\go-project\gobrave\.gobrave`, true},
		{`D:/workspace/gobrave`, true},
		{`C:`, true},
		{`\\server\share\dir`, true},
		{`/var/run/docker.sock`, false},
		{`/home/admin/workspace/gobrave/.gobrave`, false},
		{`relative/path`, false},
		{``, false},
	}
	for _, tc := range cases {
		if got := IsWindowsHostPath(tc.path); got != tc.want {
			t.Errorf("IsWindowsHostPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestToContainerPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// 用户报错的真实路径：盘符去掉、反斜杠换成 /。
		{`D:\workspace\go-project\gobrave\.gobrave`, "/workspace/go-project/gobrave/.gobrave"},
		{`D:/workspace/gobrave`, "/workspace/gobrave"},
		{`D:\workspace\go-project\gobrave\.gobrave\package\brave-env.sh`, "/workspace/go-project/gobrave/.gobrave/package/brave-env.sh"},
		// Linux 路径保持原样。
		{`/home/admin/workspace/gobrave/.gobrave`, "/home/admin/workspace/gobrave/.gobrave"},
		{`/var/run/docker.sock`, "/var/run/docker.sock"},
		// 空串保持空串。
		{``, ""},
	}
	for _, tc := range cases {
		if got := ToContainerPath(tc.in); got != tc.want {
			t.Errorf("ToContainerPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestToHostPath(t *testing.T) {
	cases := []struct {
		container string
		drive     string
		want      string
	}{
		{"/workspace/gobrave/data/1", `D:\`, `D:\workspace\gobrave\data\1`},
		{"/workspace/gobrave", "", "/workspace/gobrave"},
	}
	for _, tc := range cases {
		if got := ToHostPath(tc.container, tc.drive); got != tc.want {
			t.Errorf("ToHostPath(%q, %q) = %q, want %q", tc.container, tc.drive, got, tc.want)
		}
	}
}
