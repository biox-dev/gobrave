package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/logger"
)

// 本文件实现「容器模板临时终端」的会话层。
//
// 背景：历史实现里每个 WebSocket 连接都会新建并销毁一个一次性探针容器，浏览器与
// Agent 各自持有一个容器，无法「向当前终端发送指令」。这里把 PTY 收敛到进程内的
// 会话注册表中：
//
//   - key = (userID, projectID, templateID)，与前端底部面板的 `terminal:<templateId>`
//     一一对应，同一个模板永远复用同一个容器与 shell；
//   - 一个 PTY 支持多个订阅者（浏览器 WebSocket 字节流 / 其它观察者），输出带序号
//     环形缓冲，支持断线重放；
//   - 命令写入通过串行队列执行，靠 shell 的 PROMPT_COMMAND 打出不可见的 OSC 标记
//     （ESC ] 633 ; GD ; <exit code> BEL）来判定「命令结束 + 退出码」，不解析提示符，
//     也不会在终端里留下难看的哨兵命令回显；
//   - 最后一个订阅者离开后不立刻删容器，保留 grace period，方便 Agent 继续执行、
//     也方便用户关掉面板后再打开看到上下文。

// TerminalSessionKey 是会话的唯一标识。
type TerminalSessionKey struct {
	UserID     int64
	ProjectID  int64
	TemplateID int64
}

// SessionID 返回稳定的会话 ID（同 key 永远同 ID，前端可据此复用标签页）。
func (k TerminalSessionKey) SessionID() string {
	return fmt.Sprintf("term-%d-%d-%d", k.UserID, k.ProjectID, k.TemplateID)
}

const (
	// terminalMarkerPrefix 是 shell 侧 PROMPT_COMMAND 写出的 OSC 标记前缀。
	// OSC 序列在 xterm.js 中不可见，因此不会污染终端画面。
	terminalMarkerPrefix = "\x1b]633;GD;"
	// terminalInitCommand 在每个会话的 shell 启动后执行一次，用于在每次提示符前
	// 输出上一条命令的退出码。
	terminalInitCommand = `PROMPT_COMMAND='__gobrave_rc=$?; printf "\033]633;GD;%s\007" "$__gobrave_rc"'`

	defaultTerminalIdleGrace   = 10 * time.Minute
	defaultTerminalReadyWait   = 60 * time.Second
	defaultTerminalBufferBytes = 256 * 1024
	defaultTerminalCreateWait  = 10 * time.Minute
	defaultTerminalExecTimeout = 5 * time.Minute
	terminalIdleThreshold      = 300 * time.Millisecond
	terminalIdleWait           = 3 * time.Second
	terminalExecQueueSize      = 16
	terminalSubscriberBufSize  = 2048
	terminalEventBufSize       = 64
)

var (
	// ErrTerminalSessionNotFound 表示没有可用的会话，且调用方不允许自动创建。
	ErrTerminalSessionNotFound = errors.New("terminal session not found")
	// ErrTerminalSessionClosed 表示会话已经结束（shell 退出或容器被删除）。
	ErrTerminalSessionClosed = errors.New("terminal session is closed")
	// ErrTerminalBusy 表示命令队列已满。
	ErrTerminalBusy = errors.New("terminal session is busy")
)

// TerminalEventPublisher 把「Agent 在终端里执行了命令」这类事件推给前端，
// 用于自动打开底部终端标签。*realtime.Hub 实现了该接口。
type TerminalEventPublisher interface {
	PushMessage(userID int64, data any) error
}

// TerminalRegistryOptions 控制会话生命周期，零值使用默认值。
type TerminalRegistryOptions struct {
	IdleGrace     time.Duration
	ReadyWait     time.Duration
	CreateWait    time.Duration
	BufferBytes   int
	ExecTimeout   time.Duration
	ExecQueueSize int
}

