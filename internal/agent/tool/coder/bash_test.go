package coder

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/tool/coder/hostshell"
	"github.com/biox-dev/gobrave/internal/agent/types"
)

// TestBashToolRunsCommand 覆盖宿主机 bash 工具的端到端执行。
//
// Windows 上的常驻 PowerShell 曾因为把命令拼进 `< $null` 输入重定向（PowerShell 保留运算符）
// 导致整行解析失败、哨兵文件永远不出现，于是每条命令都要等到 60s 默认超时才返回
// "Command execution timed out or was interrupted"。
func TestBashToolRunsCommand(t *testing.T) {
	const sessionID int64 = 987654321
	ctx := tool.WithTaskFacts(context.Background(), types.TaskFacts{
		SessionID:  sessionID,
		UserID:     1,
		ProjectID:  1,
		WorkingDir: t.TempDir(),
	})
	t.Cleanup(func() { hostshell.CloseShell(sessionID) })

	bash := NewBash(Deps{})

	start := time.Now()
	got, err := bash.Execute(ctx, json.RawMessage(`{"command":"echo bash-tool-ok"}`))
	if err != nil {
		if strings.Contains(err.Error(), "no available command shell") {
			t.Skip("no host command shell available on this platform")
		}
		t.Fatalf("Execute returned error: %v", err)
	}

	out, ok := got.(BashOutput)
	if !ok {
		t.Fatalf("Execute returned %T, want BashOutput", got)
	}
	if !strings.Contains(out.Output, "bash-tool-ok") {
		t.Fatalf("output = %q, want it to contain bash-tool-ok", out.Output)
	}
	if strings.Contains(out.Output, "timed out or was interrupted") {
		t.Fatalf("command timed out: %q", out.Output)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("command took %s, expected a fast execution", elapsed)
	}
}

// TestIsSafeReadOnlyCommand 覆盖只读判定：多行/串联/管道的命令必须逐段校验，
// 不能因为首个子命令在白名单里就整条放行。
func TestIsSafeReadOnlyCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{"simple read only", "ls -l /tmp", true},
		{"multi line read only", "ls\npwd", true},
		{"variable assignment prefix", "SRC=/data ls \"$SRC\"", true},
		{"quoted separator", "ls \"a;b\"", true},

		{"set -e then mv", "set -e\nSRC=/data/a\nDST=/data/b\nmv \"$SRC\" \"$DST\"", false},
		{"semicolon chain", "echo hi; rm -rf /data", false},
		{"and chain", "git status && git reset --hard", false},
		{"pipe chain", "ps aux | xargs kill -9", false},
		{"redirect", "df -h > /etc/passwd", false},
		{"command substitution", "echo $(rm -rf /data)", false},
		{"backtick", "echo `rm -rf /data`", false},
		{"subshell", "(cd /data && ls)", false},
		{"bare set", "set -e", false},
		{"empty", "   ", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSafeReadOnlyCommand(tc.command); got != tc.want {
				t.Fatalf("isSafeReadOnlyCommand(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// TestBannedCommand 覆盖禁用命令检查：串联命令里的禁用命令也要被发现。
func TestBannedCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"direct", "curl http://example.com", "curl"},
		{"after and", "echo ok && wget http://example.com", "wget"},
		{"after semicolon", "ls; alias", "alias"},
		{"quoted", "\"curl\" http://example.com", "curl"},
		{"clean", "ls -l /tmp", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := bannedCommand(tc.command); got != tc.want {
				t.Fatalf("bannedCommand(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}
