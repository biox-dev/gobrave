package manager

import (
	"context"
	"strings"
	"testing"
	"time"
)

// newReplayTestSession 建一个持久会话并执行一条命令，返回会话与完整缓冲。
func newReplayTestSession(t *testing.T, key TerminalSessionKey) (*TerminalSessionRegistry, *ManagedTerminalSession, TerminalReplayInfo) {
	t.Helper()
	opener := &fakeOpener{shell: newFakeShell()}
	registry := newTestRegistry(opener)
	t.Cleanup(func() { registry.CloseSession(key) })

	created, _, err := registry.EnsurePersistent(context.Background(), key)
	if err != nil {
		t.Fatalf("EnsurePersistent: %v", err)
	}
	if _, err := registry.Execute(context.Background(), created.Key(), TerminalCommand{
		Command:         "pwd",
		Wait:            true,
		Timeout:         2 * time.Second,
		CreateIfMissing: true,
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	session := registry.Lookup(created.Key())
	if session == nil {
		t.Fatalf("expected live session")
	}
	sub, full := session.AttachWithInfo(0)
	session.Detach(sub)
	return registry, session, full
}

// 从中间序号续传时必须精确对齐：不回退、不跳过。
func TestTerminalAttachWithInfoResumesFromSeq(t *testing.T) {
	key := TerminalSessionKey{UserID: 201, ProjectID: 202, TemplateID: 203}
	_, session, full := newReplayTestSession(t, key)

	if full.Reset || full.Truncated {
		t.Fatalf("first attach must not reset/truncate: %+v", full)
	}
	if full.From != 0 || full.To != int64(len(full.Data)) {
		t.Fatalf("unexpected replay window from=%d to=%d len=%d", full.From, full.To, len(full.Data))
	}
	if !strings.Contains(string(full.Data), "echo:pwd") {
		t.Fatalf("expected command output in replay, got %q", string(full.Data))
	}

	mid := full.To / 2
	sub, resumed := session.AttachWithInfo(mid)
	defer session.Detach(sub)

	if resumed.Reset || resumed.Truncated {
		t.Fatalf("mid-sequence resume must not reset/truncate: %+v", resumed)
	}
	if resumed.From != mid || resumed.To != full.To {
		t.Fatalf("unexpected resumed window from=%d to=%d want from=%d to=%d", resumed.From, resumed.To, mid, full.To)
	}
	if string(resumed.Data) != string(full.Data[mid:]) {
		t.Fatalf("resumed bytes mismatch: got %q want %q", string(resumed.Data), string(full.Data[mid:]))
	}

	// 续传到末尾：只要新输出。
	sub2, tail := session.AttachWithInfo(full.To)
	session.Detach(sub2)
	if len(tail.Data) != 0 {
		t.Fatalf("expected empty replay at seq=end, got %q", string(tail.Data))
	}
}

// 客户端序号超出当前会话（会话被重建 / 认领后序号基准改变）时，
// 必须整段回放并置 Reset，否则前端会拿到空回放、历史全丢。
func TestTerminalAttachWithInfoResetsWhenSeqAhead(t *testing.T) {
	key := TerminalSessionKey{UserID: 211, ProjectID: 212, TemplateID: 213}
	_, session, full := newReplayTestSession(t, key)

	sub, info := session.AttachWithInfo(full.To + 4096)
	defer session.Detach(sub)

	if !info.Reset {
		t.Fatalf("expected Reset when client seq is ahead of the session")
	}
	if info.Truncated {
		t.Fatalf("unexpected Truncated: %+v", info)
	}
	if info.From != full.From || info.To != full.To {
		t.Fatalf("reset replay must cover the whole buffer: %+v", info)
	}
	if string(info.Data) != string(full.Data) {
		t.Fatalf("reset replay mismatch: got %q want %q", string(info.Data), string(full.Data))
	}
}

// 环形缓冲滚动后，客户端要求的位置早于 bufStart：置 Truncated 并从 bufStart 回放。
func TestTerminalAttachWithInfoTruncatedAfterRollover(t *testing.T) {
	key := TerminalSessionKey{UserID: 221, ProjectID: 222, TemplateID: 223}
	opener := &fakeOpener{shell: newFakeShell()}
	registry := newTerminalSessionRegistry(opener, nil, nil, TerminalRegistryOptions{
		ReadyWait:   2 * time.Second,
		SessionTTL:  time.Hour,
		BufferBytes: 256,
		ExecTimeout: 2 * time.Second,
	})
	defer registry.CloseSession(key)

	created, _, err := registry.EnsurePersistent(context.Background(), key)
	if err != nil {
		t.Fatalf("EnsurePersistent: %v", err)
	}
	if _, err := registry.Execute(context.Background(), created.Key(), TerminalCommand{
		Command:         strings.Repeat("x", 512),
		Wait:            true,
		Timeout:         2 * time.Second,
		CreateIfMissing: true,
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	session := registry.Lookup(created.Key())
	if session == nil {
		t.Fatalf("expected live session")
	}

	session.mu.Lock()
	start := session.bufStart
	buffered := len(session.buf)
	session.mu.Unlock()
	if start == 0 {
		t.Fatalf("expected the ring buffer to roll over (bufStart=0)")
	}
	if buffered > 256 {
		t.Fatalf("buffer exceeded limit: %d", buffered)
	}

	sub, info := session.AttachWithInfo(1)
	defer session.Detach(sub)

	if !info.Truncated {
		t.Fatalf("expected Truncated for a position below bufStart")
	}
	if info.From != start || info.To != start+int64(buffered) {
		t.Fatalf("unexpected truncated window: from=%d to=%d want from=%d to=%d", info.From, info.To, start, start+int64(buffered))
	}
}

// PTY 尺寸以调用方 / 前端 xterm 为准：
//   - 新建会话直接使用上报尺寸；没上报则回落到默认 24x80；
//   - 已存活会话在 attach（ReviveByID）时用上报尺寸 Resize，不跨会话记忆尺寸。
func TestTerminalSessionRegistryUsesBrowserPTYSize(t *testing.T) {
	opener := &fakeOpener{shell: newFakeShell()}
	registry := newTestRegistry(opener)
	key := TerminalSessionKey{UserID: 241, ProjectID: 242, TemplateID: 243}
	defer registry.CloseSession(key)

	session, created, err := registry.EnsurePersistentSized(context.Background(), key, 40, 132)
	if err != nil {
		t.Fatalf("EnsurePersistentSized: %v", err)
	}
	if !created {
		t.Fatalf("expected a newly created session")
	}
	if rows, cols := session.Size(); rows != 40 || cols != 132 {
		t.Fatalf("unexpected PTY size %dx%d, want 40x132", rows, cols)
	}

	// attach 时上报的尺寸直接作用于存活会话。
	if _, err := registry.ReviveByID(context.Background(), session.recordID(), 50, 160); err != nil {
		t.Fatalf("ReviveByID: %v", err)
	}
	if rows, cols := session.Size(); rows != 50 || cols != 160 {
		t.Fatalf("expected PTY resized to 50x160, got %dx%d", rows, cols)
	}

	// 不带尺寸的调用（Agent 的 terminal_exec）回落到默认尺寸，不再继承上次尺寸。
	registry.CloseSession(session.Key())

	recreated, _, err := ensureOneShot(registry, key)
	if err != nil {
		t.Fatalf("ensureOneShot after close: %v", err)
	}
	if rows, cols := recreated.Size(); rows != 24 || cols != 80 {
		t.Fatalf("expected default PTY size 24x80, got %dx%d", rows, cols)
	}
}
