package manager

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/types"
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
	reader := bufio.NewReader(s.inR)
	var line []byte
	for {
		b, err := reader.ReadByte()
		if err != nil {
			break
		}
		switch b {
		case 0x15: // Ctrl-U：清掉当前行（与 readline 的 kill-line 一致）
			line = line[:0]
		case '\r', '\n':
			text := string(line)
			line = line[:0]
			if text == "" {
				continue
			}
			// 真实 PTY 会回显输入，这里同样回显，便于验证「重放包含命令」。
			_, _ = io.WriteString(s.outW, text+"\r\n")
			switch {
			case strings.Contains(text, "PROMPT_COMMAND"):
				_, _ = io.WriteString(s.outW, "\x1b]633;GD;0\x07")
			case strings.HasPrefix(text, "hang"):
				// 模拟一直运行、不回到提示符的命令。
			case strings.HasPrefix(text, "fail"):
				_, _ = io.WriteString(s.outW, "boom\n\x1b]633;GD;1\x07")
			default:
				_, _ = io.WriteString(s.outW, "echo:"+text+"\n\x1b]633;GD;0\x07")
			}
		default:
			line = append(line, b)
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
	shell    *fakeShell
	calls    int
	attaches int
	mu       sync.Mutex
}

// currentShell 返回当前 shell；每次 Start 都换一个新的（真实容器每会话一个 shell）。
func (o *fakeOpener) currentShell() *fakeShell {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.shell == nil {
		o.shell = newFakeShell()
	}
	return o.shell
}

func (o *fakeOpener) StartTerminalRuntime(_ context.Context, _, _, _ int64, _ containerruntime.TerminalOptions) (*ProbeTerminalHandle, error) {
	o.mu.Lock()
	o.calls++
	// 一个容器对应一个独立 shell：上一次会话关闭后不能复用它的 shell。
	shell := newFakeShell()
	o.shell = shell
	o.mu.Unlock()
	return &ProbeTerminalHandle{
		RuntimeName: "fake",
		RuntimeID:   "fake-runtime-1",
		Session:     shell,
		Cleanup:     func() {},
	}, nil
}

func (o *fakeOpener) AttachTerminalRuntime(_ context.Context, _, runtimeID string, _ containerruntime.TerminalOptions) (*ProbeTerminalHandle, error) {
	o.mu.Lock()
	o.attaches++
	shell := o.shell
	o.mu.Unlock()
	return &ProbeTerminalHandle{
		RuntimeName: "fake",
		RuntimeID:   runtimeID,
		Session:     shell,
		Cleanup:     func() {},
	}, nil
}

func (o *fakeOpener) DropTerminalRuntime(context.Context, string, string) {}

func newTestRegistry(opener *fakeOpener) *TerminalSessionRegistry {
	return newTerminalSessionRegistry(opener, nil, nil, TerminalRegistryOptions{
		ReadyWait:   2 * time.Second,
		SessionTTL:  time.Hour,
		BufferBytes: 64 * 1024,
		ExecTimeout: 2 * time.Second,
	})
}

func TestTerminalSessionRegistryExec(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	registry := newTestRegistry(opener)

	key := NewUIKey(1, 2, 3)
	session, created, err := registry.EnsurePersistent(context.Background(), key)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !created {
		t.Fatalf("expected created session")
	}
	defer registry.CloseSession(session.Key())
	if session.ID() == "" {
		t.Fatalf("unexpected empty session id")
	}

	// UI 创建：同一模板再次 Ensure 会建一条新会话（一个模板可以有多条会话）。
	// 这里不做断言，避免多余的开销；下面用 session.Key() 验证按 ID 定位与执行。
	_ = key

	// 按会话 ID 查找 / 执行：同一个会话复用同一 PTY。
	resolved := registry.Lookup(session.Key())
	if resolved != session {
		t.Fatalf("expected Lookup by session id to return the same session")
	}

	result, err := registry.Execute(context.Background(), session.Key(), TerminalCommand{
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
	if result.SessionID != session.ID() {
		t.Fatalf("unexpected result session id: %s", result.SessionID)
	}
	if !strings.Contains(result.Output, "echo:ls -l") {
		t.Fatalf("expected command output in result, got %q", result.Output)
	}
	if strings.Contains(result.Output, "633;GD;") {
		t.Fatalf("OSC marker leaked into output: %q", result.Output)
	}

	fail, err := registry.Execute(context.Background(), session.Key(), TerminalCommand{
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
	key := NewUIKey(7, 8, 9)

	// 用持久会话验证「超时后仍可回放」：一次性会话在命令结束且无订阅者时会被回收。
	session, _, err := registry.EnsurePersistent(context.Background(), key)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	defer registry.CloseSession(session.Key())

	result, err := registry.Execute(context.Background(), session.Key(), TerminalCommand{
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

	live := registry.Lookup(session.Key())
	if live == nil {
		t.Fatalf("expected live session")
	}
	sub, replay := live.Attach(0)
	defer live.Detach(sub)
	if len(replay) == 0 {
		t.Fatalf("expected replay bytes")
	}
	if !strings.Contains(string(replay), "hang-forever") {
		t.Fatalf("expected replayed command echo, got %q", string(replay))
	}

	status := registry.Status(session.Key())
	if !status.Exists || !status.Alive || status.Subscribers != 1 || status.Seq <= 0 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

// 会话不存在时，Execute 必须「先建会话再执行命令」，且 Persistent 请求要生效。
//
// 回归场景：Agent 的 terminal_exec 在会话不存在时创建。
// 如果新建的是一次性会话，命令跑完容器就被回收；指定 Persistent 时必须保留。
func TestTerminalSessionRegistryExecCreatesPersistentSessionOnDemand(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	registry := newTestRegistry(opener)
	key := NewAgentKey(21, 22, 23, 99)

	result, err := registry.Execute(context.Background(), key, TerminalCommand{
		Command:         "pwd",
		Wait:            false,
		Source:          "ui",
		CreateIfMissing: true,
		Persistent:      true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Accepted || !result.Running {
		t.Fatalf("unexpected async result: %+v", result)
	}

	session := registry.Lookup(key)
	if session == nil {
		t.Fatalf("expected session created on demand")
	}
	defer registry.CloseSession(session.Key())
	if !session.isPersistent() {
		t.Fatalf("ui exec must create a persistent session")
	}

	// 命令真的被写进了 PTY，并且输出可在后续 attach 时重放。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(string(session.readSince(0)), "echo:pwd") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	sub, replay := session.Attach(0)
	defer session.Detach(sub)
	if !strings.Contains(string(replay), "echo:pwd") {
		t.Fatalf("expected command output in replay, got %q", string(replay))
	}

	// 命令结束后持久会话仍然存活（不会被立即回收）。
	time.Sleep(200 * time.Millisecond)
	if registry.Lookup(key) == nil {
		t.Fatalf("persistent session must survive after the command finished")
	}
	select {
	case <-opener.currentShell().closed:
		t.Fatalf("persistent session terminal must not be closed")
	default:
	}
}

// 未显式要求持久时，Execute 新建的仍是一次性会话（命令结束即回收）。
func TestTerminalSessionRegistryExecOneShotStillReaps(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	registry := newTestRegistry(opener)
	key := NewAgentKey(31, 32, 33, 7)

	result, err := registry.Execute(context.Background(), key, TerminalCommand{
		Command:         "ls",
		Wait:            true,
		Timeout:         2 * time.Second,
		Source:          "agent",
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result.Output, "echo:ls") {
		t.Fatalf("expected command output, got %q", result.Output)
	}
	// 回收发生在 result 返回之后（execLoop 里的 scheduleReap），这里等一小会儿。
	deadline := time.Now().Add(2 * time.Second)
	for registry.Lookup(key) != nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if registry.Lookup(key) != nil {
		t.Fatalf("expected one-shot session reaped after the command finished")
	}
}

// 终端里有一行「用户敲了一半、还没回车」的输入时执行命令：
// 必须先清行再写，否则命令会被拼成 `a="pending"pwd` 这种「合法但什么都不做」的行，
// 表现为「命令回显了却没执行，按回车才动」。
func TestTerminalSessionRegistryExecClearsPendingInputLine(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	registry := newTestRegistry(opener)

	session, _, err := registry.EnsurePersistent(context.Background(), NewUIKey(41, 42, 43))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	defer registry.CloseSession(session.Key())
	// 模拟用户敲了一半、没有回车提交。
	if _, err := session.WriteInput([]byte(`a="pending"`)); err != nil {
		t.Fatalf("WriteInput: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	result, err := registry.Execute(context.Background(), session.Key(), TerminalCommand{
		Command:         "pwd",
		Wait:            true,
		Timeout:         2 * time.Second,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(result.Output, "pending") {
		t.Fatalf("exec command was appended to the pending input line: %q", result.Output)
	}
	if !strings.Contains(result.Output, "echo:pwd") {
		t.Fatalf("expected the command to run on a clean line, got %q", result.Output)
	}
}

// 一次性会话（Agent 创建、从未被前端接入）：最后一个订阅者离开后立即删除容器与记录。
func TestTerminalSessionRegistryReapsOneShotSessionOnLastDetach(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	store := newFakeTerminalStore()
	registry := newTerminalSessionRegistry(opener, nil, store, TerminalRegistryOptions{
		ReadyWait:   2 * time.Second,
		BufferBytes: 64 * 1024,
	})
	key := NewAgentKey(11, 12, 13, 3)

	session, created, err := registry.Ensure(context.Background(), key)
	if err != nil || !created {
		t.Fatalf("Ensure: created=%v err=%v", created, err)
	}
	if session.isPersistent() {
		t.Fatalf("agent-created session must not be persistent by default")
	}

	sub, _ := session.Attach(0)
	select {
	case <-opener.currentShell().closed:
		t.Fatalf("terminal must not be closed while subscribed")
	default:
	}

	session.Detach(sub)

	if registry.Lookup(key) != nil {
		t.Fatalf("expected one-shot session removed immediately after last detach")
	}
	select {
	case <-opener.currentShell().closed:
	default:
		t.Fatalf("expected underlying terminal closed immediately")
	}
	if status := registry.Status(key); status.Exists {
		t.Fatalf("expected status not exists, got %+v", status)
	}
	store.mu.Lock()
	_, stillThere := store.rows[session.Key().SessionKey()]
	store.mu.Unlock()
	if stillThere {
		t.Fatalf("expected session record removed for one-shot session")
	}
}

// 一旦被标记为持久（前端接入 / Agent 显式要求），Detach 不再回收会话。
func TestTerminalSessionRegistryPersistentSurvivesDetach(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	store := newFakeTerminalStore()
	registry := newTerminalSessionRegistry(opener, nil, store, TerminalRegistryOptions{
		ReadyWait:   2 * time.Second,
		BufferBytes: 64 * 1024,
		SessionTTL:  time.Hour,
	})
	key := NewUIKey(21, 22, 23)

	session, _, err := registry.EnsurePersistent(context.Background(), key)
	if err != nil {
		t.Fatalf("EnsurePersistent: %v", err)
	}
	defer registry.CloseSession(session.Key())
	if !session.isPersistent() {
		t.Fatalf("expected persistent session")
	}

	sub, _ := session.Attach(0)
	session.Detach(sub)

	if registry.Lookup(session.Key()) == nil {
		t.Fatalf("persistent session must survive last detach")
	}
	select {
	case <-opener.currentShell().closed:
		t.Fatalf("terminal must stay open for persistent session")
	default:
	}
	store.mu.Lock()
	rec, ok := store.rows[session.Key().SessionKey()]
	persisted := ok && rec.Persistent
	store.mu.Unlock()
	if !persisted {
		t.Fatalf("expected persistent flag stored in record")
	}
}

// 一次性会话经前端接入（EnsurePersistent）后升级为持久，且不会退回一次性。
func TestTerminalSessionRegistryOneShotUpgradesToPersistent(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	store := newFakeTerminalStore()
	registry := newTerminalSessionRegistry(opener, nil, store, TerminalRegistryOptions{
		ReadyWait:   2 * time.Second,
		BufferBytes: 64 * 1024,
		SessionTTL:  time.Hour,
	})
	key := NewAgentKey(71, 72, 73, 5)

	session, created, err := registry.Ensure(context.Background(), key) // Agent 创建：一次性
	if err != nil || !created {
		t.Fatalf("Ensure: created=%v err=%v", created, err)
	}
	defer registry.CloseSession(session.Key())
	if session.isPersistent() {
		t.Fatalf("expected non-persistent session after agent create")
	}

	// 前端打开同一个 Agent 会话的终端：复用同一会话并升级为持久。
	again, createdAgain, err := registry.EnsurePersistent(context.Background(), key)
	if err != nil {
		t.Fatalf("EnsurePersistent: %v", err)
	}
	if createdAgain || again != session {
		t.Fatalf("expected the same session to be reused")
	}
	if !session.isPersistent() {
		t.Fatalf("expected session upgraded to persistent")
	}

	// 再走一次普通 Ensure 不能把它改回一次性。
	if _, _, err := registry.Ensure(context.Background(), key); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !session.isPersistent() {
		t.Fatalf("persistent flag must not be downgraded")
	}

	sub, _ := session.Attach(0)
	session.Detach(sub)
	if registry.Lookup(key) == nil {
		t.Fatalf("upgraded session must survive detach")
	}
}

// 仍有订阅者的会话不能被 TTL 维护误回收。
func TestTerminalSessionRegistryMaintainSkipsSubscribedSession(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	registry := newTerminalSessionRegistry(opener, nil, nil, TerminalRegistryOptions{
		ReadyWait:  2 * time.Second,
		SessionTTL: 10 * time.Millisecond,
	})

	session, _, err := registry.EnsurePersistent(context.Background(), NewUIKey(61, 62, 63))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	defer registry.CloseSession(session.Key())
	sub, _ := session.Attach(0)
	defer session.Detach(sub)

	time.Sleep(30 * time.Millisecond)
	registry.maintain(context.Background())

	if registry.Lookup(session.Key()) == nil {
		t.Fatalf("session with active subscriber must not be reaped by TTL")
	}
}

// terminal_exec 的两种语义：
//   - 默认（persistent=false）：一次性会话，命令结束即删除会话与容器；
//   - persistent=true：会话保留，后续调用复用同一容器。
func TestTerminalSessionRunInTerminalOneShotVsPersistent(t *testing.T) {
	store := newFakeTerminalStore()

	// 一次性会话：执行完立即回收。用独立的 opener，避免回收时关闭的 shell 影响后续用例。
	oneShotOpener := &fakeOpener{shell: newFakeShell()}
	oneShotRegistry := newTerminalSessionRegistry(oneShotOpener, nil, store, TerminalRegistryOptions{
		ReadyWait:   2 * time.Second,
		BufferBytes: 64 * 1024,
		ExecTimeout: 2 * time.Second,
	})
	oneShot := NewAgentKey(81, 82, 83, 1)
	result, err := oneShotRegistry.RunInTerminal(context.Background(), runtimecontext.ContainerTerminalExecInput{
		UserID:              oneShot.UserID,
		ProjectID:           oneShot.ProjectID,
		AgentSessionID:      oneShot.AgentSessionID,
		ContainerTemplateID: oneShot.TemplateID,
		Command:             "echo hi",
		Wait:                true,
		TimeoutSeconds:      2,
		CreateIfMissing:     true,
	})
	if err != nil {
		t.Fatalf("RunInTerminal: %v", err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Output, "echo:echo hi") {
		t.Fatalf("unexpected result: %+v", result)
	}
	if oneShotRegistry.Lookup(oneShot) != nil {
		t.Fatalf("one-shot session must be reaped after the command finishes")
	}
	store.mu.Lock()
	_, stillThere := store.rows[oneShot.SessionKey()]
	store.mu.Unlock()
	if stillThere {
		t.Fatalf("one-shot session record must be removed")
	}

	// 持久会话：执行完保留，第二次调用复用同一个 shell。
	// 用新的 opener（新的 fakeShell），因为一次性会话回收时已关闭上一个 shell。
	persistOpener := &fakeOpener{shell: newFakeShell()}
	persistRegistry := newTerminalSessionRegistry(persistOpener, nil, store, TerminalRegistryOptions{
		ReadyWait:   2 * time.Second,
		BufferBytes: 64 * 1024,
		ExecTimeout: 2 * time.Second,
	})
	persistent := NewAgentKey(84, 85, 86, 2)
	defer func() {
		if sess := persistRegistry.Lookup(persistent); sess != nil {
			persistRegistry.CloseSession(sess.Key())
		}
	}()
	for i := 0; i < 2; i++ {
		if _, err := persistRegistry.RunInTerminal(context.Background(), runtimecontext.ContainerTerminalExecInput{
			UserID:              persistent.UserID,
			ProjectID:           persistent.ProjectID,
			AgentSessionID:      persistent.AgentSessionID,
			ContainerTemplateID: persistent.TemplateID,
			Command:             "echo keep",
			Wait:                true,
			TimeoutSeconds:      2,
			CreateIfMissing:     true,
			Persistent:          true,
		}); err != nil {
			t.Fatalf("RunInTerminal persistent #%d: %v", i, err)
		}
		if persistRegistry.Lookup(persistent) == nil {
			t.Fatalf("persistent session must survive after the command finishes")
		}
	}
	oneShotOpener.mu.Lock()
	oneShotCalls := oneShotOpener.calls
	oneShotOpener.mu.Unlock()
	persistOpener.mu.Lock()
	persistCalls := persistOpener.calls
	persistOpener.mu.Unlock()
	if oneShotCalls != 1 || persistCalls != 1 {
		t.Fatalf("expected 1 creation per registry, got one-shot=%d persistent=%d", oneShotCalls, persistCalls)
	}
}

func TestTerminalSessionRegistryCreateOnlyOnceUnderConcurrency(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	registry := newTestRegistry(opener)
	key := NewAgentKey(4, 5, 6, 8)

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

	if sess := registry.Lookup(key); sess != nil {
		defer registry.CloseSession(sess.Key())
	}
	opener.mu.Lock()
	calls := opener.calls
	opener.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected one container creation, got %d", calls)
	}
}

// fakeTerminalStore 是 TerminalSessionStore 的内存实现。
type fakeTerminalStore struct {
	mu     sync.Mutex
	rows   map[string]*types.TerminalSession
	nextID int64
}

func newFakeTerminalStore() *fakeTerminalStore {
	return &fakeTerminalStore{rows: make(map[string]*types.TerminalSession)}
}

func (s *fakeTerminalStore) Get(_ context.Context, key TerminalSessionKey) (*types.TerminalSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.rows[key.SessionKey()]
	if !ok {
		return nil, nil
	}
	cp := *rec
	return &cp, nil
}

func (s *fakeTerminalStore) GetByID(_ context.Context, id int64) (*types.TerminalSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.rows {
		if rec.ID == id {
			cp := *rec
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *fakeTerminalStore) FindAgentSession(_ context.Context, userID, projectID, templateID, agentSessionID int64) (*types.TerminalSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if agentSessionID <= 0 {
		return nil, nil
	}
	for _, rec := range s.rows {
		if rec.UserID == userID && rec.ProjectID == projectID && rec.TemplateID == templateID && rec.AgentSessionID == agentSessionID {
			cp := *rec
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *fakeTerminalStore) Upsert(_ context.Context, rec *types.TerminalSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *rec
	if cp.ID == 0 {
		s.nextID++
		cp.ID = s.nextID
		rec.ID = cp.ID
	}
	s.rows[rec.SessionKey] = &cp
	return nil
}

func (s *fakeTerminalStore) Touch(_ context.Context, key TerminalSessionKey, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.rows[key.SessionKey()]; ok {
		rec.LastActiveAt = at
	}
	return nil
}

func (s *fakeTerminalStore) MarkPersistent(_ context.Context, key TerminalSessionKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.rows[key.SessionKey()]; ok {
		rec.Persistent = true
	}
	return nil
}

func (s *fakeTerminalStore) Delete(_ context.Context, key TerminalSessionKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, key.SessionKey())
	return nil
}

func (s *fakeTerminalStore) DeleteByID(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, rec := range s.rows {
		if rec.ID == id {
			delete(s.rows, key)
		}
	}
	return nil
}

func (s *fakeTerminalStore) List(context.Context) ([]*types.TerminalSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*types.TerminalSession, 0, len(s.rows))
	for _, rec := range s.rows {
		cp := *rec
		out = append(out, &cp)
	}
	return out, nil
}

func (s *fakeTerminalStore) ListByUser(_ context.Context, userID int64) ([]*types.TerminalSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*types.TerminalSession, 0, len(s.rows))
	for _, rec := range s.rows {
		if rec.UserID != userID || rec.Status != types.TerminalSessionStatusRunning {
			continue
		}
		cp := *rec
		out = append(out, &cp)
	}
	return out, nil
}

// 进程重启后：按 DB 记录认领原容器（不再新建），并从落盘的滚动缓冲回放历史输出。
func TestTerminalSessionRegistryAdoptsPersistedSession(t *testing.T) {
	dir := t.TempDir()
	// Agent 会话的终端按 (templateID, agentSessionID) 唯一，重启后靠该键找记录。
	key := NewAgentKey(31, 32, 33, 4).ReplaceID(555)

	opener := &fakeOpener{shell: newFakeShell()}
	store := newFakeTerminalStore()
	store.rows[key.SessionKey()] = &types.TerminalSession{
		ID:             key.ID,
		SessionKey:     key.SessionKey(),
		UserID:         key.UserID,
		ProjectID:      key.ProjectID,
		TemplateID:     key.TemplateID,
		AgentSessionID: key.AgentSessionID,
		RuntimeName:    "fake",
		RuntimeID:      "runtime-before-restart",
		Status:         types.TerminalSessionStatusRunning,
		LastActiveAt:   time.Now(),
	}

	scrollback, err := newTerminalScrollback(dir, key.SessionKey(), 1<<20)
	if err != nil {
		t.Fatalf("new scrollback: %v", err)
	}
	if err := scrollback.Save([]byte("before-restart-output\r\n")); err != nil {
		t.Fatalf("save scrollback: %v", err)
	}

	registry := newTerminalSessionRegistry(opener, nil, store, TerminalRegistryOptions{
		ReadyWait:     2 * time.Second,
		BufferBytes:   64 * 1024,
		SessionTTL:    time.Hour,
		ScrollbackDir: dir,
	})

	session, created, err := registry.Ensure(context.Background(), key.UIKey())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if created {
		t.Fatalf("expected adopted session, got newly created")
	}
	if session.ID() != strconv.FormatInt(key.ID, 10) {
		t.Fatalf("adopted session must keep its persisted id, got %q", session.ID())
	}
	opener.mu.Lock()
	attaches, starts := opener.attaches, opener.calls
	opener.mu.Unlock()
	if attaches != 1 || starts != 0 {
		t.Fatalf("expected attach(1) and no new container, got attaches=%d starts=%d", attaches, starts)
	}

	sub, replay := session.Attach(0)
	defer session.Detach(sub)
	if !strings.Contains(string(replay), "before-restart-output") {
		t.Fatalf("expected replayed scrollback, got %q", string(replay))
	}

	registry.CloseSession(session.Key())
	store.mu.Lock()
	_, stillThere := store.rows[key.SessionKey()]
	store.mu.Unlock()
	if stillThere {
		t.Fatalf("expected session record removed on close")
	}
}

// 新建会话必须拿到数据库分配的正数主键（对外 session_id）。
//
// 回归场景：新会话在落库前用的是负数占位 ID（newTerminalSessionPlaceholderID）。
// 如果把占位 ID 写进记录，Upsert 会以 -1 为主键插入，assignRecordID 又因 <=0 被忽略，
// 前端拿到的 session_id 就是 "-1"，随后 WS 按该 ID 找不到会话。
func TestTerminalSessionRegistryCreateAssignsPositiveID(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	store := newFakeTerminalStore()
	registry := newTerminalSessionRegistry(opener, nil, store, TerminalRegistryOptions{
		ReadyWait:   2 * time.Second,
		BufferBytes: 64 * 1024,
		SessionTTL:  time.Hour,
	})

	session, err := registry.CreateForTemplate(context.Background(), 91, 92, 93, "fresh", 24, 80)
	if err != nil {
		t.Fatalf("CreateForTemplate: %v", err)
	}
	defer registry.CloseSession(session.Key())

	id := session.recordID()
	if id <= 0 {
		t.Fatalf("new session must have a positive record id, got %d (session id %q)", id, session.ID())
	}
	if session.ID() != strconv.FormatInt(id, 10) {
		t.Fatalf("session id must be the string form of the record id, got %q want %d", session.ID(), id)
	}
	// 落库记录的主键必须与内存一致，且能被 ID 索引定位（WS / exec 都按它寻址）。
	store.mu.Lock()
	stored, ok := store.rows[session.Key().SessionKey()]
	var storedID int64
	if ok {
		storedID = stored.ID
	}
	store.mu.Unlock()
	if !ok || storedID != id {
		t.Fatalf("persisted row must carry the same id, got %d (found=%v)", storedID, ok)
	}
	if registry.LookupByID(id) != session {
		t.Fatalf("session must be addressable by its record id")
	}
}

// 进程重启后：列表必须来自数据库（内存为空也要有记录），且可以用同一 ID 重建会话。
func TestTerminalSessionRegistryListAndReviveAfterRestart(t *testing.T) {
	key := NewUIKey(51, 52, 53).ReplaceID(777)

	opener := &fakeOpener{shell: newFakeShell()}
	store := newFakeTerminalStore()
	rec := terminalSessionRecord(key, "runtime-before-restart")
	rec.Name = "my-terminal"
	rec.Persistent = true
	store.rows[key.SessionKey()] = rec

	registry := newTerminalSessionRegistry(opener, nil, store, TerminalRegistryOptions{
		ReadyWait:   2 * time.Second,
		BufferBytes: 64 * 1024,
		SessionTTL:  time.Hour,
	})

	// 内存注册表为空，但列表仍应返回数据库里的会话（未连接状态）。
	sessions := registry.ListSessions(key.UserID)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 persisted session in list, got %d", len(sessions))
	}
	if sessions[0].SessionID != "777" || sessions[0].Exists != true || sessions[0].Alive {
		t.Fatalf("unexpected list item: %+v", sessions[0])
	}
	if sessions[0].Name != "my-terminal" || !sessions[0].Persistent {
		t.Fatalf("expected record fields backfilled, got %+v", sessions[0])
	}

	// 用同一 ID 重连：自动认领原容器并重建 shell。
	sess, err := registry.ReviveByID(context.Background(), key.ID, 0, 0)
	if err != nil {
		t.Fatalf("ReviveByID: %v", err)
	}
	if sess.ID() != "777" {
		t.Fatalf("revived session must keep id, got %q", sess.ID())
	}
	opener.mu.Lock()
	attaches, starts := opener.attaches, opener.calls
	opener.mu.Unlock()
	if attaches != 1 || starts != 0 {
		t.Fatalf("expected adopt rather than new container, attaches=%d starts=%d", attaches, starts)
	}

	// 重建后列表用内存状态回填 Alive。
	sessions = registry.ListSessions(key.UserID)
	if len(sessions) != 1 || !sessions[0].Alive {
		t.Fatalf("expected alive session after revive, got %+v", sessions)
	}

	// 记录不存在时返回 NotFound，交调用方报 404。
	if _, err := registry.ReviveByID(context.Background(), 424242, 0, 0); !errors.Is(err, ErrTerminalSessionNotFound) {
		t.Fatalf("expected ErrTerminalSessionNotFound, got %v", err)
	}

	registry.CloseSession(sess.Key())
}

// 单用户会话配额：达到上限后新建会话被拒绝。
func TestTerminalSessionRegistryQuota(t *testing.T) {
	key := NewUIKey(41, 42, 43).ReplaceID(1)
	opener := &fakeOpener{shell: newFakeShell()}
	store := newFakeTerminalStore()
	start := terminalSessionRecord(key, "runtime-1")
	store.rows[key.SessionKey()] = start

	registry := newTerminalSessionRegistry(opener, nil, store, TerminalRegistryOptions{
		ReadyWait:          2 * time.Second,
		MaxSessionsPerUser: 1,
		SessionTTL:         time.Hour,
	})
	// 不同模板 → 不同会话，但同一用户已有一个会话。
	other := NewUIKey(key.UserID, key.ProjectID, 99)
	_, _, err := registry.Ensure(context.Background(), other)
	if err == nil || !errors.Is(err, ErrTerminalSessionQuotaExceeded) {
		t.Fatalf("expected quota error, got %v", err)
	}
}

func terminalSessionRecord(key TerminalSessionKey, runtimeID string) *types.TerminalSession {
	return &types.TerminalSession{
		ID:             key.ID,
		SessionKey:     key.SessionKey(),
		UserID:         key.UserID,
		ProjectID:      key.ProjectID,
		TemplateID:     key.TemplateID,
		AgentSessionID: key.AgentSessionID,
		RuntimeName:    "fake",
		RuntimeID:      runtimeID,
		Status:         types.TerminalSessionStatusRunning,
		LastActiveAt:   time.Now(),
	}
}

// recordPublisher 记录推送给前端的实时消息，用于断言会话事件。
type recordPublisher struct {
	mu   sync.Mutex
	msgs []map[string]any
}

func (p *recordPublisher) PushMessage(_ int64, data any) error {
	msg, _ := data.(map[string]any)
	p.mu.Lock()
	p.msgs = append(p.msgs, msg)
	p.mu.Unlock()
	return nil
}

// sessionEventMethods 返回推送过的会话事件 method（category=container-terminal-session）。
func (p *recordPublisher) sessionEventMethods() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.msgs))
	for _, msg := range p.msgs {
		if msg["action"] != "component.invoke" {
			continue
		}
		payload, ok := msg["payload"].(map[string]any)
		if !ok || payload["category"] != "container-terminal-session" {
			continue
		}
		if method, ok := payload["method"].(string); ok {
			out = append(out, method)
		}
	}
	return out
}

func hasSessionEvent(methods []string, want string) bool {
	for _, method := range methods {
		if method == want {
			return true
		}
	}
	return false
}

// 列表的 Running / Attached 标签依赖这些状态事件：订阅者与 busy 跳变时必须推送。
func TestTerminalSessionRegistryPushesStateEvents(t *testing.T) {
	publisher := &recordPublisher{}
	opener := &fakeOpener{shell: newFakeShell()}
	registry := newTerminalSessionRegistry(opener, publisher, nil, TerminalRegistryOptions{
		ReadyWait:   2 * time.Second,
		BufferBytes: 64 * 1024,
		ExecTimeout: 2 * time.Second,
	})
	ctx := context.Background()

	session, _, err := registry.EnsurePersistent(ctx, NewUIKey(11, 12, 13))
	if err != nil {
		t.Fatalf("EnsurePersistent: %v", err)
	}
	defer registry.CloseSession(session.Key())

	sub, _ := session.Attach(0)
	if methods := publisher.sessionEventMethods(); !hasSessionEvent(methods, "sessionAttached") {
		t.Fatalf("expected sessionAttached event, got %v", methods)
	}

	session.Detach(sub)
	if methods := publisher.sessionEventMethods(); !hasSessionEvent(methods, "sessionDetached") {
		t.Fatalf("expected sessionDetached event, got %v", methods)
	}

	if _, err := registry.Execute(ctx, session.Key(), TerminalCommand{
		Command:         "ls",
		Wait:            true,
		Timeout:         2 * time.Second,
		CreateIfMissing: true,
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	methods := publisher.sessionEventMethods()
	if !hasSessionEvent(methods, "sessionRunning") || !hasSessionEvent(methods, "sessionIdle") {
		t.Fatalf("expected sessionRunning/sessionIdle events, got %v", methods)
	}
}

// 一个容器模板可以创建多条「UI 手动创建」的会话：每次新建都是独立会话 / 容器。
func TestTerminalSessionRegistryCreateMultipleUISessionsPerTemplate(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	store := newFakeTerminalStore()
	registry := newTerminalSessionRegistry(opener, nil, store, TerminalRegistryOptions{
		ReadyWait:   2 * time.Second,
		BufferBytes: 64 * 1024,
		SessionTTL:  time.Hour,
	})

	a, err := registry.CreateForTemplate(context.Background(), 1, 2, 3, "first", 24, 80)
	if err != nil {
		t.Fatalf("CreateForTemplate #1: %v", err)
	}
	b, err := registry.CreateForTemplate(context.Background(), 1, 2, 3, "second", 24, 80)
	if err != nil {
		t.Fatalf("CreateForTemplate #2: %v", err)
	}
	defer registry.CloseSession(a.Key())
	defer registry.CloseSession(b.Key())

	if a.ID() == b.ID() {
		t.Fatalf("each UI create must produce a distinct terminal session, both got %s", a.ID())
	}
	if a.displayName() != "first" || b.displayName() != "second" {
		t.Fatalf("unexpected names: %q / %q", a.displayName(), b.displayName())
	}
	if registry.LookupByID(a.recordID()) != a || registry.LookupByID(b.recordID()) != b {
		t.Fatalf("both sessions must be addressable by their id")
	}
}

// Agent 侧同一个 (templateID, agentSessionID) 只会创建一个终端会话。
func TestTerminalSessionRegistryAgentSessionUniquePerTemplate(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	registry := newTestRegistry(opener)
	key := NewAgentKey(1, 2, 3, 42)

	first, created, err := registry.Ensure(context.Background(), key)
	if err != nil || !created {
		t.Fatalf("Ensure: created=%v err=%v", created, err)
	}
	defer registry.CloseSession(first.Key())

	second, createdAgain, err := registry.Ensure(context.Background(), key)
	if err != nil {
		t.Fatalf("Ensure again: %v", err)
	}
	if createdAgain || second != first {
		t.Fatalf("expected the same terminal session for the same agent session")
	}
	if hits := registry.Lookup(key); hits != first {
		t.Fatalf("Lookup by agent key must return the created session")
	}

	// 不同 Agent 会话 / 不同模板互不影响。
	otherAgent, createdOther, err := registry.Ensure(context.Background(), NewAgentKey(1, 2, 3, 43))
	if err != nil || !createdOther {
		t.Fatalf("Ensure other agent: created=%v err=%v", createdOther, err)
	}
	defer registry.CloseSession(otherAgent.Key())
	if otherAgent.ID() == first.ID() {
		t.Fatalf("different agent sessions must not share a terminal session")
	}
}
