package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/agent/history"
	lspmanager "github.com/biox-dev/gobrave/internal/agent/lsp/manager"
	"github.com/biox-dev/gobrave/internal/agent/message"
	"github.com/biox-dev/gobrave/internal/agent/session"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// Provider 是 Agent 的工厂接口：根据 Options 构建一个 Agent 实例。
// 这样同一 Provider 可以有不同的配置（不同模型 / 不同 endpoint）。
type Provider interface {
	// Name 返回 Provider 唯一标识（小写，如 claude_code / codex / copilot / custom）。
	Name() string
	// New 基于 Options 构建 Agent 实例。
	New(opts Options) (Agent, error)
}

// ModelProviderConfig 描述一个「模型提供商」的连接配置（base_url / api_key / 类型等）。
//
// 与 config.ModelProviderConfig 一一对应，由容器启动时从配置映射而来。Provider 在每次
// 调用时按 Profile 指定的模型名（req.ModelID）从此表解析出本次调用所需的 LLM 配置。
//
// Provider 是模型厂商类型（openai / anthropic / deepseek / gemini …），决定 Agent 类库
// 走哪条 SDK 客户端；APIModel 是发给厂商的真实模型名；其余为模型能力元信息。
type ModelProviderConfig struct {
	// ModelID 是模型标识（配置表的 key），与 API 模型名分离。
	ModelID string `json:"model_id"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`

	// Provider 是模型厂商类型（openai / anthropic / deepseek / gemini …）。
	Provider string `json:"provider"`
	// APIModel 是发给厂商的真实模型名；为空时回退 ModelID。
	APIModel string `json:"api_model"`
	// Name 是模型展示名；为空时回退 ModelID。
	Name string `json:"name"`

	// ContextWindow 模型上下文窗口（token）；0 表示未知。
	ContextWindow int64 `json:"context_window"`
	// DefaultMaxTokens 模型默认最大输出 token；0 表示未知。
	DefaultMaxTokens int64 `json:"default_max_tokens"`
	// CanReason 表示模型支持推理 / 思考过程。
	CanReason bool `json:"can_reason"`
	// SupportsAttachments 表示模型支持图片等附件。
	SupportsAttachments bool `json:"supports_attachments"`

	// CostPer1MIn / CostPer1MOut 每百万输入 / 输出 token 价格（美元）。
	CostPer1MIn  float64 `json:"cost_per_1m_in"`
	CostPer1MOut float64 `json:"cost_per_1m_out"`
	// CostPer1MInCached / CostPer1MOutCached 命中缓存的输入 / 输出价格。
	CostPer1MInCached  float64 `json:"cost_per_1m_in_cached"`
	CostPer1MOutCached float64 `json:"cost_per_1m_out_cached"`

	// ReasoningEffort 推理强度（openai 系 low / medium / high），空表示不指定。
	ReasoningEffort string `json:"reasoning_effort"`

	Extra map[string]string `json:"extra"`
}

