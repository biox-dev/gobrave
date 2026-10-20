package containerruntime

import (
	"context"
	"io"
)

// TerminalOptions 描述一次交互式终端会话。
type TerminalOptions struct {
	// Command 是要在容器内执行的命令，例如 []string{"/bin/bash", "-l"}。
	Command []string
	// TTY 为 true 时申请伪终端（交互式 shell 必须为 true）。
	TTY bool
	// Rows / Cols 是初始终端尺寸。
	Rows uint16
	Cols uint16
}

// TerminalSession 是一条双向的终端流：Read 读容器输出，Write 写用户输入。
type TerminalSession interface {
	io.Reader
	io.Writer
	// Resize 调整终端尺寸（行 / 列）。
	Resize(rows, cols uint16) error
	// Close 关闭会话与底层连接。
	Close() error
}

// RuntimeTerminal 是可选扩展接口：支持交互式 exec 的运行时实现它。
//
// 它与 Runtime.Exec 的区别：Exec 是非交互式的一次性命令（无 stdin / 无 TTY），
// 这里则等价于 `kubectl exec -it` / `docker exec -it`。
type RuntimeTerminal interface {
	OpenTerminal(ctx context.Context, runtimeID string, opts TerminalOptions) (TerminalSession, error)
}