// TerminalCommand 是一次写入终端的命令。
type TerminalCommand struct {
	// Command 是要执行的 shell 命令，可多行。
	Command string
	// Wait 为 true 时等待命令结束后返回退出码与输出；false 时只负责投递。
	Wait bool
	// Timeout <= 0 时使用默认超时。
	Timeout time.Duration
	// Source 标记来源（ui / agent），用于前端展示与审计。
	Source string
	// NotifyUI 为 true 时通过 realtime 推送 terminal.exec 事件。
	NotifyUI bool
	// CreateIfMissing 为 true 时允许自动创建会话。
	CreateIfMissing bool
}

// TerminalCommandResult 是一次命令投递 / 执行的结果。
type TerminalCommandResult struct {
	SessionID       string
	Accepted        bool
	Running         bool
	ExitCode        int
	TimedOut        bool
	DurationSeconds float64
	Output          string
	OutputTruncated bool
}

// TerminalSessionStatus 是会话的只读状态。
type TerminalSessionStatus struct {
	SessionID   string `json:"session_id"`
	Exists      bool   `json:"exists"`
	Alive       bool   `json:"alive"`
	Subscribers int    `json:"subscribers"`
	Seq         int64  `json:"seq"`
}

// TerminalSubscriber 是一个终端输出订阅者（浏览器 WebSocket）。
type TerminalSubscriber struct {
	ID   int64
	Data chan []byte
	// Events 承载 JSON 控制事件（exec start/end、exit 等），由 handler 以文本帧发送。
	Events chan []byte

	done      chan struct{}
	closeOnce sync.Once
}

func newTerminalSubscriber(id int64) *TerminalSubscriber {
	return &TerminalSubscriber{
		ID:     id,
		Data:   make(chan []byte, terminalSubscriberBufSize),
		Events: make(chan []byte, terminalEventBufSize),
		done:   make(chan struct{}),
	}
}

// Done 在订阅者被关闭时关闭。
func (s *TerminalSubscriber) Done() <-chan struct{} { return s.done }

func (s *TerminalSubscriber) close() {
	s.closeOnce.Do(func() { close(s.done) })
}

// ManagedTerminalSession 是注册表持有一个 PTY（容器 + bash -l）。
type ManagedTerminalSession struct {
	id       string
	key      TerminalSessionKey
	sess     containerruntime.TerminalSession
	cleanup  func()
	registry *TerminalSessionRegistry

	// writeMu 串行化「整段命令 / 按键」的写入，避免两次写入交错。
	writeMu sync.Mutex

	mu          sync.Mutex
	closed      bool
	dead        bool
	deadErr     error
	ready       bool
	subs        map[int64]*TerminalSubscriber
	nextSubID   int64
	buf         []byte
	bufStart    int64 // buf[0] 对应的全局序号
	bufLimit    int
	lastOutput  time.Time
	reapTimer   *time.Timer
	markerCarry []byte
	readyOnce   sync.Once
	readyCh     chan struct{}
	done        chan struct{}
	closeOnce   sync.Once
	markerCh    chan int
	requests    chan terminalExecRequest
}

type terminalExecRequest struct {
	command string
	timeout time.Duration
	source  string
	result  chan terminalExecOutcome
}

type terminalExecOutcome struct {
	exitCode        int
	timedOut        bool
	duration        time.Duration
	output          string
	outputTruncated bool
	err             error
}

// TerminalRuntimeOpener 抽象「创建探针容器 + 打开交互式 shell」的能力。
// *ContainerProbeRunner 实现它；单测可注入 fake PTY。
type TerminalRuntimeOpener interface {
	OpenProbeTerminal(ctx context.Context, projectID, templateID int64, opts containerruntime.TerminalOptions) (containerruntime.TerminalSession, func(), error)
}

// TerminalSessionRegistry 维护 (userID, projectID, templateID) → PTY 的映射。
type TerminalSessionRegistry struct {
	probe     TerminalRuntimeOpener
	publisher TerminalEventPublisher
	opts      TerminalRegistryOptions

	mu       sync.Mutex
	sessions map[string]*ManagedTerminalSession
	pending  map[string]*pendingTerminalSession
}

