package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/config"
	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
)

// 本文件实现「容器模板临时终端」的会话层。
//
// 背景：历史实现里每个 WebSocket 连接都会新建并销毁一个一次性探针容器，浏览器与
// Agent 各自持有一个容器，无法「向当前终端发送指令」。这里把 PTY 收敛到进程内的
// 会话注册表中：
//
//   - 会话以终端会话主键 ID（types.TerminalSession.ID）为唯一标识，对外接口
//     （WebSocket / exec / status / close）全部按该 ID 寻址；
//   - 会话的创建与 WebSocket 连接解耦：POST /container/terminal/session/create
//     由 (userID, projectID, templateID) 创建并立即拉起容器与 shell；WS 只是 attach；
//   - 一个容器模板可以有多个终端会话：
//     * 用户从 UI 手动创建（AgentSessionID=0）：每点一次「新建」都建一条新会话；
//     * Agent 通过 terminal_exec 创建（AgentSessionID=agent_sessions.id）：
//       同一 (templateID, agentSessionID) 唯一，复用同一个 PTY；
//   - 一个 PTY 支持多个订阅者（浏览器 WebSocket 字节流 / 其它观察者），输出带序号
//     环形缓冲，支持断线重放；
//   - 命令写入通过串行队列执行，靠 shell 的 PROMPT_COMMAND 打出不可见的 OSC 标记
//     （ESC ] 633 ; GD ; <exit code> BEL）来判定「命令结束 + 退出码」，不解析提示符，
//     也不会在终端里留下难看的哨兵命令回显；
//   - 删除时机由「会话是否持久」决定（types.TerminalSession.Persistent）：
//     * 持久会话（UI 创建 / WS 接入过 / Agent 显式要求）：只能由空闲 TTL
//       （container.terminal_session_ttl_minutes）或用户显式
//       DELETE /container/terminal/session/{id} 回收；
//     * 一次性会话（Agent 创建且从未被前端接入）：命令结束、最后一个订阅者离开时
//       立即回收容器与记录；
//   - 持久标记只增不减：一次性会话经前端打开后即变为持久；
//   - 会话 → 容器的映射落在 go_terminal_session（container.terminal_session_ttl_minutes
//     控制空闲 TTL，terminal_max_sessions_per_user 控制配额），进程重启后可认领原容器；
//   - 历史输出以滚动快照落盘（container.terminal_buffer_dir），认领后回放。

// TerminalSessionKey 定位一个终端会话。
//
// 会话的对外标识是终端会话主键（见 TerminalSessionRef.ID）；这里保留
// (userID, projectID, templateID, agentSessionID) 是为了：
//   - Agent 侧按 (templateID, agentSessionID) 查找 / 复用会话；
//   - UI 手动创建时按模板新建会话；
//   - 由 key 派生出 SessionKey（唯一键 + 滚动缓冲文件名）。
type TerminalSessionKey struct {
	UserID     int64
	ProjectID  int64
	TemplateID int64
	// AgentSessionID 是创建该终端会话的 Agent 会话 ID（agent_sessions.id）；
	// 0 表示用户从 UI 手动创建。
	AgentSessionID int64
	// ID 是终端会话主键。>0 表示已定位到具体会话；0 表示「按模板创建新会话」。
	ID int64
	// Name 是会话展示名（仅创建请求用；不参与 SessionKey 计算）。
	Name string
}

// NewUIKey 返回「用户从 UI 在某个模板上新建会话」的 key（尚未分配终端会话 ID）。
func NewUIKey(userID, projectID, templateID int64) TerminalSessionKey {
	return TerminalSessionKey{UserID: userID, ProjectID: projectID, TemplateID: templateID}
}

// NewAgentKey 返回「某个 Agent 会话在某个模板上的终端」的 key。
//
// 同一 (templateID, agentSessionID) 只会对应一个终端会话（见 registry 的
// resolveRecordKey），因此 Agent 侧不需要关心终端会话 ID。
func NewAgentKey(userID, projectID, templateID, agentSessionID int64) TerminalSessionKey {
	return TerminalSessionKey{
		UserID:         userID,
		ProjectID:      projectID,
		TemplateID:     templateID,
		AgentSessionID: agentSessionID,
	}
}

// UIKey 返回「去掉终端会话 ID」后的 key：用于从 ID 反向定位 (user/template/agent session)。
func (k TerminalSessionKey) UIKey() TerminalSessionKey {
	k.ID = 0
	return k
}

// SessionKey 返回稳定的会话标识。
//
// 只作为唯一键与滚动缓冲文件名，对外（接口 / 前端）一律使用 ID。
// ID == 0 表示「尚未分配」（创建请求），此时用 "new" 占位。
func (k TerminalSessionKey) SessionKey() string {
	id := "new"
	if k.ID != 0 {
		id = strconv.FormatInt(k.ID, 10)
	}
	return fmt.Sprintf("term-%d-%d-%d-%d-%s", k.UserID, k.ProjectID, k.TemplateID, k.AgentSessionID, id)
}

// SessionID 返回稳定的会话 ID（= SessionKey，仅供日志 / 内部使用）。
func (k TerminalSessionKey) SessionID() string { return k.SessionKey() }

// ReplaceID 返回把终端会话主键替换为 id 的 key 副本。
func (k TerminalSessionKey) ReplaceID(id int64) TerminalSessionKey {
	k.ID = id
	return k
}

const (
	// terminalMarkerPrefix 是 shell 侧 PROMPT_COMMAND 写出的 OSC 标记前缀。
	// OSC 序列在 xterm.js 中不可见，因此不会污染终端画面。
	terminalMarkerPrefix = "\x1b]633;GD;"
	// terminalInitCommand 在每个会话的 shell 启动后执行一次，用于在每次提示符前
	// 输出上一条命令的退出码。
	terminalInitCommand = `PROMPT_COMMAND='__gobrave_rc=$?; printf "\033]633;GD;%s\007" "$__gobrave_rc"'`

	defaultTerminalReadyWait   = 60 * time.Second
	defaultTerminalBufferBytes = 256 * 1024
	defaultTerminalCreateWait  = 10 * time.Minute
	defaultTerminalExecTimeout = 5 * time.Minute
	// defaultTerminalSessionTTL 是「持久会话」的空闲上限：超过即回收容器并删记录。
	defaultTerminalSessionTTL         = 2 * time.Hour
	defaultTerminalMaxSessionsPerUser = 4
	defaultTerminalScrollbackBytes    = 1024 * 1024
	terminalIdleThreshold             = 300 * time.Millisecond
	terminalIdleWait                  = 3 * time.Second
	terminalExecQueueSize             = 16
	terminalSubscriberBufSize         = 2048
	terminalEventBufSize              = 64
	terminalScrollbackFlushInterval   = 2 * time.Second
	terminalMaintenanceInterval       = time.Minute
)

var (
	// ErrTerminalSessionNotFound 表示没有可用的会话，且调用方不允许自动创建。
	ErrTerminalSessionNotFound = errors.New("terminal session not found")
	// ErrTerminalSessionClosed 表示会话已经结束（shell 退出或容器被删除）。
	ErrTerminalSessionClosed = errors.New("terminal session is closed")
	// ErrTerminalBusy 表示命令队列已满。
	ErrTerminalBusy = errors.New("terminal session is busy")
	// ErrTerminalSessionQuotaExceeded 表示当前用户的终端会话数已达上限。
	ErrTerminalSessionQuotaExceeded = errors.New("terminal session quota exceeded")
)

// TerminalEventPublisher 把「Agent 在终端里执行了命令」这类事件推给前端，
// 用于自动打开底部终端标签。*realtime.Hub 实现了该接口。
type TerminalEventPublisher interface {
	PushMessage(userID int64, data any) error
}

// TerminalRegistryOptions 控制会话生命周期，零值使用默认值。
type TerminalRegistryOptions struct {
	// SessionTTL 是持久会话的空闲上限：超过即回收容器并删除记录。<=0 使用默认 2 小时。
	SessionTTL time.Duration
	// MaxSessionsPerUser 限制单个用户同时存在的终端会话数；<=0 使用默认 4。
	MaxSessionsPerUser int
	// ScrollbackDir / ScrollbackBytes 控制滚动缓冲落盘目录与单会话上限。
	ScrollbackDir   string
	ScrollbackBytes int
	ReadyWait       time.Duration
	CreateWait      time.Duration
	BufferBytes     int
	ExecTimeout     time.Duration
	ExecQueueSize   int
}

// TerminalReplayInfo 描述一次回放覆盖的序号区间。
type TerminalReplayInfo struct {
	// Data 是从 From 到 To 的输出字节。客户端下次重连应把 To + 本连接新收到的字节数
	// 作为新的 since 传回来（不要自己从 0 开始数字节，否则缓冲滚动后会错位）。
	Data []byte
	// From / To 是本次回放在会话输出流里的序号区间（[From, To)）。
	From int64
	To   int64
	// Truncated 表示客户端请求的位置早于环形缓冲起点：中间有一段输出已被滚动覆盖。
	Truncated bool
	// Reset 表示客户端持有的序号超出了本会话（会话被重建 / 认领后序号基准改变），
	// 已回退为「整段重放」，客户端应先清屏再写入 Data，否则会出现重复内容。
	Reset bool
}

