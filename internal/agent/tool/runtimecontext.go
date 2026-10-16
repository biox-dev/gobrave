package tool

import (
	"context"
	"fmt"
	"strings"
)

// 本文件提供「工具执行期运行时上下文」的载体。
//
// builtin 工具是进程内单例（见 builtin.All），在构建期拿不到任何业务上下文；而部分工具
// （如 run_analysis_node）在执行时既需要业务变量（analysis_node_id 等，来自 RuntimeContext
// 的 SystemPromptVars），又需要业务服务（AnalysisNodeRunner）。为此约定：
//
//   - RuntimeContextResolver 解析业务 env(type,id) 后产出 RuntimeContext（纯可序列化的
//     业务事实），随请求（agent.Request.RuntimeContext）传到配置了工具的 Provider，并随
//     Request 一起被 JSON 序列化进 agent_tasks.request 以便重启后恢复；
//   - 服务能力（AnalysisNodeRunner）不放 RuntimeContext，而是作为进程级静态依赖放在
//     agent.Options 上，由 Provider 在执行工具前合并进 ToolRuntime；
//   - builtin 工具在 Execute(ctx, ...) 中用 RuntimeContextFrom / AnalysisNodeRunnerFrom
//     分别取出业务变量与服务能力。
//
// 该载体刻意不依赖 agent 包（避免 tool ← agent 的循环引用）：服务能力以最小接口形式
// 在 tool 包声明，由上层在注入时提供实现。

// RuntimeContext 是一次 Agent 调用的运行时上下文（业务变量）。
//
// 它是「业务 env(type,id) → 系统提示词变量 / 工作目录」的载体：由 handler 的
// RuntimeContextResolver 解析产出，随 agent.Request 传递到工具执行期，并随 Request
// 一起被 JSON 序列化进 agent_tasks.request 持久化（gorm serializer:json）。
//
// 注意：本结构是纯可序列化的业务事实，会随 Request 一起被 JSON 序列化进 agent_tasks
// 落库（Task.Request，gorm serializer:json），因此这里绝不能放服务对象
// （如 AnalysisNodeRunner）——服务能力是进程级静态依赖，改由 agent.Options 注入并在
// 执行期与业务变量合并。
type RuntimeContext struct {
	// Type 是归一化后的业务类型（script / analysis / analysisNode / projectReport，
	// 为空表示默认工作区）。
	Type string `json:"type,omitempty"`
	// Label 是人类可读的上下文名称（如节点名 / 报告标题），用于前端展示。
	Label string `json:"label,omitempty"`
	// SystemPromptVars 是渲染 Profile 系统提示词模板（pongo2）所需的变量
	// （如 analysis_node_id / workspace_directory / script_file_path 等）。
	SystemPromptVars map[string]any `json:"system_prompt_vars,omitempty"`
	// WorkingDir 是 Agent 执行工作目录。
	WorkingDir string `json:"working_dir,omitempty"`
}

// AnalysisNodeRunner 抽象「运行分析节点并等待完成」的业务能力。
//
// 由上层（container / handler）注入具体实现，把异步提交、状态轮询、日志读取等业务细节
// 收敛在一处；tool 包只负责把它暴露给模型。
//
// 它是进程级静态依赖（一次注入、所有请求共用），因此不放入 RuntimeContext，而由
// agent.Options.AnalysisNodeRunner 承载，经 WithAnalysisNodeRunner 注入工具执行 ctx。
type AnalysisNodeRunner interface {
	// RunAnalysisNode 触发分析节点执行并阻塞等待其进入终态，返回执行日志内容。
	//
	// timeoutSeconds <= 0 时由实现选取默认超时。节点运行失败（非成功终态 /
	// 超时）时返回错误，同时返回已读取到的日志片段（可能为空）。
	RunAnalysisNode(ctx context.Context, nodeID int64, timeoutSeconds int) (AnalysisNodeRunResult, error)
}

// AnalysisNodeRunResult 是一次分析节点执行的结果。
type AnalysisNodeRunResult struct {
	NodeID  int64  `json:"analysis_node_id"`
	Status  string `json:"status"`  // 节点终态（如 success / failed / timeout）
	Log     string `json:"log"`     // 节点 command.log 的内容
	Success bool   `json:"success"` // 是否为成功终态
}

// SystemPromptVar 从 RuntimeContext.SystemPromptVars 中取一个字符串值。
//
// 返回值会做 TrimSpace；上下文为 nil / 键不存在 / 值为空时返回空字符串。
func (rc *RuntimeContext) SystemPromptVar(key string) string {
	if rc == nil || rc.SystemPromptVars == nil {
		return ""
	}
	return toString(rc.SystemPromptVars[key])
}

// Vars 返回渲染系统提示词模板所用的变量；上下文为 nil 时返回 nil。
//
// 供调用方（如 AgentService）nil-safe 地读取 SystemPromptVars，避免直接字段访问 panic。
func (rc *RuntimeContext) Vars() map[string]any {
	if rc == nil {
		return nil
	}
	return rc.SystemPromptVars
}

// runtimeContextKey 是 RuntimeContext 在 context 中的私有键类型。
type runtimeContextKey struct{}