type pendingTerminalSession struct {
	done chan struct{}
	sess *ManagedTerminalSession
	err  error
}

// NewTerminalSessionRegistry 构造会话注册表。
//
// probe 复用 ContainerProbeRunner 的容器组装链路（与 probe_container_env 口径一致）；
// publisher 可为 nil（例如单测），此时不推送前端事件。
func NewTerminalSessionRegistry(probe *ContainerProbeRunner, publisher TerminalEventPublisher) *TerminalSessionRegistry {
	return newTerminalSessionRegistry(probe, publisher, TerminalRegistryOptions{})
}

func newTerminalSessionRegistry(probe TerminalRuntimeOpener, publisher TerminalEventPublisher, opts TerminalRegistryOptions) *TerminalSessionRegistry {
	return &TerminalSessionRegistry{
		probe:     probe,
		publisher: publisher,
		opts:      opts,
		sessions:  make(map[string]*ManagedTerminalSession),
		pending:   make(map[string]*pendingTerminalSession),
	}
}

func (r *TerminalSessionRegistry) idleGrace() time.Duration {
	if r.opts.IdleGrace > 0 {
		return r.opts.IdleGrace
	}
	return defaultTerminalIdleGrace
}

func (r *TerminalSessionRegistry) readyWait() time.Duration {
	if r.opts.ReadyWait > 0 {
		return r.opts.ReadyWait
	}
	return defaultTerminalReadyWait
}

func (r *TerminalSessionRegistry) createWait() time.Duration {
	if r.opts.CreateWait > 0 {
		return r.opts.CreateWait
	}
	return defaultTerminalCreateWait
}

func (r *TerminalSessionRegistry) bufferBytes() int {
	if r.opts.BufferBytes > 0 {
		return r.opts.BufferBytes
	}
	return defaultTerminalBufferBytes
}

func (r *TerminalSessionRegistry) execQueueSize() int {
	if r.opts.ExecQueueSize > 0 {
		return r.opts.ExecQueueSize
	}
	return terminalExecQueueSize
}

func (r *TerminalSessionRegistry) execTimeout() time.Duration {
	if r.opts.ExecTimeout > 0 {
		return r.opts.ExecTimeout
	}
	return defaultTerminalExecTimeout
}

