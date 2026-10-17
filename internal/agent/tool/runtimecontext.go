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
	// ProjectID 是本次调用所属项目的 ID（当前用户激活项目），供工具在执行期做项目级
	// 数据定位。0 表示未知（未解析到激活项目）。
	ProjectID int64 `json:"project_id,omitempty,string"`
	// UserID 是本次调用发起用户的 ID（请求级事实，由上层按 Request.UserID 注入）。
	//
	// 供需要「按用户隔离数据」的工具（如 save_memory 写长期记忆）在
	// RuntimeContext 内直接取到归属用户；0 表示未知（未注入）。
	UserID int64 `json:"user_id,omitempty,string"`
	// SessionID 是本次调用所属的会话 ID（请求级事实，由上层按 Request.SessionID 注入）。
	//
	// 它是会话主键（雪花 ID），0 表示未知（未注入）。供记忆检索 / 写入把记忆与
	// 会话关联（例如把某轮踩坑经验记在当次会话下）。
	SessionID int64 `json:"session_id,omitempty,string"`
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
	// requestParam 是本次运行的请求参数（键为脚本 io_schema inputs 的 name），可选：
	// 非空时以它为准（并回写节点记录），为空时回退到 AnalysisNode 记录上已保存的
	// request_param。
	//
	// 实现必须在每次运行前按 analysisform.BuildParseAnalysisResult 解析 request_param：
	// 先把解析结果（request_param / resolved_inputs / inputs_patterns / output_patterns）
	// 回写到 AnalysisNode，清空上一轮 input_validation_errors / output_validation_errors，
	// 再重新生成 params.json，最后提交运行。
	//
	// timeoutSeconds <= 0 时由实现选取默认超时。节点运行失败（非成功终态 /
	// 超时）时返回错误，同时返回已读取到的日志片段（可能为空）。
	RunAnalysisNode(ctx context.Context, nodeID int64, requestParam map[string]any, timeoutSeconds int) (AnalysisNodeRunResult, error)
}