// WithRuntimeContext 把运行时上下文注入 context，供 builtin 工具在执行期读取。
//
// 传入 nil 时返回原 context（不注入）。
func WithRuntimeContext(ctx context.Context, rc *RuntimeContext) context.Context {
	if ctx == nil || rc == nil {
		return ctx
	}
	return context.WithValue(ctx, runtimeContextKey{}, rc)
}

// RuntimeContextFrom 取出注入的运行时上下文；未注入时返回 nil。
func RuntimeContextFrom(ctx context.Context) *RuntimeContext {
	if ctx == nil {
		return nil
	}
	rc, _ := ctx.Value(runtimeContextKey{}).(*RuntimeContext)
	return rc
}

// SystemPromptVar 从注入的 RuntimeContext 的业务变量中取一个字符串值。
//
// 返回值会做 TrimSpace；上下文未注入 / 键不存在 / 值为空时返回空字符串。
func SystemPromptVar(ctx context.Context, key string) string {
	return RuntimeContextFrom(ctx).SystemPromptVar(key)
}

// analysisNodeRunnerKey 是 AnalysisNodeRunner 在 context 中的私有键类型。
//
// 与 runtimeContextKey 分开承载：RuntimeContext 是可序列化的业务事实，而
// AnalysisNodeRunner 是不可序列化的进程级服务——两者来源不同（前者来自业务
// env 解析，后者来自 agent.Options），生命周期也不同，故不混入同一结构。
type analysisNodeRunnerKey struct{}

// WithAnalysisNodeRunner 把「运行分析节点」的服务能力注入 context，供 builtin 工具执行期读取。
//
// 传入 nil 时返回原 context（不注入）。
func WithAnalysisNodeRunner(ctx context.Context, runner AnalysisNodeRunner) context.Context {
	if ctx == nil || runner == nil {
		return ctx
	}
	return context.WithValue(ctx, analysisNodeRunnerKey{}, runner)
}

// AnalysisNodeRunnerFrom 取出注入的「运行分析节点」服务能力；未注入时返回 nil。
func AnalysisNodeRunnerFrom(ctx context.Context) AnalysisNodeRunner {
	if ctx == nil {
		return nil
	}
	runner, _ := ctx.Value(analysisNodeRunnerKey{}).(AnalysisNodeRunner)
	return runner
}

// FileReader 抽象「按 go_file 主键 ID 读取文件」的业务能力。
//
// 它把「file_id → 文件元信息（含 path）+ 内容预览」的业务细节收敛在上层实现
// （见 container），tool 包只负责把它暴露给模型。对 tsv / csv / xlsx（全部 sheet）
// 会解析出列信息与前若干行数据；其它格式只返回文件路径。
//
// 与 AnalysisNodeRunner 一样，它是进程级静态依赖（一次注入、所有请求共用），
// 因此不放入 RuntimeContext，而由 agent.Options 承载，经 WithFileReader 注入工具执行 ctx。
type FileReader interface {
	// ReadFileByID 按 go_file 主键 ID 读取文件，返回文件元信息与内容预览。
	//
	// fileID 非法（<=0）或文件不存在时返回错误。
	ReadFileByID(ctx context.Context, fileID int64) (FileReadResult, error)
}

// FileReadResult 是一次「按 ID 读取文件」的结果。
//
// 对 tsv / csv / xlsx：Sheets 承载每个 sheet 的列名与预览行；对其它格式：
// Sheets 为空，仅 FilePath 有效。
type FileReadResult struct {
	FileID   int64          `json:"file_id"`
	FileName string         `json:"file_name,omitempty"`
	FilePath string         `json:"file_path"`
	Format   string         `json:"format,omitempty"`
	Sheets   []FileSheetTab `json:"sheets,omitempty"`
	Message  string         `json:"message,omitempty"`
}

// FileSheetTab 是单个 sheet / 数据表的内容预览。
type FileSheetTab struct {
	Name    string           `json:"name"`
	Columns []string         `json:"columns,omitempty"`
	Rows    []map[string]any `json:"rows,omitempty"`
}

// fileReaderKey 是 FileReader 在 context 中的私有键类型。
//
// 与 analysisNodeRunnerKey 分开承载：均为不可序列化的进程级服务，来源与生命周期一致
// （均来自 agent.Options），但职责不同，故各自独立存放。
type fileReaderKey struct{}

// WithFileReader 把「按 ID 读取文件」的服务能力注入 context，供 builtin 工具执行期读取。
//
// 传入 nil 时返回原 context（不注入）。
func WithFileReader(ctx context.Context, reader FileReader) context.Context {
	if ctx == nil || reader == nil {
		return ctx
	}
	return context.WithValue(ctx, fileReaderKey{}, reader)
}

// FileReaderFrom 取出注入的「按 ID 读取文件」服务能力；未注入时返回 nil。
func FileReaderFrom(ctx context.Context) FileReader {
	if ctx == nil {
		return nil
	}
	reader, _ := ctx.Value(fileReaderKey{}).(FileReader)
	return reader
}

// toString 把任意值尽力转为去空白的字符串。
func toString(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(s)
	case []byte:
		return strings.TrimSpace(string(s))
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}