// Ensure 返回（必要时创建）会话，并等待 shell 进入可执行状态。
//
// created 表示本次调用是否新建了容器。创建使用独立的后台 context，不跟随 HTTP /
// WebSocket 请求的生命周期：调用方响应结束后会话必须继续存活。
func (r *TerminalSessionRegistry) Ensure(ctx context.Context, key TerminalSessionKey) (*ManagedTerminalSession, bool, error) {
	id := key.SessionID()

	r.mu.Lock()
	if sess, ok := r.sessions[id]; ok && !sess.isDead() {
		r.mu.Unlock()
		return sess, false, nil
	}
	if wait, ok := r.pending[id]; ok {
		r.mu.Unlock()
		select {
		case <-wait.done:
			return wait.sess, true, wait.err
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	pending := &pendingTerminalSession{done: make(chan struct{})}
	r.pending[id] = pending
	r.mu.Unlock()

	sess, err := r.createSession(key)

	r.mu.Lock()
	delete(r.pending, id)
	if err == nil {
		r.sessions[id] = sess
	}
	pending.sess = sess
	pending.err = err
	close(pending.done)
	r.mu.Unlock()

	if err == nil {
		// 无人订阅时也要回收：Agent 可能先创建会话（HTTP exec）却始终没人打开终端面板。
		// 订阅者接入后 Attach 会停掉这个计时器。
		r.scheduleReap(sess)
	}

	return sess, true, err
}

func (r *TerminalSessionRegistry) createSession(key TerminalSessionKey) (*ManagedTerminalSession, error) {
	if r.probe == nil {
		return nil, fmt.Errorf("container probe is not configured")
	}

	createCtx, cancel := context.WithTimeout(context.Background(), r.createWait())
	defer cancel()

	// 用较宽的初始终端尺寸，前端连接后会用 resize 控制帧覆盖。
	session, cleanup, err := r.probe.OpenProbeTerminal(createCtx, key.ProjectID, key.TemplateID, containerruntime.TerminalOptions{
		Command: []string{"/bin/bash", "-l"},
		TTY:     true,
		Rows:    24,
		Cols:    80,
	})
	if err != nil {
		return nil, err
	}

	managed := &ManagedTerminalSession{
		id:         key.SessionID(),
		key:        key,
		sess:       session,
		cleanup:    cleanup,
		registry:   r,
		subs:       make(map[int64]*TerminalSubscriber),
		bufLimit:   r.bufferBytes(),
		readyCh:    make(chan struct{}),
		done:       make(chan struct{}),
		markerCh:   make(chan int, 8),
		lastOutput: time.Now(),
	}
	managed.start()

	// 注入提示符标记；命令本身会被 shell 回显一次，因此只写一行。
	managed.writeMu.Lock()
	_, writeErr := managed.sess.Write([]byte(terminalInitCommand + "\n"))
	managed.writeMu.Unlock()
	if writeErr != nil {
		managed.Close()
		return nil, fmt.Errorf("initialize terminal shell: %w", writeErr)
	}

	if err := managed.waitReady(r.readyWait()); err != nil {
		managed.Close()
		return nil, err
	}
	return managed, nil
}

// Lookup 返回存活会话（不创建）。
func (r *TerminalSessionRegistry) Lookup(key TerminalSessionKey) *ManagedTerminalSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	sess, ok := r.sessions[key.SessionID()]
	if !ok || sess.isDead() {
		return nil
	}
	return sess
}

// Status 返回会话状态；不存在时 Exists=false。
func (r *TerminalSessionRegistry) Status(key TerminalSessionKey) TerminalSessionStatus {
	r.mu.Lock()
	sess, ok := r.sessions[key.SessionID()]
	r.mu.Unlock()
	if !ok || sess == nil {
		return TerminalSessionStatus{SessionID: key.SessionID()}
	}
	subs, seq, alive := sess.snapshotStatus()
	return TerminalSessionStatus{
		SessionID:   key.SessionID(),
		Exists:      true,
		Alive:       alive,
		Subscribers: subs,
		Seq:         seq,
	}
}

// CloseSession 主动结束会话（删除容器）。
func (r *TerminalSessionRegistry) CloseSession(key TerminalSessionKey) bool {
	r.mu.Lock()
	sess, ok := r.sessions[key.SessionID()]
	if ok {
		delete(r.sessions, key.SessionID())
	}
	r.mu.Unlock()
	if !ok || sess == nil {
		return false
	}
	sess.Close()
	return true
}

// Execute 把命令写入会话的串行队列。
//
// Wait=false 时只投递并立即返回；Wait=true 时等待命令结束（靠 OSC 标记判定），
// 超时返回 TimedOut=true 与已产生的输出。
func (r *TerminalSessionRegistry) Execute(ctx context.Context, key TerminalSessionKey, cmd TerminalCommand) (TerminalCommandResult, error) {
	if strings.TrimSpace(cmd.Command) == "" {
		return TerminalCommandResult{}, fmt.Errorf("command is required")
	}

	sess := r.Lookup(key)
	if sess == nil {
		if !cmd.CreateIfMissing {
			return TerminalCommandResult{}, ErrTerminalSessionNotFound
		}
		created, _, err := r.Ensure(ctx, key)
		if err != nil {
			return TerminalCommandResult{}, err
		}
		sess = created
	}

	if cmd.NotifyUI {
		r.notifyExec(sess, cmd, "start", nil)
	}

	timeout := cmd.Timeout
	if timeout <= 0 {
		timeout = r.execTimeout()
	}

	req := terminalExecRequest{
		command: cmd.Command,
		timeout: timeout,
		source:  cmd.Source,
		result:  make(chan terminalExecOutcome, 1),
	}

	select {
	case sess.requests <- req:
	default:
		return TerminalCommandResult{}, ErrTerminalBusy
	}

	if !cmd.Wait {
		go func() {
			outcome := <-req.result
			if cmd.NotifyUI {
				r.notifyExec(sess, cmd, "end", &outcome)
			}
			if outcome.err != nil {
				logger.Warnf(ctx, "[TerminalSession] async exec failed session=%s err=%v", sess.ID(), outcome.err)
			}
		}()
		return TerminalCommandResult{
			SessionID: sess.ID(),
			Accepted:  true,
			Running:   true,
		}, nil
	}

	sessionID := sess.ID()
	select {
	case outcome := <-req.result:
		if cmd.NotifyUI {
			r.notifyExec(sess, cmd, "end", &outcome)
		}
		if outcome.err != nil {
			return TerminalCommandResult{SessionID: sessionID}, outcome.err
		}
		return TerminalCommandResult{
			SessionID:       sessionID,
			Accepted:        true,
			ExitCode:        outcome.exitCode,
			TimedOut:        outcome.timedOut,
			DurationSeconds: outcome.duration.Seconds(),
			Output:          outcome.output,
			OutputTruncated: outcome.outputTruncated,
		}, nil
	case <-ctx.Done():
		return TerminalCommandResult{SessionID: sessionID}, ctx.Err()
	}
}

// RunInTerminal 实现 runtimecontext.ContainerTerminalRunner，供 Agent 工具注入。
func (r *TerminalSessionRegistry) RunInTerminal(ctx context.Context, in runtimecontext.ContainerTerminalExecInput) (runtimecontext.ContainerTerminalExecResult, error) {
	result, err := r.Execute(ctx, TerminalSessionKey{
		UserID:     in.UserID,
		ProjectID:  in.ProjectID,
		TemplateID: in.ContainerTemplateID,
	}, TerminalCommand{
		Command:         in.Command,
		Wait:            in.Wait,
		Timeout:         time.Duration(in.TimeoutSeconds) * time.Second,
		Source:          "agent",
		NotifyUI:        in.NotifyUI,
		CreateIfMissing: in.CreateIfMissing,
	})
	if err != nil {
		return runtimecontext.ContainerTerminalExecResult{SessionID: result.SessionID}, err
	}
	return runtimecontext.ContainerTerminalExecResult{
		SessionID:       result.SessionID,
		Accepted:        result.Accepted,
		Running:         result.Running,
		ExitCode:        result.ExitCode,
		TimedOut:        result.TimedOut,
		DurationSeconds: result.DurationSeconds,
		Output:          result.Output,
		OutputTruncated: result.OutputTruncated,
	}, nil
}

func (r *TerminalSessionRegistry) notifyExec(sess *ManagedTerminalSession, cmd TerminalCommand, phase string, outcome *terminalExecOutcome) {
	if r.publisher == nil || sess == nil {
		return
	}
	payload := map[string]any{
		"template_id": strconv.FormatInt(sess.key.TemplateID, 10),
		"session_id":  sess.ID(),
		"command":     cmd.Command,
		"source":      cmd.Source,
		"phase":       phase,
	}
	if outcome != nil {
		payload["exit_code"] = outcome.exitCode
		payload["timed_out"] = outcome.timedOut
	}
	msg := map[string]any{
		"action":  "terminal.exec",
		"payload": payload,
	}
	if err := r.publisher.PushMessage(sess.key.UserID, msg); err != nil {
		if !strings.Contains(err.Error(), "no_client_for_user:") {
			logger.Warnf(context.Background(), "[TerminalSession] push terminal event failed user=%d session=%s err=%v", sess.key.UserID, sess.ID(), err)
		}
	}
}

// forget 从注册表中移除会话（仅当指针一致）。
func (r *TerminalSessionRegistry) forget(sess *ManagedTerminalSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.sessions[sess.id]; ok && current == sess {
		delete(r.sessions, sess.id)
	}
}

func (r *TerminalSessionRegistry) scheduleReap(sess *ManagedTerminalSession) {
	sess.mu.Lock()
	if sess.closed || len(sess.subs) > 0 {
		sess.mu.Unlock()
		return
	}
	if sess.reapTimer != nil {
		sess.reapTimer.Stop()
	}
	grace := r.idleGrace()
	sess.reapTimer = time.AfterFunc(grace, func() {
		sess.mu.Lock()
		empty := !sess.closed && len(sess.subs) == 0
		sess.mu.Unlock()
		if !empty {
			return
		}
		logger.Infof(context.Background(), "[TerminalSession] releasing idle session %s after %s", sess.id, grace)
		r.forget(sess)
		sess.Close()
	})
	sess.mu.Unlock()
}

// ===== ManagedTerminalSession =====

func (s *ManagedTerminalSession) start() {
	s.requests = make(chan terminalExecRequest, s.registry.execQueueSize())
	go s.readLoop()
	go s.execLoop()
}

// ID 返回会话 ID。
func (s *ManagedTerminalSession) ID() string { return s.id }

// Key 返回会话 key。
func (s *ManagedTerminalSession) Key() TerminalSessionKey { return s.key }

// WriteInput 写入用户按键（终端原始输入）。
func (s *ManagedTerminalSession) WriteInput(data []byte) (int, error) {
	if s.isDead() {
		return 0, ErrTerminalSessionClosed
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.sess.Write(data)
}

// Resize 调整 PTY 尺寸。
func (s *ManagedTerminalSession) Resize(rows, cols uint16) error {
	if s.isDead() {
		return ErrTerminalSessionClosed
	}
	return s.sess.Resize(rows, cols)
}

// Attach 注册一个订阅者并返回重放数据。
//
// sinceSeq <= 0 表示「尽可能早」；sinceSeq 大于当前序号表示只要新输出。
// 重放与订阅在同一临界区内完成，不会丢帧。
func (s *ManagedTerminalSession) Attach(sinceSeq int64) (*TerminalSubscriber, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.reapTimer != nil {
		s.reapTimer.Stop()
		s.reapTimer = nil
	}

	s.nextSubID++
	sub := newTerminalSubscriber(s.nextSubID)
	s.subs[sub.ID] = sub

	var replay []byte
	if sinceSeq >= 0 {
		start := s.bufStart
		if sinceSeq > start {
			start = sinceSeq
		}
		if start < s.bufStart {
			start = s.bufStart
		}
		offset := start - s.bufStart
		if offset < int64(len(s.buf)) {
			replay = append([]byte(nil), s.buf[offset:]...)
		}
	}
	return sub, replay
}

// Detach 注销订阅者；最后一个订阅者离开后进入 grace 计时。
func (s *ManagedTerminalSession) Detach(sub *TerminalSubscriber) {
	if sub == nil {
		return
	}
	s.mu.Lock()
	if _, ok := s.subs[sub.ID]; ok {
		delete(s.subs, sub.ID)
	}
	empty := len(s.subs) == 0
	s.mu.Unlock()

	sub.close()

	if empty {
		s.registry.scheduleReap(s)
	}
}

// SendEvent 向所有订阅者广播一条 JSON 控制事件。
func (s *ManagedTerminalSession) SendEvent(payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sub := range s.subs {
		select {
		case sub.Events <- data:
		default:
		}
	}
}

// Close 关闭 shell 并删除容器；可重复调用。
func (s *ManagedTerminalSession) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.dead = true
		subs := make([]*TerminalSubscriber, 0, len(s.subs))
		for _, sub := range s.subs {
			subs = append(subs, sub)
		}
		s.subs = nil
		close(s.done)
		s.mu.Unlock()

		for _, sub := range subs {
			sub.close()
		}
		if s.sess != nil {
			_ = s.sess.Close()
		}
		if s.cleanup != nil {
			s.cleanup()
		}
	})
}