// AnalysisNodeRunResult 是一次分析节点执行的结果。
type AnalysisNodeRunResult struct {
	NodeID  int64  `json:"analysis_node_id"`
	Status  string `json:"status"`  // 节点终态（如 success / failed / timeout）
	Log     string `json:"log"`     // 节点 command.log 的内容
	Success bool   `json:"success"` // 是否为成功终态

	// WorkspaceDir 是节点根目录（绝对路径），节点的 params.json / run.sh / output /
	// command.log 全部由它派生，方便模型在该目录下定位产物。
	WorkspaceDir string `json:"workspace_dir,omitempty"`
	// CommandPath 是本次运行实际执行的 run.sh 绝对路径，便于模型读脚本复核参数。
	CommandPath string `json:"command_path,omitempty"`
	// ParamsPath 是节点 params.json 的绝对路径（本次运行的输入参数）。
	ParamsPath string `json:"params_path,omitempty"`
	// LogPath 是节点命令日志 command.log 的绝对路径。Log 会按 maxLogBytes 截断，
	// 模型可在需要完整日志时用 read_file 类工具按该路径读取。
	LogPath string `json:"log_path,omitempty"`
	// ParamsJSON 是本次运行实际使用的 params.json 正文（运行前由 runner 重新生成），
	// 便于模型确认脚本实际拿到的参数，无需再读盘。
	ParamsJSON string `json:"params_json,omitempty"`
	// RequestParam 是本次运行使用的请求参数（键为 io_schema inputs 的 name）。
	// 传入 request_param 时为本次入参，未传时从节点记录的 request_param 反解而来。
	RequestParam map[string]any `json:"request_param,omitempty"`
	// ResolvedInputs 是按脚本 io_schema 解析 request_param 后的结构化输入。
	ResolvedInputs map[string]any `json:"resolved_inputs,omitempty"`
	// InputValidationErrors / OutputValidationErrors 是本次运行前写回节点的校验错误
	// 快照（运行前会被清空，由编排器在运行期重新填充）。
	InputValidationErrors  []any `json:"input_validation_errors,omitempty"`
	OutputValidationErrors []any `json:"output_validation_errors,omitempty"`
	// ParamsWarnings 是参数解析阶段发现的非致命问题（如缺少必填输入、
	// 存在 io_schema 未声明的键）。
	ParamsWarnings []string `json:"params_warnings,omitempty"`
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

// UserID 返回本次调用的用户 ID；上下文为 nil 时返回 0。
func (rc *RuntimeContext) UserIDValue() int64 {
	if rc == nil {
		return 0
	}
	return rc.UserID
}

// WithUserIDForContext 返回一个补齐了用户 / 会话事实的 RuntimeContext 副本。
//
// RuntimeContext 由 Handler 的 RuntimeContextResolver 产出，是请求级业务事实的载体；
// 用户与会话 ID 都是「一次调用」的属性（不属于某个业务 env），因此允许调用方在产出后
// 追加：rc 为 nil 且 userID / sessionID 都为空时返回 nil（表示本轮确实没有上下文）。
//
// 参数均为 0 时不覆盖已有值（增量补齐语义）。
func WithUserIDForContext(rc *RuntimeContext, userID int64, sessionID int64) *RuntimeContext {
	if rc == nil && userID <= 0 && sessionID <= 0 {
		return nil
	}
	if rc != nil && (userID <= 0 || rc.UserID == userID) && (sessionID <= 0 || rc.SessionID == sessionID) {
		return rc
	}
	var cp RuntimeContext
	if rc != nil {
		cp = *rc
	}
	if userID > 0 {
		cp.UserID = userID
	}
	if sessionID > 0 {
		cp.SessionID = sessionID
	}
	return &cp
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

// UserIDFrom 取出注入的运行时上下文中的用户 ID；未注入时返回 0。
//
// 用户 ID 不再单独存一个 context 键：RuntimeContext.UserID 就是请求级用户事实的唯一来源，
// 工具统一通过本函数读取（如 create_script 写回用户业务上下文、save_memory 归属记忆）。
func UserIDFrom(ctx context.Context) int64 {
	return RuntimeContextFrom(ctx).UserIDValue()
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

// ScriptCreator 抽象「创建脚本」与「为脚本创建分析节点」两件独立的事。
//
// 职责刻意拆开（脚本与节点是两个独立资源）：
//   - CreateScript 只落库 Script（pipeline_components）并初始化脚本目录
//     （主文件 / io_schema.json），不创建任何分析节点；
//   - CreateAnalysisNode 为已有脚本创建一个独立分析节点，并准备工作目录
//     （params.json / run.sh 等）。
//
// 这样模型可以先把脚本与 io_schema.json 写好，再决定要建几个节点；不需要节点的
// 场景（只写脚本）也不必凭空造一个节点。这些细节收敛在上层实现（见 container），
// tool 包只负责把能力暴露给模型。
//
// 与 AnalysisNodeRunner / FileReader 一样，它是进程级静态依赖（一次注入、所有请求共用），
// 因此不放入 RuntimeContext，而由 agent.Options 承载，经 WithScriptCreator 注入工具执行 ctx。
type ScriptCreator interface {
	// CreateScript 创建脚本并初始化脚本目录，返回脚本 ID 与主文件 / io_schema.json 路径。
	//
	// projectID 取自 RuntimeContext.ProjectID（当前用户激活项目），必须为正数。
	// input.ContainerTemplateID 为空（0）时由实现解析系统默认容器模板；
	// 没有配置默认模板时返回错误，提示用户先到容器模板页设置默认模板。
	CreateScript(ctx context.Context, input ScriptCreateInput) (ScriptCreateResult, error)

	// CreateAnalysisNode 为已有脚本创建一个独立分析节点（create_analysis_node）。
	//
	// 脚本必须属于 input.ProjectID；request_param 非空时按脚本 io_schema 解析出
	// resolved_inputs 并写出 params.json，为空时节点只按 io_schema 初始化 patterns。
	//
	// 节点在创建时必然要落地 inputs_patterns / output_patterns，因此 io_schema.json
	// 必须已经写好（先 CreateScript，或至少已写入一份契约）；缺失时返回错误。
	CreateAnalysisNode(ctx context.Context, input AnalysisNodeCreateInput) (AnalysisNodeCreateResult, error)
}

// AnalysisNodeCreateInput 是「为已有脚本创建分析节点」的入参。
type AnalysisNodeCreateInput struct {
	// ProjectID 是脚本所属项目（取自 RuntimeContext.ProjectID），用于校验脚本归属。
	ProjectID int64 `json:"project_id,string"`
	// ScriptID 是 Script 主键（pipeline_components.id）。
	ScriptID int64 `json:"script_id,string"`
	// NodeName 为节点名称，为空时回落脚本名称。
	NodeName string `json:"node_name,omitempty"`
	// RequestParam 是初始请求参数（键为 io_schema inputs 的 name）；为空时节点只带 patterns。
	RequestParam map[string]any `json:"request_param,omitempty"`
}

// AnalysisNodeCreateResult 是「为已有脚本创建分析节点」的结果（回传给模型）。
//
// 除节点自身的路径之外，把脚本在磁盘上的关键路径（ScriptFilePath / IOSchemaPath）
// 一并回传：节点建成后通常需要紧接着改脚本内容或补参数契约，模型无需再调
// get_script / read_file 定位这些文件。
type AnalysisNodeCreateResult struct {
	AnalysisNodeID int64  `json:"analysis_node_id,string"`
	NodeName       string `json:"node_name"`
	ScriptID       int64  `json:"script_id,string"`
	// ScriptName / ScriptType 是脚本的基本信息（便于模型确认节点绑定哪个脚本）。
	ScriptName string `json:"script_name,omitempty"`
	ScriptType string `json:"script_type,omitempty"`
	// ScriptFilePath 是脚本主文件（如 main.R）的绝对路径，由 utils.GetScriptFile 推导。
	ScriptFilePath string `json:"script_file_path,omitempty"`
	// IOSchemaPath 是脚本目录下 io_schema.json 的绝对路径（节点 patterns 的来源）。
	IOSchemaPath string         `json:"io_schema_path,omitempty"`
	WorkspaceDir string         `json:"workspace_dir"`
	ParamsPath   string         `json:"params_path"`
	RequestParam map[string]any `json:"request_param,omitempty"`
}

// ScriptUpdater 抽象「更新已有脚本基本信息（name / description）」的业务能力。
//
// 它只覆盖脚本名称与描述这两个元数据字段，不触碰脚本内容（脚本主文件由模型直接读写）、
// io_schema 与容器模板，因此实现必须是「读改写」：先按 script_id 读出完整 Script 记录，
// 只覆盖请求中给出的字段，再整体回写，避免把未提交的字段写成零值
// （workflowRepository.UpdateScript 是整体替换语义）。
//
// 与 ScriptCreator 一样，它是进程级静态依赖（一次注入、所有请求共用），
// 因此不放入 RuntimeContext，而由 agent.Options 承载，经 WithScriptUpdater 注入工具执行 ctx。
type ScriptUpdater interface {
	// UpdateScriptInfo 更新脚本的 name / description，返回更新后的关键信息。
	//
	// projectID 取自 RuntimeContext.ProjectID（当前用户激活项目）：脚本必须属于该项目，
	// 否则返回错误（避免模型改到别的项目的脚本）。
	// scriptID 是 Script 主键（int64），必须为正数。
	// name / description 为指针：nil 表示「不修改该字段」，非 nil 时按值覆盖
	// （description 允许被显式置为空字符串以清空描述）。
	UpdateScriptInfo(ctx context.Context, input ScriptUpdateInput) (ScriptUpdateResult, error)
}

// ScriptUpdateInput 是「更新脚本基本信息」的入参。
type ScriptUpdateInput struct {
	// ProjectID 是脚本所属项目（取自 RuntimeContext.ProjectID）。
	ProjectID int64 `json:"project_id,string"`
	// ScriptID 是 Script 主键（pipeline_components.id），必须为正数。
	ScriptID int64 `json:"script_id,string"`
	// Name / Description 为 nil 表示不修改；非 nil 时覆盖对应字段。
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// ScriptUpdateResult 是「更新脚本基本信息」的结果（回传给模型）。
type ScriptUpdateResult struct {
	ScriptID       int64  `json:"script_id,string"`
	ScriptName     string `json:"script_name"`
	Description    string `json:"description,omitempty"`
	ScriptType     string `json:"script_type,omitempty"`
	ScriptFilePath string `json:"script_file_path,omitempty"`
	IOSchemaPath   string `json:"io_schema_path,omitempty"`
	// Changed 列出本次实际被修改的字段名（name / description），便于模型确认。
	Changed []string `json:"changed,omitempty"`
}

// scriptUpdaterKey 是 ScriptUpdater 在 context 中的私有键类型。
type scriptUpdaterKey struct{}

// WithScriptUpdater 把「更新脚本基本信息」的服务能力注入 context，供 builtin 工具执行期读取。
//
// 传入 nil 时返回原 context（不注入）。
func WithScriptUpdater(ctx context.Context, updater ScriptUpdater) context.Context {
	if ctx == nil || updater == nil {
		return ctx
	}
	return context.WithValue(ctx, scriptUpdaterKey{}, updater)
}

// ScriptUpdaterFrom 取出注入的「更新脚本基本信息」服务能力；未注入时返回 nil。
func ScriptUpdaterFrom(ctx context.Context) ScriptUpdater {
	if ctx == nil {
		return nil
	}
	updater, _ := ctx.Value(scriptUpdaterKey{}).(ScriptUpdater)
	return updater
}

// ProjectResourceBrowser 抽象「浏览项目内的脚本 / 分析节点」的业务能力。
//
// 供内置工具 list_project_scripts / get_script / get_analysis_node 使用：三者都是纯只读
// 查询，统一由同一份业务实现承载（见 container.projectResourceBrowser），避免为每个工具
// 各建一个注入点。
//
// 与 ScriptCreator 一样，它是进程级静态依赖（一次注入、所有请求共用），因此不放入
// RuntimeContext，而由 agent.Options 承载，经 WithProjectResourceBrowser 注入工具执行 ctx。
type ProjectResourceBrowser interface {
	// ListScriptsByProject 返回指定项目下的全部脚本（item_type/pipeline 中 component_type=script）。
	//
	// 结果按 id 倒序，不分页（项目内脚本数量有限）；projectID 必须为正数。
	ListScriptsByProject(ctx context.Context, projectID int64) ([]ScriptBrief, error)

	// GetScriptByID 按主键读取脚本（必须属于当前激活项目，否则返回错误）。
	//
	// projectID 取自 RuntimeContext.ProjectID，scriptID 是 pipeline_components 主键。
	GetScriptByID(ctx context.Context, projectID int64, scriptID int64) (ScriptBrief, error)

	// GetAnalysisNodeByID 按主键读取分析节点（必须属于当前激活项目，否则返回错误）。
	//
	// 返回的 WorkspaceDir 是绝对路径，ParamsPath 由 WorkspaceDir 派生。
	GetAnalysisNodeByID(ctx context.Context, projectID int64, nodeID int64) (AnalysisNodeBrief, error)
}

// ScriptBrief 是脚本的只读摘要（工具回传给模型的最小字段集）。
//
// ScriptFilePath / IOSchemaPath 由 utils.GetScriptFile / utils.WorkflowIOSchemaPath 推导，
// 模型可直接按这两个路径读写脚本主文件与 io_schema.json。
type ScriptBrief struct {
	ScriptID       int64  `json:"script_id"`
	ScriptName     string `json:"script_name"`
	Description    string `json:"description,omitempty"`
	ScriptType     string `json:"script_type,omitempty"`
	ScriptFilePath string `json:"script_file_path"`
	IOSchemaPath   string `json:"io_schema_path"`
}

// AnalysisNodeBrief 是分析节点的只读摘要（工具回传给模型的最小字段集）。
type AnalysisNodeBrief struct {
	AnalysisNodeID int64          `json:"analysis_node_id"`
	NodeName       string         `json:"node_name"`
	ScriptID       int64          `json:"script_id,omitempty"`
	WorkspaceDir   string         `json:"workspace_dir"`
	ParamsPath     string         `json:"params_path"`
	RequestParam   map[string]any `json:"request_param,omitempty"`
}

// projectResourceBrowserKey 是 ProjectResourceBrowser 在 context 中的私有键类型。
type projectResourceBrowserKey struct{}

// WithProjectResourceBrowser 把「浏览项目内脚本 / 分析节点」的服务能力注入 context。
//
// 传入 nil 时返回原 context（不注入）。
func WithProjectResourceBrowser(ctx context.Context, browser ProjectResourceBrowser) context.Context {
	if ctx == nil || browser == nil {
		return ctx
	}
	return context.WithValue(ctx, projectResourceBrowserKey{}, browser)
}

// ProjectResourceBrowserFrom 取出注入的「浏览项目内脚本 / 分析节点」服务能力；未注入时返回 nil。
func ProjectResourceBrowserFrom(ctx context.Context) ProjectResourceBrowser {
	if ctx == nil {
		return nil
	}
	browser, _ := ctx.Value(projectResourceBrowserKey{}).(ProjectResourceBrowser)
	return browser
}

// ScriptCreateInput 是「创建脚本」的入参（不创建分析节点）。
type ScriptCreateInput struct {
	// ProjectID 是脚本所属项目（取自 RuntimeContext.ProjectID）。
	ProjectID int64 `json:"project_id,string"`
	// 以下为 Script 字段。
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// ContainerTemplateID 为 0 时表示未指定，由实现解析系统默认容器模板。
	ContainerTemplateID int64  `json:"container_template_id,string"`
	ScriptType          string `json:"script_type"`
	// Inputs / Outputs 是脚本的输入输出契约（io_schema.json 的 inputs / outputs）。
	// 非空时由实现写入脚本目录的 io_schema.json；为空时实现回落默认 schema。
	// 每一项至少应包含 name / type，可选 label / required / description 等字段。
	Inputs  []map[string]any `json:"inputs,omitempty"`
	Outputs []map[string]any `json:"outputs,omitempty"`
}

// ScriptCreateResult 是「创建脚本」的结果（回传给模型）。
//
// 只包含脚本自身的产物路径：模型据此直接编辑脚本主文件与 io_schema.json，
// 之后再按需调用 create_analysis_node 创建节点。
type ScriptCreateResult struct {
	ScriptID int64 `json:"script_id,string"`
	// ScriptKey 是 Script.ScriptID（组件业务 ID，脚本目录名）。
	ScriptKey      string `json:"script_key"`
	ScriptName     string `json:"script_name"`
	ScriptFilePath string `json:"script_file_path"`
	IOSchemaPath   string `json:"io_schema_path"`
	// ScriptType 是实际生效的脚本类型（未传时为默认类型）。
	ScriptType string `json:"script_type,omitempty"`

	// ContainerTemplateID 是脚本最终使用的容器模板 ID（未显式指定时为解析出的默认模板）。
	ContainerTemplateID   int64  `json:"container_template_id,string"`
	ContainerTemplateName string `json:"container_template_name,omitempty"`
}

// scriptCreatorKey 是 ScriptCreator 在 context 中的私有键类型。
type scriptCreatorKey struct{}

// WithScriptCreator 把「创建脚本 + 分析节点」的服务能力注入 context，供 builtin 工具执行期读取。
//
// 传入 nil 时返回原 context（不注入）。
func WithScriptCreator(ctx context.Context, creator ScriptCreator) context.Context {
	if ctx == nil || creator == nil {
		return ctx
	}
	return context.WithValue(ctx, scriptCreatorKey{}, creator)
}

// ScriptCreatorFrom 取出注入的「创建脚本 + 分析节点」服务能力；未注入时返回 nil。
func ScriptCreatorFrom(ctx context.Context) ScriptCreator {
	if ctx == nil {
		return nil
	}
	creator, _ := ctx.Value(scriptCreatorKey{}).(ScriptCreator)
	return creator
}

// AgentEnvUpdater 抽象「把用户当前业务上下文（llmEnv）写回用户记录」的业务能力。
//
// 业务页面进入时由前端调用 /auth/agent-env 写入 user.AgentConfig.EnvID / EnvType；
// 而 create_script 这类「在 Agent 会话内新建了业务对象」的工具，需要把上下文就地切到
// 新对象上（新分析节点），否则后续对话仍停留在旧上下文 / 默认工作目录。该能力让工具
// 无需依赖具体的 user service / repository。
//
// 与 ScriptCreator 一样，它是进程级静态依赖（一次注入、所有请求共用），
// 因此不放入 RuntimeContext，而由 agent.Options 承载，经 WithAgentEnvUpdater 注入工具执行 ctx。
type AgentEnvUpdater interface {
	// UpdateAgentEnv 把指定用户的 AgentConfig.EnvID / EnvType 覆盖为给定值。
	//
	// 只改业务上下文，不触碰 Profile 与权限策略。envType 为空表示清空上下文；
	// 非法 envType（不属于 handler 约定的枚举）由实现决定是否报错。
	UpdateAgentEnv(ctx context.Context, userID int64, envID string, envType string) error
}

// agentEnvUpdaterKey 是 AgentEnvUpdater 在 context 中的私有键类型。
type agentEnvUpdaterKey struct{}

// WithAgentEnvUpdater 把「更新用户业务上下文」的服务能力注入 context，供 builtin 工具执行期读取。
//
// 传入 nil 时返回原 context（不注入）。
func WithAgentEnvUpdater(ctx context.Context, updater AgentEnvUpdater) context.Context {
	if ctx == nil || updater == nil {
		return ctx
	}
	return context.WithValue(ctx, agentEnvUpdaterKey{}, updater)
}

// AgentEnvUpdaterFrom 取出注入的「更新用户业务上下文」服务能力；未注入时返回 nil。
func AgentEnvUpdaterFrom(ctx context.Context) AgentEnvUpdater {
	if ctx == nil {
		return nil
	}
	updater, _ := ctx.Value(agentEnvUpdaterKey{}).(AgentEnvUpdater)
	return updater
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

// AnalysisNodeParamsBuilder 抽象「按脚本 io_schema 解析请求参数 → 落库并生成 params.json」
// 的业务能力。
//
// 模型依据 io_schema.json 拼出 request_param 后调用该能力：上层按脚本表单
// （analysisform.BuildParseAnalysisResult）把 request_param 解析成结构化配方，
// 用它刷新分析节点的 inputs_patterns / output_patterns / resolved_inputs /
// request_param，再通过 NodeRuntimePreparer.Prepare 把配方写成节点 params.json /
// run.sh / env.sh，使 run_analysis_node 能直接以这份参数运行脚本。
//
// 与 AnalysisNodeRunner / ScriptCreator 一样，它是进程级静态依赖（一次注入、所有请求共用），
// 因此不放入 RuntimeContext，而由 agent.Options 承载，经 WithAnalysisNodeParamsBuilder
// 注入工具执行 ctx。
type AnalysisNodeParamsBuilder interface {
	// BuildAnalysisNodeParams 为指定分析节点生成运行期参数文件并落库。
	//
	// analysisNodeID <= 0 或 requestParam 为空时返回错误；projectID > 0 时用于校验节点归属，
	// 避免模型把参数写到别的项目的节点上。
	BuildAnalysisNodeParams(ctx context.Context, input AnalysisNodeParamsInput) (AnalysisNodeParamsResult, error)
}

// AnalysisNodeParamsInput 是「生成分析节点参数」的入参。
type AnalysisNodeParamsInput struct {
	// AnalysisNodeID 是目标分析节点主键。
	AnalysisNodeID int64 `json:"analysis_node_id,string"`
	// ProjectID 是当前激活项目 ID（>0 时校验节点归属）。
	ProjectID int64 `json:"project_id,string"`
	// RequestParam 是模型依据 io_schema.json 生成的请求参数，键为 io_schema 中的 name。
	RequestParam map[string]any `json:"request_param"`
}

// AnalysisNodeParamsResult 是「生成分析节点参数」的结果（回传给模型）。
//
// ParamsPath 与 ParamsJSON 是核心产物：ParamsJSON 是写入文件的 params.json 正文，
// 模型据此确认「io_schema 参数确实被正确映射」，无需再读盘。
type AnalysisNodeParamsResult struct {
	AnalysisNodeID int64  `json:"analysis_node_id,string"`
	ScriptID       int64  `json:"script_id,string"`
	WorkspaceDir   string `json:"workspace_dir"`
	ParamsPath     string `json:"params_path"`
	CommandPath    string `json:"command_path,omitempty"`
	// ParamsJSON 是写入 params.json 的实际内容（缩进后的 JSON 文本）。
	ParamsJSON string `json:"params_json"`
	// ResolvedInputs 是按脚本表单解析后的结构化输入（即写入 params.json 的主体）。
	ResolvedInputs map[string]any `json:"resolved_inputs,omitempty"`
	// Warnings 是解析过程中发现的非致命问题（如 io_schema 参数未被提供）。
	Warnings []string `json:"warnings,omitempty"`
}

// analysisNodeParamsBuilderKey 是 AnalysisNodeParamsBuilder 在 context 中的私有键类型。
type analysisNodeParamsBuilderKey struct{}

// WithAnalysisNodeParamsBuilder 把「生成分析节点参数」的服务能力注入 context，供 builtin 工具执行期读取。
//
// 传入 nil 时返回原 context（不注入）。
func WithAnalysisNodeParamsBuilder(ctx context.Context, builder AnalysisNodeParamsBuilder) context.Context {
	if ctx == nil || builder == nil {
		return ctx
	}
	return context.WithValue(ctx, analysisNodeParamsBuilderKey{}, builder)
}

// AnalysisNodeParamsBuilderFrom 取出注入的「生成分析节点参数」服务能力；未注入时返回 nil。
func AnalysisNodeParamsBuilderFrom(ctx context.Context) AnalysisNodeParamsBuilder {
	if ctx == nil {
		return nil
	}
	builder, _ := ctx.Value(analysisNodeParamsBuilderKey{}).(AnalysisNodeParamsBuilder)
	return builder
}

// MemorySaver 抽象「创建 / 更新一条用户长期记忆」的业务能力。
//
// 它把记忆域（agent.Memory / MemoryManager）的持久化细节收敛在上层实现（见 container），
// tool 包只负责把能力暴露给模型：模型在遭遇可复现的错误后，把「下次如何绕开」的注意事项
// 写入记忆，后续调用会随背景注入出现在系统提示词里。
//
// 与 AnalysisNodeRunner / ScriptCreator 一样，它是进程级静态依赖（一次注入、所有请求共用），
// 因此不放入 RuntimeContext，而由 agent.Options 承载，经 WithMemorySaver 注入工具执行 ctx。
type MemorySaver interface {
	// SaveMemory 创建或更新一条记忆。
	//
	// input.ID > 0 表示更新该条记忆（不存在时返回错误），为 0 表示新建；新建时主键由
	// 持久化层的 agent.Memory.BeforeCreate 统一分配（雪花 ID）。
	SaveMemory(ctx context.Context, input MemorySaveInput) (MemorySaveResult, error)
}

// MemorySaveInput 是「保存一条记忆」的入参。
//
// UserID 是记忆归属用户（长期记忆按用户隔离），由调用方从 RuntimeContext / context 取得，
// 不由模型填写，避免模型把记忆写到别的用户名下。
type MemorySaveInput struct {
	UserID int64 `json:"user_id,string"`
	// ID 为 0 表示新建（主键由 Memory.BeforeCreate 分配）；> 0 表示更新既有记忆。
	ID int64 `json:"id,omitempty,string"`
	// SessionID 可选：把记忆与某个具体会话关联（会话主键，0 表示不关联）。
	SessionID int64 `json:"session_id,omitempty,string"`
	// ProjectID 可选：记忆所属项目（写入 Metadata["project_id"]），用于上下文优先检索。
	ProjectID int64 `json:"project_id,omitempty,string"`
	// Kind 是记忆类别（fact / summary / note / event），非法值由实现兜底为 note。
	Kind string `json:"kind,omitempty"`
	// Content 是记忆正文（注意事项 / 事实 / 摘要）。
	Content string `json:"content"`
	// Importance 是重要度（0-10），用于检索排序。
	Importance int `json:"importance,omitempty"`
	// Metadata 是扩展信息（来源、标签等）。
	Metadata map[string]any `json:"metadata,omitempty"`
}

// MemorySaveResult 是「保存一条记忆」的结果（回传给模型）。
type MemorySaveResult struct {
	ID         int64  `json:"id,string"`
	Kind       string `json:"kind"`
	Importance int    `json:"importance"`
	// Created 标识本次是新建（true）还是更新既有记忆（false）。
	Created bool `json:"created"`
}

// memorySaverKey 是 MemorySaver 在 context 中的私有键类型。
type memorySaverKey struct{}

// WithMemorySaver 把「保存长期记忆」的服务能力注入 context，供 builtin 工具执行期读取。
//
// 传入 nil 时返回原 context（不注入）。
func WithMemorySaver(ctx context.Context, saver MemorySaver) context.Context {
	if ctx == nil || saver == nil {
		return ctx
	}
	return context.WithValue(ctx, memorySaverKey{}, saver)
}

// MemorySaverFrom 取出注入的「保存长期记忆」服务能力；未注入时返回 nil。
func MemorySaverFrom(ctx context.Context) MemorySaver {
	if ctx == nil {
		return nil
	}
	saver, _ := ctx.Value(memorySaverKey{}).(MemorySaver)
	return saver
}