// Options 是构建 Agent 实例所需的通用配置；具体 Provider 按需读取。
//
// 模型相关的连接配置（base_url / api_key / 类型等）不再作为单值字段存在，而是由
// Providers（模型名 → 配置）承载：一次请求使用的模型由 Profile.Model 决定，Provider
// 据此按模型名解析出本次调用所需的 LLM 配置。
type Options struct {
	WorkingDir string            `json:"working_dir"`
	Extra      map[string]string `json:"extra"`

	// ProviderSource 是模型提供商配置表的来源（可为 nil）。
	//
	// Provider 在每次调用时通过 ResolveProvider 按模型名从这里实时查询；
	// 由调用方（通常是 container 层）按当前生效的配置生成快照，
	// 使可视化配置页保存的 agent 段无需重启即可在下一次请求生效。
	ProviderSource func() map[string]ModelProviderConfig `json:"-"`

	// Tools 是本次调用可用的工具注册表。
	//
	// Provider 据此向模型暴露工具定义（tool.List()），并通过 ToolRunner /
	// tool.Executor 执行模型发起的工具调用。为 nil 表示本次调用不启用工具。
	Tools *tool.Registry `json:"-"`

	// AnalysisNodeRunner 是「运行分析节点」的进程级静态服务能力（可为 nil）。
	//
	// 它不是进程/请求状态，只在构建 Client 时注入一次，由 Provider 在执行工具前
	// 注入工具执行 ctx（见 agent.WithToolRuntimeContext / WithAnalysisNodeRunner），
	// 供 builtin 的 run_analysis_node 工具使用。
	//
	// 注意：该能力不放 RuntimeContext —— RuntimeContext 会随 agent.Request 序列化
	// 入库，而服务对象不可序列化。
	AnalysisNodeRunner tool.AnalysisNodeRunner `json:"-"`

	// AgentAnalysisRunner 是「创建（并按需运行）AgentAnalysis 容器任务」的进程级静态服务能力（可为 nil）。
	//
	// 与 AnalysisNodeRunner 一样，只在构建 Client 时注入一次，由 Provider 在执行工具前
	// 注入工具执行 ctx，供 builtin 的 create_agent_analysis 工具使用。
	AgentAnalysisRunner tool.AgentAnalysisRunner `json:"-"`

	// FileReader 是「按 go_file 主键 ID 读取文件（含表格预览）」的进程级静态服务能力（可为 nil）。
	//
	// 与 AnalysisNodeRunner 一样，它只在构建 Client 时注入一次，由 Provider 在执行工具前
	// 注入工具执行 ctx（见 agent.WithToolRuntime），供 builtin 的 read_file_by_id 工具使用。
	FileReader tool.FileReader `json:"-"`

	// ScriptCreator 是「创建脚本」的进程级静态服务能力（可为 nil）。
	//
	// 与 FileReader 一样，它只在构建 Client 时注入一次，由 Provider 在执行工具前注入
	// 工具执行 ctx（见 agent.WithToolRuntime），供 builtin 的 create_script 工具使用。
	ScriptCreator tool.ScriptCreator `json:"-"`

	// AnalysisNodeCreator 是「为已有脚本创建分析节点」的进程级静态服务能力（可为 nil）。
	//
	// 与 ScriptCreator 一样，它只在构建 Client 时注入一次，由 Provider 在执行工具前注入
	// 工具执行 ctx（见 agent.WithToolRuntime），供 builtin 的 create_script_analysis 工具使用。
	AnalysisNodeCreator tool.AnalysisNodeCreator `json:"-"`

	// AnalysisNodeParamsBuilder 是「按 io_schema 生成分析节点参数」的进程级静态服务能力（可为 nil）。
	//
	// 与 ScriptCreator 一样，它只在构建 Client 时注入一次，由 Provider 在执行工具前注入
	// 工具执行 ctx（见 agent.WithToolRuntime），供 builtin 的 build_analysis_node_params 工具使用。
	AnalysisNodeParamsBuilder tool.AnalysisNodeParamsBuilder `json:"-"`

	// AnalysisNodeUpdater 是「更新分析节点基本信息（node_name）」的进程级静态服务能力（可为 nil）。
	//
	// 与 AnalysisNodeParamsBuilder 一样，它只在构建 Client 时注入一次，由 Provider 在执行
	// 工具前注入工具执行 ctx（见 agent.WithToolRuntime），供 builtin 的 update_analysis_node
	// 工具使用。
	AnalysisNodeUpdater tool.AnalysisNodeUpdater `json:"-"`

	// AgentEnvUpdater 是「把用户当前业务上下文（llmEnv）写回用户记录」的进程级静态服务能力（可为 nil）。
	//
	// 与 ScriptCreator 一样，它只在构建 Client 时注入一次，由 Provider 在执行工具前注入
	// 工具执行 ctx（见 agent.WithToolRuntime），供 builtin 的 create_script_analysis 工具在
	// 创建新分析节点后把当前上下文切换过去（EnvID = 新节点 ID，EnvType = analysisNode）。
	AgentEnvUpdater tool.AgentEnvUpdater `json:"-"`

	// MemorySaver 是「保存用户长期记忆」的进程级静态服务能力（可为 nil）。
	//
	// 与 ScriptCreator 一样，它只在构建 Client 时注入一次，由 Provider 在执行工具前注入
	// 工具执行 ctx（见 agent.WithToolRuntime），供 builtin 的 save_memory 工具把「遇到的
	// 可复现错误 / 注意事项」写入用户长期记忆，后续轮次随背景注入回到系统提示词。
	MemorySaver tool.MemorySaver `json:"-"`

	// ProjectResourceBrowser 是「浏览项目内脚本 / 分析节点」的进程级静态服务能力（可为 nil）。
	//
	// 与 FileReader 一样，它只在构建 Client 时注入一次，由 Provider 在执行工具前注入
	// 工具执行 ctx（见 agent.WithToolRuntime），供 builtin 的 list_project_scripts /
	// get_script / get_analysis_node 三个只读工具使用。
	ProjectResourceBrowser tool.ProjectResourceBrowser `json:"-"`

	// Skills 是技能注册表（可为 nil）。
	//
	// 它既用于把技能元信息（name / description）注入系统提示词，也由 Provider 注入
	// 工具执行 ctx（见 agent.WithToolRuntime），供 get_skill / load_skill_resource 工具
	// 在执行期按名称解析技能并读取指令与附属资源。
	Skills tool.SkillResolver `json:"-"`

	// FileHistory 是会话内文件版本历史服务（可为 nil）。
	//
	// 从 opencode internal/history 迁移而来，供 edit / write / patch 工具记录文件版本。
	// 与其它服务能力一样，它只在构建 Client 时注入一次，由 Provider 在执行工具前
	// 注入工具执行 ctx（见 agent.WithToolRuntime → tool.WithFileHistory）。
	FileHistory history.Service `json:"-"`

	// LSP 是 LSP 客户端管理器（可为 nil）。
	//
	// 由上层启动并持有各语言的 LSP 客户端；Provider 在每次调用前通过 `LSP.Clients()`
	// 取当前客户端快照注入工具执行 ctx，供 diagnostics / view / edit / write / patch 使用。
	LSP *lspmanager.Manager `json:"-"`

	// Sessions / Messages 是会话与消息的持久化服务（可为 nil）。
	//
	// custom Provider 用它们为子 Agent 创建子会话（ParentSessionID = 父会话）并写入
	// 子 Agent 的 reasoning / tool_call / tool_result / 最终回答；任一为 nil 时子 Agent
	// 不落库，只把最终结果回填给父 Agent（旧行为）。
	Sessions session.Service `json:"-"`
	Messages message.Service `json:"-"`
}