func (s *ManagedTerminalSession) isDead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dead || s.closed
}

func (s *ManagedTerminalSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *ManagedTerminalSession) snapshotStatus() (subs int, seq int64, alive bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs), s.bufStart + int64(len(s.buf)), !s.dead && !s.closed
}

func (s *ManagedTerminalSession) markReady() {
	s.readyOnce.Do(func() {
		s.mu.Lock()
		s.ready = true
		s.mu.Unlock()
		close(s.readyCh)
	})
}

func (s *ManagedTerminalSession) waitReady(timeout time.Duration) error {
	select {
	case <-s.readyCh:
		return nil
	case <-s.done:
		return ErrTerminalSessionClosed
	case <-time.After(timeout):
		return fmt.Errorf("terminal shell did not become ready within %s (prompt marker missing; check the template image has bash)", timeout)
	}
}

func (s *ManagedTerminalSession) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.sess.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			codes := s.feedMarkers(chunk)
			s.publish(chunk)
			if len(codes) > 0 {
				s.markReady()
				for _, code := range codes {
					select {
					case s.markerCh <- code:
					default:
					}
				}
			}
		}
		if err != nil {
			s.handleReadError(err)
			return
		}
	}
}

// feedMarkers 从输出流中解析 OSC 标记（可能跨 chunk），返回退出码序列。
func (s *ManagedTerminalSession) feedMarkers(chunk []byte) []int {
	s.markerCarry = append(s.markerCarry, chunk...)
	var codes []int
	prefix := []byte(terminalMarkerPrefix)
	for {
		idx := bytes.Index(s.markerCarry, prefix)
		if idx < 0 {
			keep := len(prefix) - 1
			if len(s.markerCarry) > keep {
				s.markerCarry = append([]byte(nil), s.markerCarry[len(s.markerCarry)-keep:]...)
			}
			return codes
		}
		rest := s.markerCarry[idx+len(prefix):]
		end := bytes.IndexByte(rest, 0x07)
		if end < 0 {
			if len(rest) > 64 {
				// 非法标记：丢弃已扫描部分，避免无限增长。
				s.markerCarry = append([]byte(nil), rest...)
				continue
			}
			return codes
		}
		if code, err := strconv.Atoi(strings.TrimSpace(string(rest[:end]))); err == nil {
			codes = append(codes, code)
		}
		s.markerCarry = append([]byte(nil), rest[end+1:]...)
	}
}

