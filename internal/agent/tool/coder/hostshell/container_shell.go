package hostshell

import (
	"context"
	"fmt"
	"sync"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
)

// ContainerShellService 是 manager 提供的「容器终端」最小能力集。
//
// 与 hostshell 里的持久 shell 一一对应：
//   - Ensure 相当于 GetPersistentShell（按会话定位 / 创建容器终端，保留 cd / export）；
//   - Exec 相当于 PersistentShell.Exec（把命令写入同一个 PTY 并等待结果）；
//   - Close 相当于 ClosePersistentShell（结束容器并删除会话记录）。
type ContainerShellService interface {
	// Ensure 返回 terminalID 对应的终端会话 ID；不存在时按 (userID, projectID, templateID) 创建。
	Ensure(ctx context.Context, userID, projectID, templateID, agentSessionID int64) (int64, error)
	// Exec 在 terminalID 对应的终端里执行命令（等待结束）。
	Exec(ctx context.Context, terminalID int64, command string, timeoutSeconds int) (runtimecontext.ContainerTerminalExecResult, error)
	// Close 关闭 terminalID 对应的终端会话（删除容器）。
	Close(ctx context.Context, terminalID int64)
}

// ContainerShell 是一个 Agent 会话对应的容器持久 shell。
//
// 语义与 PersistentShell 完全一致：一次 Agent 请求内多次调用复用同一个容器 PTY，
// 请求结束时由调用方 Close 掉（见 providers/custom.go 的 OnFinish）。
type ContainerShell struct {
	agentSessionID int64
	userID         int64
	projectID      int64
	templateID     int64

	svc ContainerShellService

	mu         sync.Mutex
	terminalID int64
	closed     bool
}

// containerShells 按 Agent 会话维护容器 shell：不同会话各自独立。
var (
	containerShellMu sync.Mutex
	containerShells  = make(map[int64]*ContainerShell)
)

// GetContainerShell 返回 agentSessionID 对应的容器 shell：同一会话内复用（容器与 PTY 保留），
// 会话结束后调用方必须调用 CloseContainerShell 释放容器。
//
// svc 为 nil 或 templateID <= 0 时返回 nil（未启用容器执行）。
func GetContainerShell(svc ContainerShellService, agentSessionID, userID, projectID, templateID int64) *ContainerShell {
	if svc == nil || agentSessionID <= 0 || templateID <= 0 {
		return nil
	}
	containerShellMu.Lock()
	defer containerShellMu.Unlock()

	if sh := containerShells[agentSessionID]; sh != nil && !sh.isClosed() {
		return sh
	}
	sh := &ContainerShell{
		agentSessionID: agentSessionID,
		userID:         userID,
		projectID:      projectID,
		templateID:     templateID,
		svc:            svc,
	}
	containerShells[agentSessionID] = sh
	return sh
}

// CloseContainerShell 关闭 agentSessionID 对应的容器 shell（会话结束后调用）。
func CloseContainerShell(agentSessionID int64) {
	containerShellMu.Lock()
	sh := containerShells[agentSessionID]
	delete(containerShells, agentSessionID)
	containerShellMu.Unlock()

	if sh != nil {
		sh.close()
	}
}

// Exec 在容器 shell 里执行命令（等待结束），返回与宿主机 shell 对齐的四元组。
func (s *ContainerShell) Exec(ctx context.Context, command string, timeoutMs int) (string, string, int, bool, error) {
	terminalID, err := s.ensure(ctx)
	if err != nil {
		return "", "", 1, false, err
	}

	result, err := s.svc.Exec(ctx, terminalID, command, timeoutMs/1000)
	if err != nil {
		return "", "", 1, false, err
	}
	if result.TimedOut {
		return result.Output, "Command execution timed out or was interrupted", result.ExitCode, true, nil
	}
	return result.Output, "", result.ExitCode, false, nil
}

// close 关闭容器 shell 并释放容器；可重复调用。仅供 CloseContainerShell 调用。
func (s *ContainerShell) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	terminalID := s.terminalID
	s.mu.Unlock()

	if terminalID != 0 {
		s.svc.Close(context.Background(), terminalID)
	}
}

func (s *ContainerShell) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// ensure 返回终端会话 ID，首次调用时创建容器终端。
func (s *ContainerShell) ensure(ctx context.Context) (int64, error) {
	s.mu.Lock()
	id := s.terminalID
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return 0, fmt.Errorf("container shell is closed")
	}
	if id != 0 {
		return id, nil
	}

	id, err := s.svc.Ensure(ctx, s.userID, s.projectID, s.templateID, s.agentSessionID)
	if err != nil {
		return 0, err
	}

	s.mu.Lock()
	s.terminalID = id
	s.mu.Unlock()
	return id, nil
}
