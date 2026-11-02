package manager

import (
	"bufio"
	"context"
	"errors"
	"io"
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
	shell    *fakeShell
	calls    int
	attaches int
	mu       sync.Mutex
}

func (o *fakeOpener) StartTerminalRuntime(_ context.Context, _, _, _ int64, _ containerruntime.TerminalOptions) (*ProbeTerminalHandle, error) {
	o.mu.Lock()
	o.calls++
	o.mu.Unlock()
	return &ProbeTerminalHandle{
		RuntimeName: "fake",
		RuntimeID:   "fake-runtime-1",
		Session:     o.shell,
		Cleanup:     func() {},
	}, nil
}

func (o *fakeOpener) AttachTerminalRuntime(_ context.Context, _, runtimeID string, _ containerruntime.TerminalOptions) (*ProbeTerminalHandle, error) {
	o.mu.Lock()
	o.attaches++
	o.mu.Unlock()
	return &ProbeTerminalHandle{
		RuntimeName: "fake",
		RuntimeID:   runtimeID,
		Session:     o.shell,
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

	// 用持久会话验证「超时后仍可回放」：一次性会话在命令结束且无订阅者时会被回收。
	if _, _, err := registry.EnsurePersistent(context.Background(), key); err != nil {
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

// 一次性会话（Agent 创建、从未被前端接入）：最后一个订阅者离开后立即删除容器与记录。
func TestTerminalSessionRegistryReapsOneShotSessionOnLastDetach(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	store := newFakeTerminalStore()
	registry := newTerminalSessionRegistry(opener, nil, store, TerminalRegistryOptions{
		ReadyWait:   2 * time.Second,
		BufferBytes: 64 * 1024,
	})
	key := TerminalSessionKey{UserID: 11, ProjectID: 12, TemplateID: 13}

	session, created, err := registry.Ensure(context.Background(), key)
	if err != nil || !created {
		t.Fatalf("Ensure: created=%v err=%v", created, err)
	}
	if session.isPersistent() {
		t.Fatalf("agent-created session must not be persistent by default")
	}

	sub, _ := session.Attach(0)
	select {
	case <-opener.shell.closed:
		t.Fatalf("terminal must not be closed while subscribed")
	default:
	}

	session.Detach(sub)

	if registry.Lookup(key) != nil {
		t.Fatalf("expected one-shot session removed immediately after last detach")
	}
	select {
	case <-opener.shell.closed:
	default:
		t.Fatalf("expected underlying terminal closed immediately")
	}
	if status := registry.Status(key); status.Exists {
		t.Fatalf("expected status not exists, got %+v", status)
	}
	store.mu.Lock()
	_, stillThere := store.rows[key.SessionID()]
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
	key := TerminalSessionKey{UserID: 21, ProjectID: 22, TemplateID: 23}

	session, _, err := registry.EnsurePersistent(context.Background(), key)
	if err != nil {
		t.Fatalf("EnsurePersistent: %v", err)
	}
	if !session.isPersistent() {
		t.Fatalf("expected persistent session")
	}

	sub, _ := session.Attach(0)
	session.Detach(sub)

	if registry.Lookup(key) == nil {
		t.Fatalf("persistent session must survive last detach")
	}
	select {
	case <-opener.shell.closed:
		t.Fatalf("terminal must stay open for persistent session")
	default:
	}
	store.mu.Lock()
	rec, ok := store.rows[key.SessionID()]
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
	key := TerminalSessionKey{UserID: 71, ProjectID: 72, TemplateID: 73}

	session, created, err := registry.Ensure(context.Background(), key) // Agent 创建：一次性
	if err != nil || !created {
		t.Fatalf("Ensure: created=%v err=%v", created, err)
	}
	if session.isPersistent() {
		t.Fatalf("expected non-persistent session after agent create")
	}

	// 前端打开同一个模板的终端：复用同一会话并升级为持久。
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
	key := TerminalSessionKey{UserID: 61, ProjectID: 62, TemplateID: 63}

	session, _, err := registry.EnsurePersistent(context.Background(), key)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	sub, _ := session.Attach(0)
	defer session.Detach(sub)

	time.Sleep(30 * time.Millisecond)
	registry.maintain(context.Background())

	if registry.Lookup(key) == nil {
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
	oneShot := TerminalSessionKey{UserID: 81, ProjectID: 82, TemplateID: 83}
	result, err := oneShotRegistry.RunInTerminal(context.Background(), runtimecontext.ContainerTerminalExecInput{
		UserID:              oneShot.UserID,
		ProjectID:           oneShot.ProjectID,
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
	_, stillThere := store.rows[oneShot.SessionID()]
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
	persistent := TerminalSessionKey{UserID: 84, ProjectID: 85, TemplateID: 86}
	defer persistRegistry.CloseSession(persistent)
	for i := 0; i < 2; i++ {
		if _, err := persistRegistry.RunInTerminal(context.Background(), runtimecontext.ContainerTerminalExecInput{
			UserID:              persistent.UserID,
			ProjectID:           persistent.ProjectID,
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

// fakeTerminalStore 是 TerminalSessionStore 的内存实现。
type fakeTerminalStore struct {
	mu   sync.Mutex
	rows map[string]*types.TerminalSession
}

func newFakeTerminalStore() *fakeTerminalStore {
	return &fakeTerminalStore{rows: make(map[string]*types.TerminalSession)}
}

func (s *fakeTerminalStore) Get(_ context.Context, key TerminalSessionKey) (*types.TerminalSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.rows[key.SessionID()]
	if !ok {
		return nil, nil
	}
	cp := *rec
	return &cp, nil
}

func (s *fakeTerminalStore) Upsert(_ context.Context, rec *types.TerminalSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *rec
	s.rows[rec.SessionKey] = &cp
	return nil
}

func (s *fakeTerminalStore) Touch(_ context.Context, key TerminalSessionKey, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.rows[key.SessionID()]; ok {
		rec.LastActiveAt = at
	}
	return nil
}

func (s *fakeTerminalStore) MarkPersistent(_ context.Context, key TerminalSessionKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.rows[key.SessionID()]; ok {
		rec.Persistent = true
	}
	return nil
}

func (s *fakeTerminalStore) Delete(_ context.Context, key TerminalSessionKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, key.SessionID())
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

// 进程重启后：按 DB 记录认领原容器（不再新建），并从落盘的滚动缓冲回放历史输出。
func TestTerminalSessionRegistryAdoptsPersistedSession(t *testing.T) {
	dir := t.TempDir()
	key := TerminalSessionKey{UserID: 31, ProjectID: 32, TemplateID: 33}

	opener := &fakeOpener{shell: newFakeShell()}
	store := newFakeTerminalStore()
	store.rows[key.SessionID()] = &types.TerminalSession{
		SessionKey:   key.SessionID(),
		UserID:       key.UserID,
		ProjectID:    key.ProjectID,
		TemplateID:   key.TemplateID,
		RuntimeName:  "fake",
		RuntimeID:    "runtime-before-restart",
		Status:       types.TerminalSessionStatusRunning,
		LastActiveAt: time.Now(),
	}

	scrollback, err := newTerminalScrollback(dir, key.SessionID(), 1<<20)
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

	session, created, err := registry.Ensure(context.Background(), key)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if created {
		t.Fatalf("expected adopted session, got newly created")
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

	registry.CloseSession(key)
	if _, err := store.Get(context.Background(), key); err != nil {
		t.Fatalf("get after close: %v", err)
	}
	store.mu.Lock()
	_, stillThere := store.rows[key.SessionID()]
	store.mu.Unlock()
	if stillThere {
		t.Fatalf("expected session record removed on close")
	}
}

// 单用户会话配额：达到上限后新建会话被拒绝。
func TestTerminalSessionRegistryQuota(t *testing.T) {
	key := TerminalSessionKey{UserID: 41, ProjectID: 42, TemplateID: 43}
	opener := &fakeOpener{shell: newFakeShell()}
	store := newFakeTerminalStore()
	start := terminalSessionRecord(key, "runtime-1")
	store.rows[key.SessionID()] = start

	registry := newTerminalSessionRegistry(opener, nil, store, TerminalRegistryOptions{
		ReadyWait:          2 * time.Second,
		MaxSessionsPerUser: 1,
		SessionTTL:         time.Hour,
	})
	// 不同模板 → 不同 key，但同一用户已有一个会话。
	other := TerminalSessionKey{UserID: key.UserID, ProjectID: key.ProjectID, TemplateID: 99}
	_, _, err := registry.Ensure(context.Background(), other)
	if err == nil || !errors.Is(err, ErrTerminalSessionQuotaExceeded) {
		t.Fatalf("expected quota error, got %v", err)
	}
}

func terminalSessionRecord(key TerminalSessionKey, runtimeID string) *types.TerminalSession {
	return &types.TerminalSession{
		SessionKey:   key.SessionID(),
		UserID:       key.UserID,
		ProjectID:    key.ProjectID,
		TemplateID:   key.TemplateID,
		RuntimeName:  "fake",
		RuntimeID:    runtimeID,
		Status:       types.TerminalSessionStatusRunning,
		LastActiveAt: time.Now(),
	}
}