func (s *ManagedTerminalSession) publish(chunk []byte) {
	s.mu.Lock()
	s.lastOutput = time.Now()
	s.buf = append(s.buf, chunk...)
	if s.bufLimit > 0 && len(s.buf) > s.bufLimit {
		drop := len(s.buf) - s.bufLimit
		s.buf = append([]byte(nil), s.buf[drop:]...)
		s.bufStart += int64(drop)
	}
	for _, sub := range s.subs {
		select {
		case sub.Data <- chunk:
		default:
			// 订阅者消费不过来：丢弃该块（前端可通过 since 重放补齐）。
		}
	}
	s.mu.Unlock()
}

func (s *ManagedTerminalSession) handleReadError(err error) {
	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		return
	}
	s.dead = true
	s.deadErr = err
	s.mu.Unlock()

	// shell 退出属于正常结束（用户输入 exit），不打印告警；其它错误记录一下。
	if !isTerminalEOF(err) {
		logger.Warnf(context.Background(), "[TerminalSession] session %s read closed: %v", s.id, err)
	}
	s.SendEvent(map[string]any{"type": "exit", "session_id": s.id})
	s.registry.forget(s)
	s.Close()
}

func (s *ManagedTerminalSession) currentSeq() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bufStart + int64(len(s.buf))
}