// terminalSize 是 PTY 的字符尺寸。
//
// 它必须和浏览器 xterm 的尺寸一致：PTY 宽度决定了历史输出的换行位置，也决定了
// bash/readline 对「提示符占几行」的判断。宽度变了以后 bash 会按旧宽度重绘提示符
// （\r ESC[K ESC[A ...），在更宽的 xterm 里那个 ESC[A 会落到上一行，把已经输出的
// 历史行覆盖掉。所以新建会话直接用前端上报的尺寸，前端接入时再用 Resize 对齐。
type terminalSize struct {
	rows uint16
	cols uint16
}

func (s terminalSize) valid() bool { return s.rows > 0 && s.cols > 0 }

// normalize 补齐缺失维度（默认 24x80）。
func (s terminalSize) normalize() terminalSize {
	if s.rows == 0 {
		s.rows = 24
	}
	if s.cols == 0 {
		s.cols = 80
	}
	return s
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
	// Persistent 为 true 时，本命令「新建」的会话直接按持久会话创建。
	//
	// 原因：一次性会话在命令结束且没有订阅者时会被立即回收（容器也会删除），
	// 因此「先投递命令、再由前端打开终端看输出」的场景必须建持久会话，
	// 否则命令执行完容器就没了，前端随后接入拿到的是另一台新容器（看不到输出）。
	Persistent bool
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
	// SessionID 是终端会话主键（types.TerminalSession.ID）的字符串形式，
	// 也是 WS / exec / status / close 接口里使用的 {id}。
	SessionID   string `json:"session_id"`
	Exists      bool   `json:"exists"`
	Alive       bool   `json:"alive"`
	Subscribers int    `json:"subscribers"`
	Seq         int64  `json:"seq"`

	// 以下字段只在「按用户列出会话」时填充，供左侧栏列表 join 容器模板展示。
	UserID     int64 `json:"user_id,omitempty,string"`
	ProjectID  int64 `json:"project_id,omitempty,string"`
	TemplateID int64 `json:"template_id,omitempty,string"`
	// AgentSessionID 是创建该终端会话的 Agent 会话 ID；0 表示 UI 手动创建。
	AgentSessionID int64 `json:"agent_session_id,omitempty,string"`
	// Name 是会话展示名（UI 创建时写入；Agent 创建时取模板展示名）。
	Name string `json:"name,omitempty"`
	// Persistent 为 true 表示会话只能由空闲 TTL 或显式关闭回收。
	Persistent bool `json:"persistent"`
	// Busy 表示当前有命令在执行（此时空闲 TTL 不会回收容器）。
	Busy bool `json:"busy"`
	// RuntimeID / RuntimeName 是底层容器标识（docker id / k8s workload），便于排查。
	RuntimeID   string `json:"runtime_id,omitempty"`
	RuntimeName string `json:"runtime_name,omitempty"`
	// CreatedAt 是会话在内存注册表里的创建时间；LastActive 是最近一次操作时间。
	CreatedAt  time.Time `json:"created_at,omitempty"`
	LastActive time.Time `json:"last_active,omitempty"`
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
	id          string
	key         TerminalSessionKey
	sess        containerruntime.TerminalSession
	cleanup     func()
	registry    *TerminalSessionRegistry
	store       TerminalSessionStore
	scrollback  *terminalScrollback
	runtimeID   string
	runtimeName string

	// writeMu 串行化「整段命令 / 按键」的写入，避免两次写入交错。
	writeMu sync.Mutex

	mu         sync.Mutex
	closed     bool
	dead       bool
	deadErr    error
	ready      bool
	subs       map[int64]*TerminalSubscriber
	nextSubID  int64
	buf        []byte
	bufStart   int64 // buf[0] 对应的全局序号
	bufLimit   int
	lastOutput time.Time
	// lastActive 记录「用户/Agent 最近一次操作」时间，用于空闲 TTL 回收。
	lastActive time.Time
	// createdAt 是会话在注册表内的创建时间，仅用于列表展示。
	createdAt time.Time
	// busy 表示有命令正在执行：空闲 TTL 回收必须跳过，避免杀掉长任务。
	busy            bool
	scrollbackDirty bool
	// persistent 标记会话是否为持久会话（见 types.TerminalSession.Persistent）。
	// 只会在 WS（ContainerTemplateTerminal）接入、UI 手动创建或 Agent 显式要求时从 false 变 true。
	persistent bool
	// name 是会话展示名（UI 创建时由用户 / 模板提供，Agent 创建时取模板展示名）。
	name string
	// rows / cols 是 PTY 当前尺寸；前端据此渲染回放，保证与历史输出的宽度一致。
	rows        uint16
	cols        uint16
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

// TerminalRuntimeOpener 抽象「创建探针容器 / 认领已有容器 + 打开交互式 shell」的能力。
// *ContainerProbeRunner 实现它；单测可注入 fake PTY。
type TerminalRuntimeOpener interface {
	StartTerminalRuntime(ctx context.Context, userID, projectID, templateID int64, opts containerruntime.TerminalOptions) (*ProbeTerminalHandle, error)
	AttachTerminalRuntime(ctx context.Context, runtimeName, runtimeID string, opts containerruntime.TerminalOptions) (*ProbeTerminalHandle, error)
	DropTerminalRuntime(ctx context.Context, runtimeName, runtimeID string)
}

// TerminalSessionRegistry 维护终端会话 ID → PTY 的映射。
type TerminalSessionRegistry struct {
	probe     TerminalRuntimeOpener
	publisher TerminalEventPublisher
	store     TerminalSessionStore
	opts      TerminalRegistryOptions

	mu sync.Mutex
	// sessions 是唯一的会话索引：键是终端会话主键（types.TerminalSession.ID）。
	// 会话落库前使用进程内负数占位 ID，落库后换成真实主键（见 assignRecordID），
	// 因此「按 ID 定位」在整个生命周期都成立，不需要第二套索引。
	sessions map[int64]*ManagedTerminalSession
	// pending 是「创建中」的占位表：此时会话还没有主键（甚至还没有数据库记录），
	// 只能用「身份」做键，避免同一 Agent 会话并发建出多个 PTY。键的形态：
	//   - 按会话主键重建：id:<会话主键>
	//   - 按 Agent 会话新建：(userID, projectID, templateID, agentSessionID)
	pending map[string]*pendingTerminalSession
}

type pendingTerminalSession struct {
	done    chan struct{}
	sess    *ManagedTerminalSession
	created bool
	err     error
}

// NewTerminalSessionRegistry 构造会话注册表。
//
// probe 复用 ContainerProbeRunner 的容器组装链路（与 probe_container_env 口径一致）；
// publisher 可为 nil（例如单测），此时不推送前端事件；store 记录会话→容器的映射，
// 供进程重启后认领；cfg 决定回收策略 / 配额 / 滚动缓冲目录。
func NewTerminalSessionRegistry(probe *ContainerProbeRunner, publisher TerminalEventPublisher, store TerminalSessionStore, cfg *config.Config) *TerminalSessionRegistry {
	registry := newTerminalSessionRegistry(probe, publisher, store, terminalRegistryOptions(cfg))
	// 注册表在进程启动时构造一次（Router → ContainerHandler），在这里拉起后台维护：
	// 空闲 TTL 回收 + 跨进程残留对账。放在构造函数里而不是 dig Invoke，
	// 避免最小依赖图的单元测试强制解析 ContainerCreateWorker 的全部依赖。
	registry.StartMaintenance(context.Background())
	return registry
}

// terminalRegistryOptions 把配置转换成注册表选项。
// 未配置（或 cfg 为 nil）时 IdleGrace = 0，即「最后一个订阅者离开立即删除容器」。
func terminalRegistryOptions(cfg *config.Config) TerminalRegistryOptions {
	opts := TerminalRegistryOptions{}
	if cfg == nil || cfg.Container == nil {
		return opts
	}
	opts.SessionTTL = time.Duration(cfg.Container.TerminalSessionTTLMinutes) * time.Minute
	opts.MaxSessionsPerUser = cfg.Container.TerminalMaxSessionsPerUser
	opts.ScrollbackDir = cfg.Container.TerminalBufferDir
	opts.ScrollbackBytes = cfg.Container.TerminalBufferKB * 1024
	if opts.ScrollbackDir == "" && cfg.Storage != nil && strings.TrimSpace(cfg.Storage.BaseDir) != "" {
		opts.ScrollbackDir = filepath.Join(cfg.Storage.BaseDir, "terminal_sessions")
	}
	return opts
}

func newTerminalSessionRegistry(probe TerminalRuntimeOpener, publisher TerminalEventPublisher, store TerminalSessionStore, opts TerminalRegistryOptions) *TerminalSessionRegistry {
	return &TerminalSessionRegistry{
		probe:     probe,
		publisher: publisher,
		store:     store,
		opts:      opts,
		sessions:  make(map[int64]*ManagedTerminalSession),
		pending:   make(map[string]*pendingTerminalSession),
	}
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

func (r *TerminalSessionRegistry) sessionTTL() time.Duration {
	if r.opts.SessionTTL > 0 {
		return r.opts.SessionTTL
	}
	return defaultTerminalSessionTTL
}

func (r *TerminalSessionRegistry) maxSessionsPerUser() int {
	if r.opts.MaxSessionsPerUser > 0 {
		return r.opts.MaxSessionsPerUser
	}
	return defaultTerminalMaxSessionsPerUser
}

func (r *TerminalSessionRegistry) scrollbackBytes() int {
	if r.opts.ScrollbackBytes > 0 {
		return r.opts.ScrollbackBytes
	}
	return defaultTerminalScrollbackBytes
}

// Ensure 返回（必要时创建）会话，并等待 shell 进入可执行状态。
//
// created 表示本次调用是否新建了容器。创建使用独立的后台 context，不跟随 HTTP /
// WebSocket 请求的生命周期：调用方响应结束后会话必须继续存活。
// Create 创建（或复用）一条终端会话并立即拉起容器与 shell。
//
// 与 WebSocket 连接解耦：UI 的「新建终端会话」按钮走这个入口，创建成功后再用
// 返回的会话 ID 打开 WS。生命周期规则：
//   - key.AgentSessionID == 0（UI 手动创建）：总是新建一条独立会话，持久会话；
//   - key.AgentSessionID > 0（Agent 创建）：同一 (templateID, agentSessionID) 只有一条，
//     已存在则复用，持久性由 persistent 决定。
//
// created 表示本次调用是否新建了容器。
func (r *TerminalSessionRegistry) Create(ctx context.Context, key TerminalSessionKey, persistent bool, size terminalSize) (*ManagedTerminalSession, bool, error) {
	return r.ensure(ctx, key, persistent, size)
}

// CreateForTemplate 按容器模板新建一条「UI 手动创建」的终端会话并立即启动。
//
// 一个模板可以有多条这样的会话（每次点「新建」都新建一条），彼此独立。
// name 是会话展示名（前端 / 模板名），持久会话（只能由空闲 TTL 或显式关闭回收）。
func (r *TerminalSessionRegistry) CreateForTemplate(ctx context.Context, userID, projectID, templateID int64, name string, rows, cols uint16) (*ManagedTerminalSession, error) {
	size := terminalSize{rows: rows, cols: cols}
	key := NewUIKey(userID, projectID, templateID)
	key.Name = strings.TrimSpace(name)
	sess, _, err := r.ensure(ctx, key, true, size)
	if err != nil {
		return nil, err
	}
	if key.Name != "" {
		if sess.setName(key.Name) {
			r.persistName(ctx, sess)
		}
	}
	return sess, nil
}

// Ensure 按 key 返回（必要时创建）会话，默认一次性（Agent 语义）。
func (r *TerminalSessionRegistry) Ensure(ctx context.Context, key TerminalSessionKey) (*ManagedTerminalSession, bool, error) {
	return r.ensure(ctx, key, false, terminalSize{})
}

// EnsurePersistent 与 Ensure 相同，但会把会话标记为持久会话（见 types.TerminalSession.Persistent）。
//
// WebSocket 接入（ContainerTemplateTerminal）走这个入口：无论会话是否新建、是否复用，
// 只要前端接入了，此后就只能由空闲 TTL 或显式 DELETE 回收。
func (r *TerminalSessionRegistry) EnsurePersistent(ctx context.Context, key TerminalSessionKey) (*ManagedTerminalSession, bool, error) {
	return r.ensure(ctx, key, true, terminalSize{})
}

// EnsurePersistentSized 与 EnsurePersistent 相同，但会带上前端 xterm 的行列：
// 新建的会话直接以这个尺寸创建 PTY；已存活的会话保持原 PTY，接入方再调用 Resize 同步。
func (r *TerminalSessionRegistry) EnsurePersistentSized(ctx context.Context, key TerminalSessionKey, rows, cols uint16) (*ManagedTerminalSession, bool, error) {
	return r.ensure(ctx, key, true, terminalSize{rows: rows, cols: cols})
}

func (r *TerminalSessionRegistry) ensure(ctx context.Context, key TerminalSessionKey, persistent bool, size terminalSize) (*ManagedTerminalSession, bool, error) {
	// 先把「Agent 会话身份」解析成终端会话主键：之后 UI / Agent 走同一条路。
	resolved, err := r.resolveRecordKey(ctx, key)
	if err != nil {
		return nil, false, err
	}
	key = resolved

	// pending 用「身份」做键（见 TerminalSessionRegistry.pending）：创建期间会话还没有主键。
	// UI 的「新建」（key.ID == 0 且 agentSessionID == 0）永远建新会话，无需去重。
	pendingID := ""
	if key.ID != 0 {
		pendingID = "id:" + strconv.FormatInt(key.ID, 10)
	} else if key.AgentSessionID > 0 {
		pendingID = key.UIKey().SessionKey()
	}

	// 已定位到具体会话且内存里有：直接复用；否则认领原容器 / 重建。
	//
	// 「已有会话」的复查与 pending 占位必须在同一个临界区内完成，否则会漏判：
	// A 先查到没有会话 → B 建完并删掉 pending → A 走到这里发现没有 pending，
	// 于是又建了一个容器（同一个 Agent 会话拉起两台）。
	r.mu.Lock()
	if sess := r.lookupLocked(key); sess != nil {
		r.mu.Unlock()
		return r.reuseExisting(ctx, sess, persistent), false, nil
	}
	if pendingID != "" {
		if wait, ok := r.pending[pendingID]; ok {
			r.mu.Unlock()
			return r.waitForPending(ctx, wait, persistent)
		}
		r.pending[pendingID] = &pendingTerminalSession{done: make(chan struct{})}
	}
	r.mu.Unlock()

	sess, created, err := r.createOrAdopt(key, persistent, size)

	r.mu.Lock()
	var pending *pendingTerminalSession
	if pendingID != "" {
		pending = r.pending[pendingID]
		delete(r.pending, pendingID)
	}
	if err == nil {
		r.sessions[sess.recordID()] = sess
	}
	if pending != nil {
		pending.sess = sess
		pending.created = created
		pending.err = err
		close(pending.done)
	}
	r.mu.Unlock()

	if err == nil {
		// 通知前端「终端会话列表」刷新（新建 / 复用都推，列表侧重拉是幂等的）。
		r.notifySessionChanged(sess, "sessionCreated")
	}

	return sess, created, err
}

// resolveRecordKey 把「Agent 会话身份」解析成终端会话主键；其它形态原样返回。
//
// Agent 只带 (userID, projectID, templateID, agentSessionID)，这里先换算出主键，
// 之后就和「UI 打开已有会话」完全同路：内存复用 → 认领原容器 → 必要时重建。
// 取值顺序：内存里已有存活会话 → 数据库里的记录（进程重启前建的）→ 都没有则返回 ID = 0 交给调用方新建。
func (r *TerminalSessionRegistry) resolveRecordKey(ctx context.Context, key TerminalSessionKey) (TerminalSessionKey, error) {
	if key.ID != 0 || key.AgentSessionID <= 0 {
		return key, nil
	}
	if sess := r.lookupByAgentSession(key); sess != nil {
		return sess.Key(), nil
	}
	if r.store == nil {
		return key, nil
	}
	rec, err := r.store.FindAgentSession(ctx, key.UserID, key.ProjectID, key.TemplateID, key.AgentSessionID)
	if err != nil {
		return key, err
	}
	if rec == nil || rec.Status != types.TerminalSessionStatusRunning || strings.TrimSpace(rec.RuntimeID) == "" {
		return key, nil
	}
	// 记录还在：用记录主键走认领（adoptSession 会校验空闲 TTL 与容器可用性）。
	return key.ReplaceID(rec.ID), nil
}

// reuseExisting 是 ensure 的「命中已存活会话」分支：按需升级持久标记并推一次列表刷新。
//
// 调用方不得持有 r.mu（MarkPersistent / notifySessionChanged 会重新加锁）。
func (r *TerminalSessionRegistry) reuseExisting(ctx context.Context, sess *ManagedTerminalSession, persistent bool) *ManagedTerminalSession {
	if persistent {
		r.MarkPersistent(ctx, sess.Key())
	}
	// 复用也要通知：Agent 的 terminal_exec 大多复用已存活会话，
	// 不推前端列表就会表现为「执行了但列表没刷新」。
	r.notifySessionChanged(sess, "sessionCreated")
	return sess
}

// waitForPending 等待同一个 pending 创建完成，并复用它建好的会话。
//
// 调用方已释放 r.mu；pending 的 sess / created / err 都在 close(done) 之前写入，
// 因此这里的读取与创建方之间由 channel close 建立了 happens-before 关系。
func (r *TerminalSessionRegistry) waitForPending(ctx context.Context, wait *pendingTerminalSession, persistent bool) (*ManagedTerminalSession, bool, error) {
	select {
	case <-wait.done:
		if wait.err != nil {
			return wait.sess, wait.created, wait.err
		}
		return r.reuseExisting(ctx, wait.sess, persistent), wait.created, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

// lookupLocked 按身份在内存里找存活会话；调用方必须持有 r.mu。
//
// key.ID 优先，其次是 (userID, projectID, templateID, agentSessionID)。
func (r *TerminalSessionRegistry) lookupLocked(key TerminalSessionKey) *ManagedTerminalSession {
	if key.ID != 0 {
		return r.lookupByIDLocked(key.ID)
	}
	if key.AgentSessionID <= 0 {
		return nil
	}
	return r.lookupByAgentSessionLocked(key)
}

// lookupByIDLocked 按终端会话主键查找存活会话；调用方必须持有 r.mu。
func (r *TerminalSessionRegistry) lookupByIDLocked(id int64) *ManagedTerminalSession {
	if id == 0 {
		return nil
	}
	sess, ok := r.sessions[id]
	if !ok || sess == nil || sess.isDead() {
		return nil
	}
	return sess
}

// lookupByAgentSessionLocked 按 (userID, projectID, templateID, agentSessionID)
// 查找存活会话；调用方必须持有 r.mu。
func (r *TerminalSessionRegistry) lookupByAgentSessionLocked(key TerminalSessionKey) *ManagedTerminalSession {
	for _, sess := range r.sessions {
		if sess == nil || sess.isDead() {
			continue
		}
		if sess.key.UserID == key.UserID &&
			sess.key.ProjectID == key.ProjectID &&
			sess.key.TemplateID == key.TemplateID &&
			sess.key.AgentSessionID == key.AgentSessionID {
			return sess
		}
	}
	return nil
}

// lookupByAgentSession 按 (userID, projectID, templateID, agentSessionID) 在内存里找存活会话。
func (r *TerminalSessionRegistry) lookupByAgentSession(key TerminalSessionKey) *ManagedTerminalSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lookupByAgentSessionLocked(key)
}

// MarkPersistent 把会话标记为持久，并同步到持久层。
//
// 只会 false → true：已经持久的会话不会被改回一次性。
func (r *TerminalSessionRegistry) MarkPersistent(ctx context.Context, key TerminalSessionKey) {
	sess := r.Lookup(key)
	if sess == nil {
		return
	}
	if !sess.markPersistent() {
		return
	}
	if r.store == nil {
		return
	}
	if err := r.store.MarkPersistent(ctx, sess.Key()); err != nil {
		logger.Warnf(ctx, "[TerminalSession] mark session %s persistent failed: %v", sess.ID(), err)
	}
}

// terminalOptions 按指定尺寸组装交互式终端参数。
func terminalOptions(size terminalSize) containerruntime.TerminalOptions {
	size = size.normalize()
	return containerruntime.TerminalOptions{
		Command: []string{"/bin/bash", "-l"},
		TTY:     true,
		Rows:    size.rows,
		Cols:    size.cols,
	}
}

// createOrAdopt 先尝试认领数据库里记录的旧容器，失败再新建。
//
// “认领”的含义：进程重启后容器还在，但原来的 shell 已随连接结束；这里在同一个
// 容器上重新 exec 一个 bash，并把落盘的滚动缓冲回放进内存缓冲。
func (r *TerminalSessionRegistry) createOrAdopt(key TerminalSessionKey, persistent bool, size terminalSize) (*ManagedTerminalSession, bool, error) {
	if r.probe == nil {
		return nil, false, fmt.Errorf("container probe is not configured")
	}

	// 调用方没给尺寸（例如 Agent 的 terminal_exec）时用默认 24x80；
	// 前端接入后由 Resize 把 PTY 调整成 xterm 的尺寸。
	resolved := size.normalize()

	createCtx, cancel := context.WithTimeout(context.Background(), r.createWait())
	defer cancel()

	adopted, err := r.adoptSession(createCtx, key, resolved)
	if err == nil && adopted != nil {
		if persistent {
			adopted.setPersistent(true)
			r.persistRecord(createCtx, adopted, true)
		}
		return adopted, false, nil
	}
	if err != nil {
		logger.Warnf(createCtx, "[TerminalSession] adopt session %s failed, will recreate: %v", key.SessionKey(), err)
	}

	if err := r.checkQuota(createCtx, key.UserID); err != nil {
		return nil, false, err
	}

	handle, err := r.probe.StartTerminalRuntime(createCtx, key.UserID, key.ProjectID, key.TemplateID, terminalOptions(resolved))
	if err != nil {
		return nil, false, err
	}
	managed, err := r.initSession(createCtx, key, handle, nil, resolved)
	if err != nil {
		handle.Cleanup()
		return nil, false, err
	}
	// 新会话默认是一次性（Agent 创建的）；UI / Agent 显式要求持久时在这里直接落库为持久。
	// 先落库拿到主键，再让注册表把会话挂进索引（保证 createOrAdopt 返回时 ID 已就绪）。
	managed.setPersistent(persistent)
	r.persistRecord(createCtx, managed, persistent)
	return managed, true, nil
}

// adoptSession 按终端会话主键认领数据库记录指向的原容器；没有记录 / 容器已不可用时返回 (nil, nil)。
func (r *TerminalSessionRegistry) adoptSession(ctx context.Context, key TerminalSessionKey, size terminalSize) (*ManagedTerminalSession, error) {
	// 只有已定位到主键的会话才可能被认领：UI 的「新建」永远是新会话，
	// Agent 的身份在 ensure 里已经解析成了主键（见 resolveRecordKey）。
	if r.store == nil || key.ID <= 0 {
		return nil, nil
	}
	rec, err := r.store.GetByID(ctx, key.ID)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.Status != types.TerminalSessionStatusRunning || strings.TrimSpace(rec.RuntimeID) == "" {
		return nil, nil
	}
	// 超出空闲 TTL 的残留记录：直接清掉，交给新建路径。
	if ttl := r.sessionTTL(); ttl > 0 && time.Since(rec.LastActiveAt) > ttl {
		r.dropRecord(ctx, rec)
		return nil, nil
	}

	recKey := key.ReplaceID(rec.ID)
	handle, err := r.probe.AttachTerminalRuntime(ctx, rec.RuntimeName, rec.RuntimeID, terminalOptions(size))
	if err != nil {
		// 容器已被删除 / 节点重建：记录作废，走新建。
		r.dropRecord(ctx, rec)
		return nil, err
	}

	managed, err := r.initSession(ctx, recKey, handle, r.loadScrollback(recKey), size)
	if err != nil {
		handle.Cleanup()
		return nil, err
	}
	managed.setName(rec.Name)
	logger.Infof(ctx, "[TerminalSession] adopted session %s runtime=%s persistent=%v", managed.ID(), rec.RuntimeID, rec.Persistent)
	r.touchRecord(ctx, recKey)
	managed.setPersistent(rec.Persistent)
	return managed, nil
}

// initSession 把「新容器 / 认领容器」统一初始化为托管会话。
//
// 新建容器时 key.ID 为 0：先用进程内生成的占位 ID 建立会话（保证有稳定的
// sessions 索引与滚动缓冲文件名），等落库（persistRecord）拿到主键后
// 再 ReplaceID 换成真实 ID。
func (r *TerminalSessionRegistry) initSession(ctx context.Context, key TerminalSessionKey, handle *ProbeTerminalHandle, replay []byte, size terminalSize) (*ManagedTerminalSession, error) {
	if handle == nil || handle.Session == nil {
		return nil, fmt.Errorf("terminal runtime handle is empty")
	}
	if key.ID <= 0 {
		key = key.ReplaceID(newTerminalSessionPlaceholderID())
	}
	managed := &ManagedTerminalSession{
		id:          key.SessionKey(),
		key:         key,
		sess:        handle.Session,
		cleanup:     handle.Cleanup,
		registry:    r,
		store:       r.store,
		scrollback:  r.openScrollback(key),
		runtimeID:   handle.RuntimeID,
		runtimeName: handle.RuntimeName,
		subs:        make(map[int64]*TerminalSubscriber),
		bufLimit:    r.bufferBytes(),
		readyCh:     make(chan struct{}),
		done:        make(chan struct{}),
		markerCh:    make(chan int, 8),
		lastOutput:  time.Now(),
		lastActive:  time.Now(),
		createdAt:   time.Now(),
		rows:        size.rows,
		cols:        size.cols,
	}
	if name := strings.TrimSpace(key.Name); name != "" {
		managed.name = name
	}
	if len(replay) > 0 {
		// 重启前缓存的历史输出：作为初始缓冲，前端 attach 时直接回放。
		if managed.bufLimit > 0 && len(replay) > managed.bufLimit {
			replay = replay[len(replay)-managed.bufLimit:]
		}
		managed.buf = replay
		managed.bufStart = 0
	}
	managed.start()

	// 注入提示符标记；命令本身会被 shell 回显一次，因此只写一行。
	// 用 LF 提交（canonical TTY 下 LF 恒为行分隔符，readline 下 C-j 为 accept-line）。
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

// persistRecord 把 会话 → runtimeID 的映射落库，供进程重启后认领。
//
// 新建会话时 key.ID 为 0，Upsert 后回填主键：随后把内存会话的 key 换成真实 ID，
// 并把滚动缓冲文件从 "new" 占位名迁移到 ID 名（超时 / 重启后仍能回放）。
func (r *TerminalSessionRegistry) persistRecord(ctx context.Context, sess *ManagedTerminalSession, persistent bool) {
	if r.store == nil || sess == nil {
		return
	}
	key := sess.Key()
	rec := &types.TerminalSession{
		// 注意只认「真实主键」：新会话在落库前用的是负数占位 ID（见
		// newTerminalSessionPlaceholderID），绝不能写进记录，否则主键会变成 -1。
		SessionKey:     key.SessionKey(),
		Name:           sess.displayName(),
		AgentSessionID: key.AgentSessionID,
		UserID:         key.UserID,
		ProjectID:      key.ProjectID,
		TemplateID:     key.TemplateID,
		RuntimeName:    sess.runtimeName,
		RuntimeID:      sess.runtimeID,
		ContainerName:  sess.runtimeID,
		Status:         types.TerminalSessionStatusRunning,
		Persistent:     persistent,
		LastActiveAt:   time.Now(),
	}
	// 已认领的会话必须保留原主键，否则 Upsert 会当成新记录插入并把 session_id 换掉。
	// 只有真实主键（> 0）才写入；新会话此时还是负数占位 ID，交给数据库分配。
	if id := sess.recordID(); id > 0 {
		rec.ID = id
	}
	if err := r.store.Upsert(ctx, rec); err != nil {
		logger.Warnf(ctx, "[TerminalSession] persist session %s failed: %v", key.SessionKey(), err)
		return
	}
	if rec.ID <= 0 {
		return
	}
	// 把（可能还是占位 / 未分配）的 ID 换成数据库主键。
	sess.assignRecordID(rec.ID)
	if rec.SessionKey == sess.Key().SessionKey() {
		return
	}
	// 唯一键从「占位形态」变成了 ID 形态：把旧记录删掉，再写一条 ID 形态的记录。
	_ = r.store.DeleteByID(ctx, rec.ID)
	renamed := *rec
	renamed.SessionKey = sess.Key().SessionKey()
	if err := r.store.Upsert(ctx, &renamed); err != nil {
		logger.Warnf(ctx, "[TerminalSession] persist renamed session %s failed: %v", renamed.SessionKey, err)
	}
}

// persistName 把会话展示名写回持久层（UI 创建时由用户 / 模板提供）。
func (r *TerminalSessionRegistry) persistName(ctx context.Context, sess *ManagedTerminalSession) {
	if r.store == nil || sess == nil {
		return
	}
	rec, err := r.store.GetByID(ctx, sess.recordID())
	if err != nil || rec == nil {
		return
	}
	rec.Name = sess.displayName()
	if err := r.store.Upsert(ctx, rec); err != nil {
		logger.Warnf(ctx, "[TerminalSession] persist session name %s failed: %v", sess.ID(), err)
	}
}

func (r *TerminalSessionRegistry) touchRecord(ctx context.Context, key TerminalSessionKey) {
	if r.store == nil {
		return
	}
	if err := r.store.Touch(ctx, key, time.Now()); err != nil {
		logger.Warnf(ctx, "[TerminalSession] touch session %s failed: %v", key.SessionKey(), err)
	}
}

// dropRecord 删除数据库记录并尽力删除其容器（处理跨进程残留）。
func (r *TerminalSessionRegistry) dropRecord(ctx context.Context, rec *types.TerminalSession) {
	if rec == nil {
		return
	}
	if r.probe != nil {
		r.probe.DropTerminalRuntime(ctx, rec.RuntimeName, rec.RuntimeID)
	}
	// 会话已不存在，滚动缓冲文件也一并清理，避免目录无限增长。
	if scrollback, err := newTerminalScrollback(r.opts.ScrollbackDir, rec.SessionKey, r.scrollbackBytes()); err == nil {
		scrollback.Remove()
	}
	if r.store != nil {
		if err := r.store.DeleteByID(ctx, rec.ID); err != nil {
			logger.Warnf(ctx, "[TerminalSession] delete session record %s failed: %v", rec.SessionKey, err)
		}
	}
}

// checkQuota 限制单个用户同时存在的终端会话数。
func (r *TerminalSessionRegistry) checkQuota(ctx context.Context, userID int64) error {
	limit := r.maxSessionsPerUser()
	if r.store == nil || limit <= 0 || userID <= 0 {
		return nil
	}
	rows, err := r.store.List(ctx)
	if err != nil {
		// 查询失败不阻塞使用，只记录。
		logger.Warnf(ctx, "[TerminalSession] list sessions for quota failed: %v", err)
		return nil
	}
	now := time.Now()
	active := 0
	for _, rec := range rows {
		if rec.UserID != userID || rec.Status != types.TerminalSessionStatusRunning {
			continue
		}
		if r.sessionTTL() > 0 && now.Sub(rec.LastActiveAt) > r.sessionTTL() {
			continue
		}
		active++
	}
	if active >= limit {
		return fmt.Errorf("%w (user %d already has %d terminal sessions, limit %d)", ErrTerminalSessionQuotaExceeded, userID, active, limit)
	}
	return nil
}

func (r *TerminalSessionRegistry) openScrollback(key TerminalSessionKey) *terminalScrollback {
	if strings.TrimSpace(r.opts.ScrollbackDir) == "" {
		return nil
	}
	scrollback, err := newTerminalScrollback(r.opts.ScrollbackDir, key.SessionKey(), r.scrollbackBytes())
	if err != nil {
		logger.Warnf(context.Background(), "[TerminalSession] open scrollback for %s failed: %v", key.SessionKey(), err)
		return nil
	}
	return scrollback
}

func (r *TerminalSessionRegistry) loadScrollback(key TerminalSessionKey) []byte {
	scrollback, err := newTerminalScrollback(r.opts.ScrollbackDir, key.SessionKey(), r.scrollbackBytes())
	if err != nil {
		return nil
	}
	data := scrollback.Load()
	if len(data) == 0 {
		return nil
	}
	// 去掉旧的 OSC 标记，避免回放里混入控制字符。
	return stripTerminalMarkers(data)
}

// StartMaintenance 启动后台维护：空闲会话回收 + 跨进程残留对账。
// 传入的 ctx 取消时退出。
func (r *TerminalSessionRegistry) StartMaintenance(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(terminalMaintenanceInterval)
		defer ticker.Stop()
		r.maintain(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.maintain(ctx)
			}
		}
	}()
}

func (r *TerminalSessionRegistry) maintain(ctx context.Context) {
	now := time.Now()
	ttl := r.sessionTTL()

	r.mu.Lock()
	live := make([]*ManagedTerminalSession, 0, len(r.sessions))
	for _, sess := range r.sessions {
		live = append(live, sess)
	}
	r.mu.Unlock()

	for _, sess := range live {
		if sess.isBusy() || sess.hasSubscribers() {
			// 长任务执行中 / 仍有人连接：只刷新活跃时间，不做 TTL 回收。
			r.touchRecord(ctx, sess.key)
			continue
		}
		// 一次性会话在最后一个订阅者离开时已被 scheduleReap 回收；
		// 这里主要兜底持久会话（以及极端情况下漏掉的一次性会话）。
		if ttl > 0 && sess.idleFor(now) > ttl {
			logger.Infof(ctx, "[TerminalSession] session %s idle for %s, releasing", sess.id, sess.idleFor(now).Truncate(time.Second))
			r.forget(sess)
			sess.Close()
			r.notifySessionChanged(sess, "sessionDeleted")
			continue
		}
		r.touchRecord(ctx, sess.key)
	}

	if r.store == nil || ttl <= 0 {
		return
	}
	rows, err := r.store.List(ctx)
	if err != nil {
		logger.Warnf(ctx, "[TerminalSession] maintenance list failed: %v", err)
		return
	}
	for _, rec := range rows {
		if now.Sub(rec.LastActiveAt) <= ttl {
			continue
		}
		if r.LookupByID(rec.ID) != nil {
			continue
		}
		logger.Infof(ctx, "[TerminalSession] dropping stale session %d (idle %s)", rec.ID, now.Sub(rec.LastActiveAt).Truncate(time.Second))
		r.dropRecord(ctx, rec)
		r.notifySessionEvent(rec.UserID, rec.TemplateID, strconv.FormatInt(rec.ID, 10), "sessionDeleted")
	}
}

// Lookup 返回存活会话（不创建）。
//
// 定位顺序：
//   - key.ID != 0：按终端会话主键查找；
//   - key.AgentSessionID > 0：按 (userID, projectID, templateID, agentSessionID) 查找
//     （Agent 侧只关心「这个 Agent 会话在该模板上的终端」，不关心会话 ID）；
//   - 否则无法定位：会话以主键为唯一标识。
func (r *TerminalSessionRegistry) Lookup(key TerminalSessionKey) *ManagedTerminalSession {
	if key.ID != 0 {
		return r.LookupByID(key.ID)
	}
	if key.AgentSessionID > 0 {
		return r.lookupByAgentSession(key)
	}
	return nil
}

// LookupByID 按终端会话主键返回存活会话（不创建）。
//
// 新会话在落库前使用负数占位 ID，因此这里只拒绝 0。
func (r *TerminalSessionRegistry) LookupByID(id int64) *ManagedTerminalSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lookupByIDLocked(id)
}

// Status 返回会话状态；不存在时 Exists=false。
func (r *TerminalSessionRegistry) Status(key TerminalSessionKey) TerminalSessionStatus {
	sess := r.Lookup(key)
	if sess == nil {
		return TerminalSessionStatus{SessionID: sessionIDString(key.ID)}
	}
	subs, seq, alive := sess.snapshotStatus()
	return TerminalSessionStatus{
		SessionID:      sess.ID(),
		Exists:         true,
		Alive:          alive,
		Subscribers:    subs,
		Seq:            seq,
		UserID:         sess.key.UserID,
		ProjectID:      sess.key.ProjectID,
		TemplateID:     sess.key.TemplateID,
		AgentSessionID: sess.key.AgentSessionID,
		Name:           sess.displayName(),
		Persistent:     sess.isPersistent(),
		Busy:           sess.isBusy(),
		RuntimeID:      sess.runtimeID,
		RuntimeName:    sess.runtimeName,
		CreatedAt:      sess.createdAt,
		LastActive:     sess.lastActiveTime(),
	}
}

// ListSessions 返回当前用户的全部终端会话状态（按模板 / 会话 ID 排序，保证顺序稳定）。
//
// 数据源是数据库（go_terminal_session）：会话是持久化实体，进程重启后列表仍应完整。
// 数据库里有的记录，如果内存注册表里已有对应会话（Alive=true），用内存状态回填
// 订阅者数 / 序号 / busy 等实时字段；没有则在原记录上补充 Exists/Alive=false，
// 前端打开它会触发 ContainerTemplateTerminal 自动认领原容器并重建 shell。
func (r *TerminalSessionRegistry) ListSessions(userID int64) []TerminalSessionStatus {
	if r.store == nil || userID <= 0 {
		return nil
	}
	records, err := r.store.ListByUser(context.Background(), userID)
	if err != nil {
		logger.Warnf(context.Background(), "[TerminalSession] list sessions for user %d failed: %v", userID, err)
		return nil
	}

	out := make([]TerminalSessionStatus, 0, len(records))
	for _, rec := range records {
		if rec == nil {
			continue
		}
		status := TerminalSessionStatus{
			SessionID:      sessionIDString(rec.ID),
			Exists:         true,
			UserID:         rec.UserID,
			ProjectID:      rec.ProjectID,
			TemplateID:     rec.TemplateID,
			AgentSessionID: rec.AgentSessionID,
			Name:           strings.TrimSpace(rec.Name),
			Persistent:     rec.Persistent,
			RuntimeID:      rec.RuntimeID,
			RuntimeName:    rec.RuntimeName,
			CreatedAt:      rec.CreatedAt,
			LastActive:     rec.LastActiveAt,
		}
		// 内存里有会话（本进程已 attach）：用实时状态覆盖。
		if sess := r.LookupByID(rec.ID); sess != nil {
			subs, seq, alive := sess.snapshotStatus()
			status.Alive = alive
			status.Subscribers = subs
			status.Seq = seq
			status.Busy = sess.isBusy()
			status.Persistent = sess.isPersistent()
			status.Name = sess.displayName()
			status.LastActive = sess.lastActiveTime()
		}
		out = append(out, status)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].TemplateID != out[j].TemplateID {
			return out[i].TemplateID < out[j].TemplateID
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

// SessionRecord 按终端会话主键返回持久层记录（不存在时返回 nil）。
//
// 供 handler 在内存注册表里找不到会话时做归属校验 / 判断会话是否仍然存在。
func (r *TerminalSessionRegistry) SessionRecord(ctx context.Context, id int64) (*types.TerminalSession, error) {
	if r.store == nil || id <= 0 {
		return nil, nil
	}
	rec, err := r.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.Status != types.TerminalSessionStatusRunning {
		return nil, nil
	}
	return rec, nil
}

// ReviveByID 按终端会话主键「重建」一个本进程里还不存在的会话。
// 典型场景：进程重启后数据库里仍有记录、容器也可能还在，但 shell 随进程结束。
// 这里走与创建相同的 ensure 路径：能认领原容器就重新 exec 一个 shell（adoptSession），
// 容器已不存在则新建；历史输出由落盘的滚动缓冲回放。
//
// rows/cols 是前端 xterm 当前尺寸：会话已存活时直接 Resize 到该尺寸；
// 需要重新 exec shell 时用它建 PTY。传 0 时保持原尺寸 / 使用默认 24x80。
//
// 返回 ErrTerminalSessionNotFound 表示记录不存在（或已关闭），由调用方报 404。
// id 只拒绝 0（未分配）：落库前的新会话使用负数占位 ID，它们同样可以被定位。
func (r *TerminalSessionRegistry) ReviveByID(ctx context.Context, id int64, rows, cols uint16) (*ManagedTerminalSession, error) {
	if id == 0 {
		return nil, ErrTerminalSessionNotFound
	}
	if sess := r.LookupByID(id); sess != nil {
		// 已存活的会话：接入时按前端上报的尺寸调整 PTY。
		resizeTerminalSession(sess, terminalSize{rows: rows, cols: cols})
		return sess, nil
	}
	if r.store == nil {
		return nil, ErrTerminalSessionNotFound
	}
	rec, err := r.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.Status != types.TerminalSessionStatusRunning {
		return nil, ErrTerminalSessionNotFound
	}

	key := TerminalSessionKey{
		UserID:         rec.UserID,
		ProjectID:      rec.ProjectID,
		TemplateID:     rec.TemplateID,
		AgentSessionID: rec.AgentSessionID,
		ID:             rec.ID,
		Name:           rec.Name,
	}
	size := terminalSize{rows: rows, cols: cols}
	// 用户正在主动打开它：一律按持久会话重建，避免一次性规则把容器回收掉。
	sess, created, err := r.ensure(ctx, key, true, size)
	if err != nil {
		return nil, err
	}
	if sess == nil || sess.isDead() {
		return nil, ErrTerminalSessionNotFound
	}
	// ensure 复用了本进程里的存活会话时，用它把 PTY 调整到前端尺寸。
	resizeTerminalSession(sess, size)
	logger.Infof(ctx, "[TerminalSession] revived session %d created=%v runtime=%s", rec.ID, created, rec.RuntimeID)
	return sess, nil
}

// resizeTerminalSession 按前端 xterm 尺寸调整会话 PTY；rows/cols 为 0 时不做任何事。
//
// 前端把 PTY 宽度当权威尺寸：接入时先按上报尺寸对齐 PTY，再回放历史输出，
// 否则 bash 会在回放期间按旧宽度重绘提示符，覆盖已经输出的行。
func resizeTerminalSession(sess *ManagedTerminalSession, size terminalSize) {
	if sess == nil || !size.valid() {
		return
	}
	if err := sess.Resize(size.rows, size.cols); err != nil {
		logger.Warnf(context.Background(), "[TerminalSession] resize session %s to %dx%d failed: %v", sess.ID(), size.rows, size.cols, err)
	}
}

// CloseSession 主动结束会话（删除容器），并删除持久层记录。
//
// 只按终端会话主键寻址：key.ID 必须 > 0。
func (r *TerminalSessionRegistry) CloseSession(key TerminalSessionKey) bool {
	// 只有带主键的会话能被关闭（UI 「关闭」与 Agent 的会话关闭都先解析出主键）。
	if key.ID == 0 {
		return false
	}
	r.mu.Lock()
	sess := r.sessions[key.ID]
	delete(r.sessions, key.ID)
	r.mu.Unlock()

	// 记录里的 runtime 信息用于兜底删除容器：即便内存里没有会话（例如进程重启后
	// 还残留记录），也应当把容器删掉，避免「列表点了删除但容器还在」。
	if r.store != nil && key.ID > 0 {
		if rec, err := r.store.GetByID(context.Background(), key.ID); err == nil && rec != nil {
			r.dropRecord(context.Background(), rec)
		}
	}

	if sess != nil {
		sess.Close()
	}
	// 广播删除事件：前端「终端会话列表」不再轮询，完全依赖该事件刷新，
	// 因此即使内存里没有会话（记录 / 容器残留）也必须推一次。
	userID, templateID := key.UserID, key.TemplateID
	if sess != nil {
		userID, templateID = sess.key.UserID, sess.key.TemplateID
	}
	r.notifySessionEvent(userID, templateID, sessionIDString(key.ID), "sessionDeleted")

	return sess != nil
}

// Execute 把命令写入会话的串行队列。
//
// 定位规则（见 ensure）：
//   - key.ID > 0：直接投递到该终端会话；
//   - 否则按 (templateID, agentSessionID) 查找 Agent 会话的终端（agentSessionID=0 时新建）；
//   - 会话不存在且 cmd.CreateIfMissing 时按 cmd.Persistent 创建。
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
		// 新建会话时按 cmd.Persistent 决定生命周期：一次性会话在本命令结束
		// 且没有订阅者时会被回收，命令输出随之丢失。
		created, _, err := r.ensure(ctx, key, cmd.Persistent, terminalSize{})
		if err != nil {
			return TerminalCommandResult{}, err
		}
		sess = created
	} else if cmd.Persistent {
		// 复用已有会话：持久标记只增不减，这里显式升级以免命令结束就被回收。
		r.MarkPersistent(ctx, sess.Key())
	}
	sess.touch()

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
//
// 会话定位：按 (userID, projectID, templateID, agentSessionID) 查找该 Agent 会话在该模板上的
// 终端会话；不存在时新建（同一 agentSessionID + templateID 唯一），因此同一个 Agent 会话
// 多次调用 terminal_exec 会复用同一个容器与 PTY。
//
// 语义：
//   - 创建新会话时，按 in.Persistent 决定它是一次性还是持久会话；
//   - 命令结束（含超时 / 失败）后总是 Detach：一次性会话因此立即回收容器，
//     持久会话则保留，等待空闲 TTL 或用户显式关闭。
func (r *TerminalSessionRegistry) RunInTerminal(ctx context.Context, in runtimecontext.ContainerTerminalExecInput) (runtimecontext.ContainerTerminalExecResult, error) {
	if in.AgentSessionID <= 0 {
		return runtimecontext.ContainerTerminalExecResult{}, fmt.Errorf("terminal_exec: agent session id is required")
	}
	key := NewAgentKey(in.UserID, in.ProjectID, in.ContainerTemplateID, in.AgentSessionID)

	// 先占位：保证「一次性会话 + 无 WS」时命令执行期间不会被回收。
	sess := r.Lookup(key)
	if sess == nil && in.CreateIfMissing {
		created, _, err := r.Ensure(ctx, key)
		if err != nil {
			return runtimecontext.ContainerTerminalExecResult{}, err
		}
		sess = created
	}
	if sess == nil {
		return runtimecontext.ContainerTerminalExecResult{}, ErrTerminalSessionNotFound
	}

	if in.Persistent {
		if sess.markPersistent() && r.store != nil {
			if err := r.store.MarkPersistent(ctx, sess.Key()); err != nil {
				logger.Warnf(ctx, "[TerminalSession] mark session %s persistent failed: %v", sess.ID(), err)
			}
		}
	}

	detach := sess.AttachObserver()
	defer detach()

	// 持久会话会通过 realtime 推送 terminal.exec 事件，让前端自动打开终端标签。
	result, err := r.Execute(ctx, sess.Key(), TerminalCommand{
		Command:         in.Command,
		Wait:            in.Wait,
		Timeout:         time.Duration(in.TimeoutSeconds) * time.Second,
		Source:          "agent",
		NotifyUI:        in.Persistent,
		CreateIfMissing: in.CreateIfMissing,
		// 会话可能在本函数 Ensure 之后被回收，交给 Execute 兜底按同一规则创建。
		Persistent: in.Persistent,
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

// notifySessionChanged 推送「终端会话列表」需要刷新的实时事件。
//
// 与 AppSessionEventHandler 的推送方式一致：action=component.invoke，前端
// ActionDispatcher 收到后调用 useComponentStore().invoke(category, "*", method)，
// 左侧栏「终端会话」面板注册的 sessionCreated / sessionDeleted 会触发 refetch。
func (r *TerminalSessionRegistry) notifySessionChanged(sess *ManagedTerminalSession, method string) {
	if sess == nil {
		return
	}
	r.notifySessionEvent(sess.key.UserID, sess.key.TemplateID, sess.ID(), method)
}

// notifySessionState 推送「会话运行 / 订阅」状态变化，让列表里的
// Running（busy）/ Attached（subscribers）标签即时刷新。
//
// method 取值：sessionRunning（命令开始）、sessionIdle（命令结束）、
// sessionAttached（WS 首次接入）、sessionDetached（最后一个 WS 断开）。
// 只应在状态真正发生跳变时调用，否则每个 terminal_exec 都会刷一次列表。
func (r *TerminalSessionRegistry) notifySessionState(sess *ManagedTerminalSession, method string) {
	if r.publisher == nil || sess == nil {
		return
	}
	sess.mu.Lock()
	busy := sess.busy
	subs := len(sess.subs)
	persistent := sess.persistent
	closed := sess.closed || sess.dead
	sess.mu.Unlock()
	if closed {
		// 会话正在关闭：sessionDeleted 紧随其后，没必要再推一条状态事件。
		return
	}
	r.pushSessionEvent(sess.key.UserID, sess.key.TemplateID, sess.ID(), method, map[string]any{
		"busy":        busy,
		"subscribers": subs,
		"persistent":  persistent,
	})
}

// notifySessionEvent 推送会话变更事件（method 为 sessionCreated / sessionDeleted）。
func (r *TerminalSessionRegistry) notifySessionEvent(userID, templateID int64, sessionID, method string) {
	r.pushSessionEvent(userID, templateID, sessionID, method, nil)
}

// pushSessionEvent 把会话事件封装成 component.invoke 推给前端；extra 会并入 args。
func (r *TerminalSessionRegistry) pushSessionEvent(userID, templateID int64, sessionID, method string, extra map[string]any) {
	if r.publisher == nil || userID == 0 {
		return
	}
	eventMethod := strings.TrimSpace(method)
	if eventMethod == "" {
		eventMethod = "sessionDeleted"
	}
	payload := map[string]any{
		"session_id":  sessionID,
		"template_id": strconv.FormatInt(templateID, 10),
		"event":       eventMethod,
	}
	for key, value := range extra {
		payload[key] = value
	}
	msg := map[string]any{
		"action": "component.invoke",
		"payload": map[string]any{
			"category": "container-terminal-session",
			"id":       "*",
			"method":   eventMethod,
			"args":     payload,
		},
	}
	if err := r.publisher.PushMessage(userID, msg); err != nil {
		if !strings.Contains(err.Error(), "no_client_for_user:") {
			logger.Warnf(context.Background(), "[TerminalSession] push session event failed user=%d session=%s method=%s err=%v", userID, sessionID, eventMethod, err)
		}
	}
}

// forget 从注册表中移除会话（仅当指针一致）。
func (r *TerminalSessionRegistry) forget(sess *ManagedTerminalSession) {
	if sess == nil {
		return
	}
	recordID := sess.recordID()
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.sessions[recordID]; ok && current == sess {
		delete(r.sessions, recordID)
	}
}

// scheduleReap 在「最后一个订阅者离开」后决定是否立即回收会话。
//
// 规则：
//   - 仍有订阅者 / 已关闭 / 有命令在执行 → 不回收；
//   - 持久会话（前端接入过，或 Agent 显式要求持久）→ 不回收，
//     只能等空闲 TTL 到期（maintain）或显式 CloseSession；
//   - 一次性会话（Agent 创建且从未被前端接入）→ 立即回收容器与记录。
func (r *TerminalSessionRegistry) scheduleReap(sess *ManagedTerminalSession) {
	if sess == nil || !r.shouldReap(sess) {
		return
	}

	logger.Infof(context.Background(), "[TerminalSession] releasing one-shot session %s", sess.ID())
	r.forget(sess)
	sess.Close()
	r.notifySessionChanged(sess, "sessionDeleted")
}

// shouldReap 判断会话此刻是否应该被立即回收（scheduleReap 的唯一判据）。
//
// 规则：
//   - 已关闭 / 已断开 / 仍有订阅者 / 有命令在执行 → 不回收；
//   - 持久会话 → 不回收，交给空闲 TTL 或显式 CloseSession；
//   - 一次性会话 → 回收。
func (r *TerminalSessionRegistry) shouldReap(sess *ManagedTerminalSession) bool {
	if sess == nil {
		return false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.closed || sess.dead || len(sess.subs) > 0 || sess.busy {
		return false
	}
	return !sess.persistent
}

// suspendReap 已不再需要（回收改为同步判定），保留空实现以免破坏调用点。
//
// 命令执行期间的防回收由 scheduleReap 里的 busy 判断负责。
func (s *ManagedTerminalSession) suspendReap() {
	s.setBusy(true)
}

// ===== ManagedTerminalSession =====

func (s *ManagedTerminalSession) start() {
	s.requests = make(chan terminalExecRequest, s.registry.execQueueSize())
	go s.readLoop()
	go s.execLoop()
	if s.scrollback != nil {
		go s.flushLoop()
	}
}

// ID 返回会话 ID（终端会话主键的字符串形式）。
func (s *ManagedTerminalSession) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sessionIDString(s.key.ID)
}

// recordID 返回终端会话主键（未落库时也是 0，此时用 SessionKey 定位）。
func (s *ManagedTerminalSession) recordID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.key.ID
}

// displayName 返回会话展示名；未设置时回退为 ID。
func (s *ManagedTerminalSession) displayName() string {
	s.mu.Lock()
	name := strings.TrimSpace(s.name)
	id := s.key.ID
	s.mu.Unlock()
	if name != "" {
		return name
	}
	return sessionIDString(id)
}

// setName 设置会话展示名；返回是否发生变化。
func (s *ManagedTerminalSession) setName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.name == name {
		return false
	}
	s.name = name
	return true
}

// assignRecordID 在会话落库后把（占位）ID 换成真实主键。
//
// 只更新会话自身（key / id）；注册表索引由调用方在加锁后维护。
func (s *ManagedTerminalSession) assignRecordID(id int64) {
	if id <= 0 {
		return
	}
	s.mu.Lock()
	if s.key.ID == id {
		s.mu.Unlock()
		return
	}
	s.key = s.key.ReplaceID(id)
	s.id = s.key.SessionKey()
	s.mu.Unlock()
}

// sessionIDString 把终端会话主键转成对外字符串；0 表示尚未分配（返回空串）。
func sessionIDString(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// terminalSessionPlaceholderSeq 为新会话生成进程内占位 ID（负数，永远不与数据库主键冲突）。
var terminalSessionPlaceholderSeq atomic.Int64

func newTerminalSessionPlaceholderID() int64 {
	return -terminalSessionPlaceholderSeq.Add(1)
}

// Key 返回会话 key。
func (s *ManagedTerminalSession) Key() TerminalSessionKey { return s.key }

// lastActiveTime 返回最近一次操作时间（用于列表展示）。
func (s *ManagedTerminalSession) lastActiveTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastActive
}

// WriteInput 写入用户按键（终端原始输入）。
func (s *ManagedTerminalSession) WriteInput(data []byte) (int, error) {
	if s.isDead() {
		return 0, ErrTerminalSessionClosed
	}
	s.touch()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.sess.Write(data)
}

// Resize 调整 PTY 尺寸（前端 xterm 是尺寸的唯一来源）。
func (s *ManagedTerminalSession) Resize(rows, cols uint16) error {
	if s.isDead() {
		return ErrTerminalSessionClosed
	}
	if rows == 0 || cols == 0 {
		return nil
	}
	s.mu.Lock()
	s.rows = rows
	s.cols = cols
	s.mu.Unlock()
	return s.sess.Resize(rows, cols)
}

// Size 返回 PTY 当前尺寸（前端据此渲染回放，保证宽度一致）。
func (s *ManagedTerminalSession) Size() (rows, cols uint16) {
	s.mu.Lock()
	size := terminalSize{rows: s.rows, cols: s.cols}.normalize()
	s.mu.Unlock()
	return size.rows, size.cols
}

// Attach 注册一个订阅者并返回重放数据。
//
// 前端（ContainerTemplateTerminal）接入时调用者还应调用 MarkPersistent，
// 把会话升级为持久会话。需要知道回放序号区间 / 是否需要清屏时用 AttachWithInfo。
//
// sinceSeq <= 0 表示「尽可能早」（整段缓冲）；否则从该序号开始回放。
// 注意 sinceSeq 是会话输出流的全局序号（见 TerminalReplayInfo），不是「本地收到的
// 字节数」；超过当前序号时会被当作失效序号整段重放（Reset）。
// 重放与订阅在同一临界区内完成，不会丢帧。
func (s *ManagedTerminalSession) Attach(sinceSeq int64) (*TerminalSubscriber, []byte) {
	sub, replay := s.AttachWithInfo(sinceSeq)
	return sub, replay.Data
}

// AttachWithInfo 与 Attach 相同，但额外返回本次回放的序号区间与续传状态。
//
// 为什么需要：调用方（前端）把「已消费到哪个序号」传回来才能增量回放，而序号是
// 环形缓冲里的全局偏移（bufStart 可能在缓冲滚动后不为 0）。如果客户端自己按
// 「收到多少字节」计数，一旦缓冲滚动、或会话被重建 / 认领（序号基准归零），
// 续传位置就会错位：轻则重复/跳过一段，重则 since 超出当前序号 → 空回放、历史全丢。
//
// 因此这里把边界情况显式告知调用方：
//   - sinceSeq > To：客户端位置超出本会话（会话重建 / 认领后序号基准改变），
//     回退为整段重放并置 Reset=true，客户端应先清屏；
//   - sinceSeq < bufStart：中间那段已被缓冲覆盖，置 Truncated=true；
//   - From/To 是本次回放覆盖的序号区间，客户端下次应从这里继续。
func (s *ManagedTerminalSession) AttachWithInfo(sinceSeq int64) (*TerminalSubscriber, TerminalReplayInfo) {
	return s.attach(sinceSeq, true)
}

// attach 是 Attach / AttachObserver 的共同实现；withReplay=false 时不复制缓冲。
func (s *ManagedTerminalSession) attach(sinceSeq int64, withReplay bool) (*TerminalSubscriber, TerminalReplayInfo) {
	s.mu.Lock()
	first := len(s.subs) == 0

	s.nextSubID++
	sub := newTerminalSubscriber(s.nextSubID)
	s.subs[sub.ID] = sub
	s.lastActive = time.Now()

	end := s.bufStart + int64(len(s.buf))
	info := TerminalReplayInfo{From: end, To: end}
	if withReplay {
		start := sinceSeq
		switch {
		case sinceSeq <= 0:
			// 首次连接：尽可能早。
			start = s.bufStart
		case sinceSeq > end:
			// 客户端的位置比本会话还大：旧序号已失效，整段重放并让客户端清屏。
			start = s.bufStart
			info.Reset = true
		case sinceSeq < s.bufStart:
			// 缓冲已滚动：请求的位置之前那段没了。
			start = s.bufStart
			info.Truncated = true
		}
		if start < s.bufStart {
			start = s.bufStart
		}
		info.From = start
		if offset := start - s.bufStart; offset < int64(len(s.buf)) {
			info.Data = append([]byte(nil), s.buf[offset:]...)
		}
	}
	s.mu.Unlock()

	// 订阅者 0 → n：列表里的 Attached 标签需要立刻反映出来。
	if first {
		s.registry.notifySessionState(s, "sessionAttached")
	}
	return sub, info
}

// AttachObserver 登记一个「只占位、不接收输出」的订阅者，返回幂等的 Detach 函数。
//
// 供 terminal_exec 这类「我只需要保证执行期间不被回收」的调用使用：
//   - 它与 WS 订阅者共用 subs 计数，因此有前端终端在线时 Detach 不会触发回收；
//   - 它的 Data 是无缓冲 channel 且无人读取，publish 会走 default 分支丢弃，
//     不会像普通订阅者那样占用 2048 块的缓冲内存。
func (s *ManagedTerminalSession) AttachObserver() func() {
	sub, _ := s.attach(0, false)
	s.mu.Lock()
	sub.Data = make(chan []byte) // 无缓冲 + 无人读：publish 必定走 default
	s.mu.Unlock()

	var once sync.Once
	return func() { once.Do(func() { s.Detach(sub) }) }
}

// Detach 注销订阅者；最后一个订阅者离开后按会话类型决定是否立即回收。
func (s *ManagedTerminalSession) Detach(sub *TerminalSubscriber) {
	if sub == nil {
		return
	}
	s.mu.Lock()
	delete(s.subs, sub.ID)
	empty := len(s.subs) == 0
	s.mu.Unlock()

	sub.close()

	if empty {
		// 一次性会话会被 scheduleReap 立即回收并推 sessionDeleted，
		// 此时再补一条 detached 只会多一次无效刷新。
		if !s.registry.shouldReap(s) {
			s.registry.notifySessionState(s, "sessionDetached")
		}
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

// isPersistent 返回会话是否为持久会话。
func (s *ManagedTerminalSession) isPersistent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistent
}

// markPersistent 把会话升级为持久会话；返回是否发生了状态变化（false → true）。
func (s *ManagedTerminalSession) markPersistent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persistent {
		return false
	}
	s.persistent = true
	return true
}

// setPersistent 直接设置持久标记（用于认领进程重启前的记录）。
func (s *ManagedTerminalSession) setPersistent(persistent bool) {
	s.mu.Lock()
	s.persistent = persistent
	s.mu.Unlock()
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
		// 会话结束：删除持久记录与滚动缓冲文件，避免重启后被认领。
		if s.store != nil {
			_ = s.store.Delete(context.Background(), s.key)
		}
		if s.scrollback != nil {
			s.scrollback.Remove()
		}
	})
}

// touch 记录一次「用户 / Agent 操作」，用于空闲 TTL 回收。
func (s *ManagedTerminalSession) touch() {
	s.mu.Lock()
	s.lastActive = time.Now()
	s.mu.Unlock()
}

// idleFor 返回距离最近一次操作（含输出）的时长。
func (s *ManagedTerminalSession) idleFor(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := s.lastActive
	if s.lastOutput.After(last) {
		last = s.lastOutput
	}
	return now.Sub(last)
}

func (s *ManagedTerminalSession) setBusy(busy bool) {
	s.mu.Lock()
	changed := s.busy != busy
	s.busy = busy
	if busy {
		s.lastActive = time.Now()
	}
	s.mu.Unlock()

	if !changed {
		return
	}
	if busy {
		s.registry.notifySessionState(s, "sessionRunning")
		return
	}
	// 一次性会话执行完就会被回收（推 sessionDeleted），不必再推 idle。
	if s.registry.shouldReap(s) {
		return
	}
	s.registry.notifySessionState(s, "sessionIdle")
}

func (s *ManagedTerminalSession) isBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

func (s *ManagedTerminalSession) hasSubscribers() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs) > 0
}