// ResolveProvider 按模型名从 ProviderSource 解析本次调用的模型提供商配置。
//
// 未配置 ProviderSource 或未命中模型名时返回 nil（走官方 API / 默认配置）。
func (o Options) ResolveProvider(modelKey string) *ModelProviderConfig {
	if o.ProviderSource == nil {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(modelKey))
	if key == "" {
		return nil
	}
	pc, ok := o.ProviderSource()[key]
	if !ok {
		return nil
	}
	return &pc
}

// Registry 负责注册与解析 Provider。
// 容器启动时把全部 Provider 注册进来，运行时按名称解析。
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry 创建 Registry 并注册传入的 Provider。
func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{providers: make(map[string]Provider)}
	for _, p := range providers {
		r.Register(p)
	}
	return r
}

// Register 注册 Provider；同名覆盖。
func (r *Registry) Register(p Provider) {
	if p == nil {
		return
	}
	name := strings.ToLower(strings.TrimSpace(p.Name()))
	r.mu.Lock()
	r.providers[name] = p
	r.mu.Unlock()
}

// Resolve 按名称解析并构建 Agent 实例。
func (r *Registry) Resolve(name string, opts Options) (Agent, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	r.mu.RLock()
	p, ok := r.providers[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("agent: unknown provider %q", name)
	}
	return p.New(opts)
}

// Has 报告指定名称的 Provider 是否已注册。
func (r *Registry) Has(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	r.mu.RLock()
	_, ok := r.providers[name]
	r.mu.RUnlock()
	return ok
}

// Names 返回已注册的 Provider 名称列表（升序，便于调试与校验）。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