func (s *ManagedTerminalSession) readSince(seq int64) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := seq
	if start < s.bufStart {
		start = s.bufStart
	}
	offset := start - s.bufStart
	if offset >= int64(len(s.buf)) {
		return nil
	}
	return append([]byte(nil), s.buf[offset:]...)
}

func (s *ManagedTerminalSession) drainMarkers() {
	for {
		select {
		case <-s.markerCh:
		default:
			return
		}
	}
}

// waitIdle 等待输出静止，降低「命令正在运行 / 用户正在输入」时把命令写进错误进程的概率。
func (s *ManagedTerminalSession) waitIdle(ctx context.Context) {
	deadline := time.Now().Add(terminalIdleWait)
	for {
		s.mu.Lock()
		last := s.lastOutput
		s.mu.Unlock()
		if time.Since(last) >= terminalIdleThreshold {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *ManagedTerminalSession) execLoop() {
	for {
		select {
		case <-s.done:
			return
		case req := <-s.requests:
			outcome := s.runCommand(req)
			if req.result != nil {
				req.result <- outcome
			}
		}
	}
}

func (s *ManagedTerminalSession) runCommand(req terminalExecRequest) terminalExecOutcome {
	if s.isClosed() {
		return terminalExecOutcome{err: ErrTerminalSessionClosed}
	}

	// 先等输出静止，再清掉上一条命令残留的标记：
	// 顺序反过来会把「等待期间产生的旧标记」误当成本次命令的退出码。
	waitCtx, cancel := context.WithTimeout(context.Background(), terminalIdleWait+time.Second)
	s.waitIdle(waitCtx)
	cancel()
	s.drainMarkers()

	startSeq := s.currentSeq()
	startedAt := time.Now()

	s.SendEvent(map[string]any{
		"type":       "exec",
		"phase":      "start",
		"session_id": s.id,
		"command":    req.command,
		"source":     req.source,
	})

	s.writeMu.Lock()
	_, writeErr := s.sess.Write([]byte(strings.TrimRight(req.command, "\n") + "\n"))
	s.writeMu.Unlock()
	if writeErr != nil {
		outcome := terminalExecOutcome{err: writeErr, duration: time.Since(startedAt)}
		s.SendEvent(map[string]any{"type": "exec", "phase": "end", "session_id": s.id, "error": writeErr.Error()})
		return outcome
	}

	timer := time.NewTimer(req.timeout)
	defer timer.Stop()

	finish := func(exitCode int, timedOut bool) terminalExecOutcome {
		out := s.readSince(startSeq)
		output, truncated := truncateTerminalOutput(string(stripTerminalMarkers(out)), maxProbeLogBytes)
		s.SendEvent(map[string]any{
			"type":       "exec",
			"phase":      "end",
			"session_id": s.id,
			"exit_code":  exitCode,
			"timed_out":  timedOut,
		})
		return terminalExecOutcome{
			exitCode:        exitCode,
			timedOut:        timedOut,
			duration:        time.Since(startedAt),
			output:          output,
			outputTruncated: truncated,
		}
	}

	for {
		select {
		case code := <-s.markerCh:
			return finish(code, false)
		case <-timer.C:
			return finish(-1, true)
		case <-s.done:
			return terminalExecOutcome{err: ErrTerminalSessionClosed, duration: time.Since(startedAt), exitCode: -1}
		}
	}
}

// stripTerminalMarkers 去掉输出里的 OSC 633 标记，避免模型看到控制字符。
func stripTerminalMarkers(data []byte) []byte {
	prefix := []byte(terminalMarkerPrefix)
	out := make([]byte, 0, len(data))
	for {
		idx := bytes.Index(data, prefix)
		if idx < 0 {
			out = append(out, data...)
			return out
		}
		out = append(out, data[:idx]...)
		rest := data[idx+len(prefix):]
		end := bytes.IndexByte(rest, 0x07)
		if end < 0 {
			return out
		}
		data = rest[end+1:]
	}
}

func truncateTerminalOutput(text string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text, false
	}
	return "...(truncated)\n" + text[len(text)-maxBytes:], true
}

func isTerminalEOF(err error) bool {
	if err == nil {
		return true
	}
	msg := strings.ToLower(err.Error())
	return errors.Is(err, context.Canceled) || msg == "eof" || strings.Contains(msg, "closed")
}
