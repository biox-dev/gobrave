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