// flushLoop 定期把环形缓冲快照落盘（仅脏写）。
func (s *ManagedTerminalSession) flushLoop() {
	ticker := time.NewTicker(terminalScrollbackFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.flushScrollback()
		}
	}
}

func (s *ManagedTerminalSession) flushScrollback() {
	if s.scrollback == nil {
		return
	}
	s.mu.Lock()
	if !s.scrollbackDirty {
		s.mu.Unlock()
		return
	}
	data := append([]byte(nil), s.buf...)
	s.scrollbackDirty = false
	s.mu.Unlock()
	if err := s.scrollback.Save(data); err != nil {
		logger.Warnf(context.Background(), "[TerminalSession] flush scrollback %s failed: %v", s.id, err)
	}
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
	if s.scrollback != nil {
		s.scrollbackDirty = true
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
	// shell 自然退出 / 容器消失：内存会话已移除，推一次删除事件让前端列表刷新
	// （前端不再轮询，只能靠这条事件感知会话已消失）。
	s.registry.notifySessionEvent(s.key.UserID, s.key.TemplateID, s.id, "sessionDeleted")
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
			// 命令结束后重新评估回收：一次性会话若已无订阅者则在此回收。
			s.registry.scheduleReap(s)
		}
	}
}

func (s *ManagedTerminalSession) runCommand(req terminalExecRequest) terminalExecOutcome {
	if s.isClosed() {
		return terminalExecOutcome{err: ErrTerminalSessionClosed}
	}

	// 命令执行期间不允许被「无人订阅」回收（agent 可能在没有前端连接时跑长任务）。
	s.suspendReap()
	defer s.setBusy(false)
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

	// 写入前先发 Ctrl-U（kill line）清掉当前行残留的输入，再用 LF 提交：
	//   - 交互式 shell 里随时可能有一行「用户敲了一半、还没回车」的内容。
	//     直接接上命令会拼成一整行（例如 `a="x"` + `pwd` → 只是个赋值，什么都不执行），
	//     现象就是「命令回显了但没执行，按回车才动 / 干脆没有输出」；
	//   - 提交用 LF(0x0a) 而不是 CR：canonical TTY 下 LF 永远是行分隔符
	//     （CR 只有在 ICRNL 打开时才会被当成换行），readline 下 C-j 同样是 accept-line。
	//     实测某些镜像的 PTY 上只发 CR 会导致命令只回显、永不执行。
	// 整段要放在同一次 Write 里，避免与浏览器按键输入交错。
	payload := []byte("\x15" + strings.TrimRight(req.command, "\r\n") + "\n")
	s.writeMu.Lock()
	n, writeErr := s.sess.Write(payload)
	s.writeMu.Unlock()
	// 排查用：确认命令真的写进了 PTY（\x15/CR 用转义表示，避免日志里出现控制字符）。
	logger.Debugf(context.Background(), "[TerminalSession] exec session=%s source=%s bytes=%d/%d payload=%q",
		s.id, req.source, n, len(payload), string(payload))
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
