package manager

import (
	"bufio"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
)

// fakeShell 模拟一个 bash + PROMPT_COMMAND 的 PTY：
//   - 收到初始化行时回一个退出码 0 的标记（让会话进入 ready）；
//   - 收到普通命令行时回显 + 标记（"fail" 前缀返回 1，"hang" 不返回）。
type fakeShell struct {
	inR  *io.PipeReader
	inW  *io.PipeWriter
	outR *io.PipeReader
	outW *io.PipeWriter

	closeOnce sync.Once
	closed    chan struct{}
}

func newFakeShell() *fakeShell {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	shell := &fakeShell{inR: inR, inW: inW, outR: outR, outW: outW, closed: make(chan struct{})}
	go shell.loop()
	return shell
}

func (s *fakeShell) loop() {
	scanner := bufio.NewScanner(s.inR)
	for scanner.Scan() {
		line := scanner.Text()
		// 真实 PTY 会回显输入，这里同样回显，便于验证「重放包含命令」。
		_, _ = io.WriteString(s.outW, line+"\r\n")
		switch {
		case strings.Contains(line, "PROMPT_COMMAND"):
			_, _ = io.WriteString(s.outW, "\x1b]633;GD;0\x07")
		case strings.HasPrefix(line, "hang"):
			// 模拟一直运行、不回到提示符的命令。
		case strings.HasPrefix(line, "fail"):
			_, _ = io.WriteString(s.outW, "boom\n\x1b]633;GD;1\x07")
		default:
			_, _ = io.WriteString(s.outW, "echo:"+line+"\n\x1b]633;GD;0\x07")
		}
	}
	_ = s.outW.Close()
}

func (s *fakeShell) Read(p []byte) (int, error)  { return s.outR.Read(p) }
func (s *fakeShell) Write(p []byte) (int, error) { return s.inW.Write(p) }

func (s *fakeShell) Resize(uint16, uint16) error { return nil }

func (s *fakeShell) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.inW.Close()
		_ = s.outR.Close()
	})
	return nil
}

type fakeOpener struct {
	shell *fakeShell
	calls int
	mu    sync.Mutex
}

func (o *fakeOpener) OpenProbeTerminal(_ context.Context, _, _ int64, _ containerruntime.TerminalOptions) (containerruntime.TerminalSession, func(), error) {
	o.mu.Lock()
	o.calls++
	o.mu.Unlock()
	return o.shell, func() {}, nil
}

func newTestRegistry(opener *fakeOpener) *TerminalSessionRegistry {
	return newTerminalSessionRegistry(opener, nil, TerminalRegistryOptions{
		ReadyWait:   2 * time.Second,
		IdleGrace:   time.Minute,
		BufferBytes: 64 * 1024,
		ExecTimeout: 2 * time.Second,
	})
}

func TestTerminalSessionRegistryExec(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	registry := newTestRegistry(opener)
	defer registry.CloseSession(TerminalSessionKey{UserID: 1, ProjectID: 2, TemplateID: 3})

	key := TerminalSessionKey{UserID: 1, ProjectID: 2, TemplateID: 3}
	session, created, err := registry.Ensure(context.Background(), key)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !created {
		t.Fatalf("expected created session")
	}
	if session.ID() != key.SessionID() {
		t.Fatalf("unexpected session id: %s", session.ID())
	}

	// 同一 key 复用同一会话，不再创建容器。
	again, createdAgain, err := registry.Ensure(context.Background(), key)
	if err != nil || again != session || createdAgain {
		t.Fatalf("expected session reuse, got created=%v err=%v", createdAgain, err)
	}

	result, err := registry.Execute(context.Background(), key, TerminalCommand{
		Command:         "ls -l",
		Wait:            true,
		Timeout:         2 * time.Second,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("unexpected exit code: %d", result.ExitCode)
	}
	if !strings.Contains(result.Output, "echo:ls -l") {
		t.Fatalf("expected command output in result, got %q", result.Output)
	}
	if strings.Contains(result.Output, "633;GD;") {
		t.Fatalf("OSC marker leaked into output: %q", result.Output)
	}

	fail, err := registry.Execute(context.Background(), key, TerminalCommand{
		Command:         "fail-cmd",
		Wait:            true,
		Timeout:         2 * time.Second,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("Execute fail: %v", err)
	}
	if fail.ExitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", fail.ExitCode)
	}
}

func TestTerminalSessionRegistryExecTimeoutAndReplay(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	registry := newTestRegistry(opener)
	key := TerminalSessionKey{UserID: 7, ProjectID: 8, TemplateID: 9}
	defer registry.CloseSession(key)

	if _, _, err := registry.Ensure(context.Background(), key); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	result, err := registry.Execute(context.Background(), key, TerminalCommand{
		Command:         "hang-forever",
		Wait:            true,
		Timeout:         150 * time.Millisecond,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.TimedOut {
		t.Fatalf("expected timed out result, got %+v", result)
	}

	session := registry.Lookup(key)
	if session == nil {
		t.Fatalf("expected live session")
	}
	sub, replay := session.Attach(0)
	defer session.Detach(sub)
	if len(replay) == 0 {
		t.Fatalf("expected replay bytes")
	}
	if !strings.Contains(string(replay), "hang-forever") {
		t.Fatalf("expected replayed command echo, got %q", string(replay))
	}

	status := registry.Status(key)
	if !status.Exists || !status.Alive || status.Subscribers != 1 || status.Seq <= 0 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestTerminalSessionRegistryCreateOnlyOnceUnderConcurrency(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	registry := newTestRegistry(opener)
	key := TerminalSessionKey{UserID: 4, ProjectID: 5, TemplateID: 6}
	defer registry.CloseSession(key)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := registry.Ensure(context.Background(), key); err != nil {
				t.Errorf("Ensure: %v", err)
			}
		}()
	}
	wg.Wait()

	opener.mu.Lock()
	calls := opener.calls
	opener.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected one container creation, got %d", calls)
	}
}
