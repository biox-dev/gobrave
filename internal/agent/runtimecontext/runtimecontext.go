package runtimecontext

import (
	"context"
	"fmt"
	"strings"
)

// 本文件提供「工具执行期运行时上下文」的载体。
//
// 分两类职责：
//
//   - RuntimeContext（本文件的 context 载体）承载可序列化的业务变量（SystemPromptVars），
//     随请求（agent.Request.RuntimeContext）传到配置了工具的 Provider，并随 Request
//     一起被 JSON 序列化进 agent_tasks.request 以便重启后恢复；
//   - 服务能力接口（AnalysisNodeRunner / ScriptBrowser / SkillResolver 等）只是端口类型，
//     供 builtin.Deps / coder.Deps 在构造工具时注入具体实现；它们不再进 context。
//
// 该包刻意不依赖 agent 包（避免 tool ← agent 的循环引用），因此端口与上下文载体
// 都放在这里，由上层（container / providers）实现与装配。

// RuntimeContext 是一次 Agent 调用的运行时上下文（业务变量）。
//
// 它是「系统提示词模板变量」的载体：由 handler 的 RuntimeContextResolver 解析产出，
// 随 agent.Request 传递到工具执行期，并随 Request 一起被 JSON 序列化进
// agent_tasks.request 持久化（gorm serializer:json）。
// 工作目录属于执行事实，不在这里：由 Resolver 单独返回并落到 Task.WorkingDir。
//
// 注意：本结构是纯可序列化的业务事实，会随 Request 一起被 JSON 序列化进 agent_tasks
// 落库（Task.Request，gorm serializer:json），因此这里绝不能放服务对象——服务能力
// 是进程级静态依赖，改由 builtin.Deps / coder.Deps 在构造工具时注入。
type RuntimeContext struct {
	// SystemPromptVars 是渲染 Profile 系统提示词模板（pongo2）所需的变量
	// （如 analysis_node_id / workspace_directory / script_file_path 等）。
	SystemPromptVars map[string]any `json:"system_prompt_vars,omitempty"`
}

// AnalysisNodeRunner 抽象「运行分析节点并等待完成」的业务能力。
//
// 由上层（container / handler）实现，经 builtin.Deps 在构造工具时注入；tool 包只负责
// 把它暴露给模型。
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
	NodeID int64 `json:"analysis_node_id"`
	// ScriptID 是该节点绑定的脚本 ID：运行后用它配合 get_script / 脚本路径定位
	// 本次实际执行的脚本，无需再额外查节点。
	ScriptID int64  `json:"script_id,omitempty"`
	Status   string `json:"status"`  // 节点终态（如 success / failed / timeout）
	Log      string `json:"log"`     // 节点 command.log 的内容
	Success  bool   `json:"success"` // 是否为成功终态

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
	// InputValidationErrors / OutputValidationErrors 是本次运行结束后的校验错误快照：
	// 运行前会被清空，编排器只在本次运行失败时重新填充，因此成功终态一定为空
	// （不会携带上一轮运行遗留的缺失产物）。仅在节点进入终态时回传；超时 / 取消
	// 不带这两个字段（避免把历史快照当成结论）。
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

// AgentAnalysisRunner 抽象「创建命令型分析节点（可选运行）并等待其结束」的业务能力。
//
// 与 AnalysisNodeRunner 一样，它是进程级静态依赖，由 builtin.Deps 在构造工具时注入。
type AgentAnalysisRunner interface {
	// CreateAgentAnalysis 创建一条命令型 AnalysisNode（工作目录、run.sh、容器模板）；
	// Run 为 nil / true 时复用通用节点链路运行到终态并返回状态、退出码与命令日志，
	// Run 为 false 时只创建节点。超时 / 取消时返回错误。
	CreateAgentAnalysis(ctx context.Context, in AgentAnalysisInput) (AgentAnalysisResult, error)
}

// AgentAnalysisInput 是一次 AgentAnalysis 工具调用的入参。
type AgentAnalysisInput struct {
	// ProjectID 取自 RuntimeContext.ProjectID（当前用户激活项目）。
	ProjectID int64
	// Name 是任务名称（同时用作节点名称与容器资源名）。
	Name string
	// ContainerTemplateID 是模型选择的容器模板；必须 > 0（不回退到系统默认模板）。
	ContainerTemplateID int64
	// Command 是要执行的 shell 命令，由实现写入节点工作目录的 run.sh。
	Command string
	// Run 控制创建后是否立即运行：nil / true 表示运行（默认），false 表示只创建节点。
	Run *bool
	// TimeoutSeconds <= 0 时使用实现默认超时。
	TimeoutSeconds int
}

// AgentAnalysisResult 是一次 AgentAnalysis 的执行结果。
//
// AgentAnalysisID 是命令型 AnalysisNode 的主键（历史字段名保持不变，便于工具输出兼容）。
type AgentAnalysisResult struct {
	AgentAnalysisID     int64  `json:"agent_analysis_id"`
	ContainerInstanceID int64  `json:"container_instance_id,omitempty"`
	Status              string `json:"status"`
	// Ran 表示是否实际提交了运行（Run=false 时为 false）。
	Ran          bool   `json:"ran"`
	Success      bool   `json:"success"`
	ExitCode     int    `json:"exit_code"`
	Log          string `json:"log,omitempty"`
	LogPath      string `json:"log_path,omitempty"`
	WorkspaceDir string `json:"workspace_dir,omitempty"`
	CommandPath  string `json:"command_path,omitempty"`
}

// ContainerTerminalRunner 抽象「向当前用户激活项目下的容器模板临时终端写入命令」
// 的业务能力（terminal_exec）。
//
// 终端会话复用同一个持久 PTY，命令输出会实时出现在前端 xterm 中，用户也能看到
// Agent 执行了什么。
type ContainerTerminalRunner interface {
	// RunInTerminal 把 command 写入会话的串行队列。
	//
	// Wait=true 时等待命令结束并返回退出码与输出尾部；Wait=false 时只投递并立即返回
	// （Accepted=true，Running=true）。
	//
	// 命令结束（含超时 / 失败）后实现方必须「解绑」本次调用：
	// 一次性会话（Persistent=false）若已无前端终端连接会被立即回收，
	// 持久会话（Persistent=true）则保留，直到空闲 TTL 或显式关闭。
	RunInTerminal(ctx context.Context, in ContainerTerminalExecInput) (ContainerTerminalExecResult, error)
}

// ContainerTerminalExecInput 是一次 terminal_exec 调用的入参。
type ContainerTerminalExecInput struct {
	// UserID 取自执行事实（tool.UserIDFrom）。
	UserID int64
	// ProjectID 取自执行事实（tool.ProjectIDFrom，当前用户激活项目）。
	ProjectID int64
	// AgentSessionID 是发起本次调用的 Agent 会话 ID（tool.SessionIDFrom）。
	//
	// 终端会话按 (ContainerTemplateID, AgentSessionID) 唯一：同一 Agent 会话在同一个模板上
	// 多次执行命令会复用同一个终端会话（同一容器与 PTY）。必须 > 0。
	AgentSessionID int64
	// ContainerTemplateID 是必填的容器模板 ID。
	ContainerTemplateID int64
	// Command 是要在终端里执行的 shell 命令。
	Command string
	// WorkingDir 是命令的执行目录（宿主路径，由执行事实 tool.TaskFactsFrom 提供）。
	//
	// 非空时实现方把终端的创建工作目录直接设为该目录（容器 WorkDir / exec WorkingDir），
	// 而不是给每条命令拼 cd；路径按容器语义换算（Windows 宿主去掉盘符）。
	// 空串表示沿用终端默认目录。
	WorkingDir string
	// TimeoutSeconds <= 0 时使用实现默认超时。
	TimeoutSeconds int
	// Wait 为 true 时等待命令结束。
	Wait bool
	// CreateIfMissing 为 true 时允许自动创建终端会话（容器）。
	CreateIfMissing bool
	// Persistent 为 true 时把会话标记为持久：命令结束后不回收容器，
	// 直到空闲 TTL 到期或用户显式关闭。false（默认）= 一次性会话，
	// 命令结束后若没有前端终端连接则立即删除会话与容器。
	Persistent bool
}

// UIActionPusher 抽象「把一条 UI 动作推给当前用户的在线前端」的能力（ui_action）。
//
// 它只负责「投递」：动作语义（action 名 + payload 结构）由前端 ActionDispatcher 定义，
// 后端不解释 payload，因此新增动作（路由跳转 / 表单填充 / 提示消息 …）不需要改动本接口。
//
// 与 ContainerTerminalRunner 一样，它是进程级静态依赖，由 builtin.Deps 在构造工具时注入；
// 推送目标是哪个用户取自执行事实（tool.UserIDFrom），不由模型提供。
type UIActionPusher interface {
	// PushUIAction 向 userID 的在线客户端推送一条 {action, payload} 消息。
	//
	// userID <= 0（无登录用户）或当前无在线客户端时返回 nil：UI 动作是尽力而为的
	// 「显示层提示」，不应让工具调用失败。
	PushUIAction(ctx context.Context, userID int64, action string, payload map[string]any) error
}

// ContainerTerminalExecResult 是一次 terminal_exec 调用的执行结果。
type ContainerTerminalExecResult struct {
	SessionID       string  `json:"session_id,omitempty"`
	Accepted        bool    `json:"accepted"`
	Running         bool    `json:"running,omitempty"`
	ExitCode        int     `json:"exit_code"`
	TimedOut        bool    `json:"timed_out,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	Output          string  `json:"output,omitempty"`
	OutputTruncated bool    `json:"output_truncated,omitempty"`
}

// FileReader 抽象「按 go_file 主键解析文件路径」与「读取 Excel 工作簿预览」的能力。
//
// 它把「file_id → 真实路径」与「xlsx → 列名/预览行文本」的业务细节收敛在上层实现
// （见 container），tool 包只负责把它暴露给模型。
//
// 与 AnalysisNodeRunner 一样，它是进程级静态依赖，由 builtin.Deps / coder.Deps 在构造工具时注入。
type FileReader interface {
	// ReadFileByID 按 go_file 主键 ID 查询文件元信息（含真实路径）。
	//
	// fileID 非法（<=0）或文件不存在时返回错误。
	ReadFileByID(ctx context.Context, fileID int64) (FileReadResult, error)
	// ReadSpreadsheet 读取 Excel 工作簿（xlsx），返回可直接展示的文本预览：
	// 每个 sheet 的名称、列名与前若干行数据。
	ReadSpreadsheet(ctx context.Context, path string) (string, error)
}

// FileReadResult 是按 ID 查询文件的结果（只含文件元信息）。
type FileReadResult struct {
	FileID   int64  `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	FilePath string `json:"file_path"`
	Format   string `json:"format,omitempty"`
}

// ScriptCreator 抽象「创建脚本」的业务能力（create_script）。
//
// 职责刻意与节点创建拆开（脚本与节点是两个独立资源）：
//   - CreateScript 只落库 Script（pipeline_components）并初始化脚本目录
//     （主文件 / io_schema.json），不创建任何分析节点；
//   - 为脚本创建节点由 AnalysisNodeCreator 负责（见 create_analysis_node_script）。
//
// 这样模型可以先把脚本与 io_schema.json 写好，再决定要建几个节点；不需要节点的
// 场景（只写脚本）也不必凭空造一个节点。这些细节收敛在上层实现（见 container），
// tool 包只负责把能力暴露给模型。
//
// 与 AnalysisNodeRunner / FileReader 一样，它是进程级静态依赖，由 builtin.Deps 在构造工具时注入。
type ScriptCreator interface {
	// CreateScript 创建脚本并初始化脚本目录，返回脚本 ID 与主文件 / io_schema.json 路径。
	//
	// projectID 取自 RuntimeContext.ProjectID（当前用户激活项目），必须为正数。
	// input.ContainerTemplateID 为空（0）时由实现解析系统默认容器模板；
	// 没有配置默认模板时返回错误，提示用户先到容器模板页设置默认模板。
	CreateScript(ctx context.Context, input ScriptCreateInput) (ScriptCreateResult, error)
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
	// ScriptFilePath 是脚本主文件（如 main.R）的绝对路径，由 types.Script.Files 推导。
	ScriptFilePath string `json:"script_file_path,omitempty"`
	// IOSchemaPath 是脚本目录下 io_schema.json 的绝对路径（节点 patterns 的来源）。
	IOSchemaPath string         `json:"io_schema_path,omitempty"`
	WorkspaceDir string         `json:"workspace_dir"`
	ParamsPath   string         `json:"params_path"`
	RequestParam map[string]any `json:"request_param,omitempty"`
}

// AnalysisNodeCreator 抽象「为已有脚本创建独立分析节点」的业务能力
// （create_analysis_node_script）。
//
// 它属于分析节点域（而非脚本域）：脚本必须已存在且 io_schema.json 已写好，
// 创建时落地节点的 inputs_patterns / output_patterns 并准备好工作目录。
//
// 与 ScriptCreator 一样，它是进程级静态依赖，由 builtin.Deps 在构造工具时注入。
type AnalysisNodeCreator interface {
	// CreateAnalysisNode 为已有脚本创建一个独立分析节点。
	//
	// 脚本必须属于 input.ProjectID；request_param 非空时按脚本 io_schema 解析出
	// resolved_inputs 并写出 params.json，为空时节点只按 io_schema 初始化 patterns。
	//
	// 节点在创建时必然要落地 inputs_patterns / output_patterns，因此 io_schema.json
	// 必须已经写好（先 CreateScript，或至少已写入一份契约）；缺失时返回错误。
	CreateAnalysisNode(ctx context.Context, input AnalysisNodeCreateInput) (AnalysisNodeCreateResult, error)
}

// ScriptUpdater 抽象「更新已有脚本基本信息（name / description）」的业务能力。
//
// 它只覆盖脚本名称与描述这两个元数据字段，不触碰脚本内容（脚本主文件由模型直接读写）、
// io_schema 与容器模板，因此实现必须是「读改写」：先按 script_id 读出完整 Script 记录，
// 只覆盖请求中给出的字段，再整体回写，避免把未提交的字段写成零值
// （workflowRepository.UpdateScript 是整体替换语义）。
//
// 与 ScriptCreator 一样，它是进程级静态依赖，由 builtin.Deps 在构造工具时注入。
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
	// File 指向要编辑的脚本文件（script_file / io_schema / readme）。
	//
	// File 非空时按 ResourceFileEdit 的语义编辑该文件：Content != nil 为整体覆盖，
	// 否则按 OldString → NewString 做唯一替换；元数据与文件编辑可在同一次调用中完成。
	File      ResourceFile `json:"file,omitempty"`
	Content   *string      `json:"content,omitempty"`
	OldString string       `json:"old_string,omitempty"`
	NewString string       `json:"new_string,omitempty"`
}

// ResourceFileEdit 返回本次的文件编辑（未指定文件时 IsZero 为 true）。
func (in ScriptUpdateInput) ResourceFileEdit() ResourceFileEdit {
	return ResourceFileEdit{File: in.File, Content: in.Content, OldString: in.OldString, NewString: in.NewString}
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

// AnalysisNodeUpdater 抽象「更新已有分析节点」的业务能力。
//
// 按节点类型拆成两个入口（节点类型取自 analysis_nodes.creation_source，只有三种）：
//   - UpdateAgentAnalysis：更新 agent 节点（命令节点，无脚本），可改 node_name / command / container_template_id；
//   - UpdateScriptAnalysis：更新 script / scheduler 节点（绑定脚本），可改 node_name / request_param。
//
// 实现是「读改写」：先按 analysis_node_id 读出节点，只覆盖请求中给出的字段再回写；
// 命令 / 参数的变更还会重新生成 run.sh / params.json 等运行期文件。
//
// 与 ScriptUpdater 一样，它是进程级静态依赖，由 builtin.Deps 在构造工具时注入。
type AnalysisNodeUpdater interface {
	// UpdateAgentAnalysis 更新 agent 节点（命令节点，无脚本）。
	//
	// 可更新 node_name / command / container_template_id；未给出的字段保持原值。
	// command 变更后会重新生成 run.sh。节点必须属于 projectID，且节点类型必须为 agent。
	UpdateAgentAnalysis(ctx context.Context, input AgentAnalysisUpdateInput) (AgentAnalysisUpdateResult, error)

	// UpdateScriptAnalysis 更新 script / scheduler 节点（绑定脚本）。
	//
	// 可更新 node_name / request_param；request_param 非 nil 时按脚本最新 io_schema 重新解析
	// resolved_inputs 并重新生成 params.json（空对象表示清空参数）。
	// 节点必须属于 projectID，且节点必须绑定脚本（类型为 script 或 scheduler）。
	UpdateScriptAnalysis(ctx context.Context, input ScriptAnalysisUpdateInput) (ScriptAnalysisUpdateResult, error)
}

// AgentAnalysisUpdateInput 是「更新 agent 节点」的入参。
//
// 所有可选字段用指针表示：nil 表示不修改，非 nil 时按值覆盖。
type AgentAnalysisUpdateInput struct {
	// ProjectID 是节点所属项目（取自 RuntimeContext.ProjectID）。
	ProjectID int64 `json:"project_id,string"`
	// AnalysisNodeID 是分析节点主键，必须为正数。
	AnalysisNodeID int64 `json:"analysis_node_id,string"`
	// NodeName 为 nil 表示不修改；非 nil 时覆盖节点名称（不能为空）。
	NodeName *string `json:"node_name,omitempty"`
	// Command 为 nil 表示不修改；非 nil 时覆盖 run.sh 正文（不能为空）。
	Command *string `json:"command,omitempty"`
	// ContainerTemplateID 为 nil 表示不修改；非 nil 时覆盖节点容器模板（必须为正数）。
	ContainerTemplateID *int64 `json:"container_template_id,omitempty,string"`
	// File 固定为 command（run.sh）；非空时按 Content / OldString → NewString 编辑 run.sh。
	File      ResourceFile `json:"file,omitempty"`
	Content   *string      `json:"content,omitempty"`
	OldString string       `json:"old_string,omitempty"`
	NewString string       `json:"new_string,omitempty"`
}

// AgentAnalysisUpdateResult 是「更新 agent 节点」的结果（回传给模型）。
type AgentAnalysisUpdateResult struct {
	AnalysisNodeID      int64  `json:"analysis_node_id,string"`
	NodeName            string `json:"node_name"`
	Command             string `json:"command,omitempty"`
	ContainerTemplateID int64  `json:"container_template_id,string,omitempty"`
	WorkspaceDir        string `json:"workspace_dir,omitempty"`
	CommandPath         string `json:"command_path,omitempty"`
	ParamsPath          string `json:"params_path,omitempty"`
	// Changed 列出本次实际被修改的字段（node_name / command / container_template_id）。
	Changed []string `json:"changed,omitempty"`
}

// ScriptAnalysisUpdateInput 是「更新 script / scheduler 节点」的入参。
//
// 所有可选字段用指针 / nil map 表示：nil 表示不修改，非 nil 时按值覆盖。
// RequestParam 为「已提供但为空对象」时表示清空节点参数。
type ScriptAnalysisUpdateInput struct {
	// ProjectID 是节点所属项目（取自 RuntimeContext.ProjectID）。
	ProjectID int64 `json:"project_id,string"`
	// AnalysisNodeID 是分析节点主键，必须为正数。
	AnalysisNodeID int64 `json:"analysis_node_id,string"`
	// NodeName 为 nil 表示不修改；非 nil 时覆盖节点名称（不能为空）。
	NodeName *string `json:"node_name,omitempty"`
	// RequestParam 为 nil 表示不修改；非 nil 时按脚本 io_schema 重新解析并覆盖节点参数。
	RequestParam map[string]any `json:"request_param,omitempty"`
	// File 固定为 params（params.json）；非空时把编辑后的 params.json 解析为 RequestParam。
	File      ResourceFile `json:"file,omitempty"`
	Content   *string      `json:"content,omitempty"`
	OldString string       `json:"old_string,omitempty"`
	NewString string       `json:"new_string,omitempty"`
}

// ScriptAnalysisUpdateResult 是「更新 script / scheduler 节点」的结果（回传给模型）。
type ScriptAnalysisUpdateResult struct {
	AnalysisNodeID int64          `json:"analysis_node_id,string"`
	NodeName       string         `json:"node_name"`
	ScriptID       int64          `json:"script_id,string,omitempty"`
	WorkspaceDir   string         `json:"workspace_dir,omitempty"`
	ParamsPath     string         `json:"params_path,omitempty"`
	CommandPath    string         `json:"command_path,omitempty"`
	RequestParam   map[string]any `json:"request_param,omitempty"`
	// Warnings 是参数解析阶段的非致命提示（缺少必填输入 / 存在 io_schema 未声明的键）。
	Warnings []string `json:"warnings,omitempty"`
	// Changed 列出本次实际被修改的字段（node_name / request_param）。
	Changed []string `json:"changed,omitempty"`
}

// ScriptBrowser 抽象「浏览项目脚本」的只读能力（list_project_scripts / get_script）。
//
// 脚本按当前激活项目隔离，projectID 由调用方从执行事实（TaskFacts）取得。
type ScriptBrowser interface {
	// ListScriptsByProject 返回指定项目下的全部脚本（item_type/pipeline 中 component_type=script）。
	//
	// 结果按 id 倒序，不分页（项目内脚本数量有限）；projectID 必须为正数。
	ListScriptsByProject(ctx context.Context, projectID int64) ([]ScriptBrief, error)

	// GetScriptByID 按主键读取脚本（必须属于当前激活项目，否则返回错误）。
	//
	// projectID 取自执行事实，scriptID 是 pipeline_components 主键。
	// 返回的 ScriptDetail 除摘要外还附带 README.md 正文（文件不存在时为空）。
	GetScriptByID(ctx context.Context, projectID int64, scriptID int64) (ScriptDetail, error)

	// ReadScriptFile 直接读取脚本目录下指定文件的正文（script_file / io_schema / readme）。
	//
	// 工具层据此把文件内容直接回传给模型，不再需要 view / bash。
	ReadScriptFile(ctx context.Context, projectID int64, scriptID int64, file ResourceFile) (ResourceFileContent, error)
}

// AnalysisNodeBrowser 抽象「浏览项目分析节点」的只读能力
// （list_project_analysis_node / get_analysis_node 与更新类工具的预览）。
type AnalysisNodeBrowser interface {
	// ListAnalysisNodesByProject 返回指定项目下的分析节点（按 id 倒序，不分页）。
	//
	// filters 为空时返回全部节点：后端会先按过滤条件筛选，未命中任何分析运行时再按
	// 节点名 / 节点 ID / 脚本名关键字退化为「按名字搜索」；显式给出 filters.ScriptID
	// 时则始终按脚本返回（跨分析运行），便于按脚本汇总节点。
	// 每一项是 AnalysisNodeSummary（摘要 + 所属分析的执行事实 + 工作目录与产物路径），
	// 不含 run.sh / params.json 正文；需要正文时再用 GetAnalysisNodeByID / ReadAnalysisNodeFile。
	// projectID 必须为正数。
	ListAnalysisNodesByProject(ctx context.Context, projectID int64, filters AnalysisNodeListFilters) ([]AnalysisNodeSummary, error)

	// GetAnalysisNodeByID 按主键读取分析节点（必须属于当前激活项目，否则返回错误）。
	//
	// 返回的 WorkspaceDir 是绝对路径，ParamsPath 与 OutputDir 均由 WorkspaceDir 派生。
	GetAnalysisNodeByID(ctx context.Context, projectID int64, nodeID int64) (AnalysisNodeBrief, error)

	// ReadAnalysisNodeFile 直接读取节点工作目录下指定文件的正文（command / params）。
	ReadAnalysisNodeFile(ctx context.Context, projectID int64, nodeID int64, file ResourceFile) (ResourceFileContent, error)

	// ListAnalysisNodeFiles 返回节点由系统自动登记到 go_file 的产物文件。
	//
	// 这些记录不是用户手工添加的：节点运行成功且输出校验通过后，由系统按节点类型
	// 写入 go_file.analysis_node_id -> analysis_nodes.id（agent 命令节点来自
	// output/outputs.json，script / scheduler 节点来自 output_patterns 中 type=file 的条目）。
	// 节点没有产物时返回空切片而不是错误；projectID 必须为正数，节点必须属于当前激活项目。
	ListAnalysisNodeFiles(ctx context.Context, projectID int64, nodeID int64) ([]AnalysisNodeFile, error)
}

// AnalysisNodeListFilters 是「列出项目分析节点」的过滤条件（全部可选）。
//
// 关键字统一走 Keyword；其余字段用于把结果限定到某次分析运行 / 某个工作流 / 某个脚本，
// 或按节点状态筛选。空值表示该维度不过滤。
type AnalysisNodeListFilters struct {
	// Keyword 模糊匹配节点名称 / 节点 ID / 脚本名称。
	Keyword string
	// AnalysisID 是分析运行主键（nextflow.id）的字符串形式（避免大整数精度问题）。
	AnalysisID string
	// WorkflowID 是工作流主键（pipeline_components_relation.id）的字符串形式。
	WorkflowID string
	// ScriptID 是脚本主键（pipeline_components.id）的字符串形式；给出时按脚本返回全部节点。
	ScriptID string
	// Status 是节点状态（如 success / failed / running）。
	Status string
	// Limit 是返回条数上限（<=0 时取后端默认值，超出后端上限会被截断）。
	Limit int
}

// AnalysisNodeSummary 是分析节点列表项的只读摘要（list_project_analysis_node 回传）。
//
// 内嵌 AnalysisNodeBrief（节点自身名称 / 类型 / 工作目录 / 参数），再补充「它属于哪次
// 分析运行」的执行事实与运行状态，使模型无需再逐个 get_analysis_workflow 就能对账：
//   - AnalysisRunID / AnalysisKey / AnalysisName / JobStatus 来自 nextflow 行；
//   - NodeID 是 DAG 内的节点标识（analysis_nodes.node_id），SampleID 是样本标识；
//   - ScriptName 是按 script_id join 取到的脚本名，Status / StartedAt / FinishedAt
//     是节点的运行状态与起止时间。
type AnalysisNodeSummary struct {
	AnalysisNodeBrief
	// AnalysisNodeKey 是 analysis_nodes.analysis_node_id（对外 UUID），Agent 命令节点的
	// 节点 ID 与创建返回一致，便于与工具结果 / 用户引用对账。
	AnalysisNodeKey string `json:"analysis_node_key,omitempty"`
	NodeID          string `json:"node_id,omitempty"`
	SampleID        string `json:"sample_id,omitempty"`
	ScriptName      string `json:"script_name,omitempty"`

	// AnalysisRunID 是所属分析运行的主键（nextflow.id，字符串以保精度）。
	AnalysisRunID int64  `json:"analysis_run_id,string"`
	AnalysisKey   string `json:"analysis_key,omitempty"`
	AnalysisName  string `json:"analysis_name,omitempty"`
	JobStatus     string `json:"job_status,omitempty"`
	SchedulerMode string `json:"scheduler_mode,omitempty"`

	Status     string `json:"status,omitempty"`
	ExitCode   int    `json:"exit_code"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// WorkflowBrowser 抽象「按 ID 查看工作流」的只读能力（get_workflow）。
//
// 工作流按当前激活项目隔离，projectID 由调用方从执行事实（TaskFacts）取得。
type WorkflowBrowser interface {
	// ListWorkflowsByProject 返回指定项目下的全部工作流（按 id 倒序，不分页）。
	//
	// 每一项是 WorkflowBrief（元数据 + 目录内文件路径 + DAG 节点 / 连线数），
	// 不含 dag_definition / io_schema 正文；需要正文时再用 GetWorkflowByID。
	// projectID 必须为正数。
	ListWorkflowsByProject(ctx context.Context, projectID int64) ([]WorkflowBrief, error)

	// GetWorkflowByID 按主键读取工作流（必须属于当前激活项目，否则返回错误）。
	//
	// workflowID 是 pipeline_components_relation 主键（不是 relation_id UUID）。
	// 返回的 DagDefinition / IOSchema 是工作流目录下文件的原始 JSON 文本，
	// 目录内文件是唯一数据源（不落库），模型可直接据此编辑后再调用 update_workflow。
	GetWorkflowByID(ctx context.Context, projectID int64, workflowID int64) (WorkflowDetail, error)

	// ReadWorkflowFile 直接读取工作流目录下指定文件的正文
	// （dag_definition / io_schema / readme）。
	ReadWorkflowFile(ctx context.Context, projectID int64, workflowID int64, file ResourceFile) (ResourceFileContent, error)
}

// WorkflowCreator 抽象「创建工作流」的业务能力（create_workflow）。
//
// 与 ScriptCreator 一样，它是进程级静态依赖，由 builtin.Deps 在构造工具时注入。
type WorkflowCreator interface {
	// CreateWorkflow 新建一个工作流：落库 pipeline_components_relation 行，并初始化工作流目录
	// （dag_definition.json / io_schema.json / README.md 中实际给出的那几份）。
	//
	// projectID 取自执行事实（当前激活项目），必须为正数。
	CreateWorkflow(ctx context.Context, input WorkflowCreateInput) (WorkflowCreateResult, error)
}

// WorkflowUpdater 抽象「更新工作流」的业务能力（update_workflow）。
//
// 实现是「读改写」：先按 workflow_id 读出完整记录，只覆盖请求中给出的字段再整体回写
// （workflowRepository.UpdateWorkflow 是整体替换语义）。
type WorkflowUpdater interface {
	// UpdateWorkflow 更新工作流的元数据与 DAG 定义 / io_schema。
	//
	// 指针 / 非空字符串表示「修改该字段」，nil / 空串表示「保持原值」。
	// 工作流必须属于 projectID，否则返回错误。
	UpdateWorkflow(ctx context.Context, input WorkflowUpdateInput) (WorkflowUpdateResult, error)
}

// AnalysisBrowser 抽象「浏览分析运行」的只读能力
// （list_project_analysis_workflow / get_analysis_workflow）。
//
// 这里的 analysis 指 nextflow 表的一行（一次工作流运行），不是单个节点。
type AnalysisBrowser interface {
	// ListAnalysesByProject 返回指定项目下的分析运行（按 id 倒序，不分页）。
	//
	// filters 为空时返回全部；各字段之间为「与」关系。每一项是 AnalysisBrief
	// （名称 / 工作流 / 状态 / 调度器 / 缓存策略 / 工作目录 / 节点状态分布），
	// 不含 params.json 正文；需要正文或按 ID 详情时再用 GetAnalysisByID。
	// projectID 必须为正数。
	ListAnalysesByProject(ctx context.Context, projectID int64, filters AnalysisListFilters) ([]AnalysisBrief, error)

	// GetAnalysisByID 按主键读取分析运行（必须属于当前激活项目，否则返回错误）。
	//
	// analysisID 是 nextflow 主键（int64），不是 analysis_id UUID。
	GetAnalysisByID(ctx context.Context, projectID int64, analysisID int64) (AnalysisDetail, error)

	// ReadAnalysisFile 直接读取分析运行目录下指定文件的正文（params）。
	ReadAnalysisFile(ctx context.Context, projectID int64, analysisID int64, file ResourceFile) (ResourceFileContent, error)
}

// AnalysisListFilters 是「列出项目分析运行」的过滤条件（全部可选）。
//
// 各字段之间为「与」关系；空值表示该维度不过滤。ID 类字段用字符串承载，
// 避免大整数在模型侧丢失精度。
type AnalysisListFilters struct {
	// Keyword 模糊匹配分析名称 / analysis_id。
	Keyword string
	// WorkflowID 是工作流主键（pipeline_components_relation.id）的字符串形式。
	WorkflowID string
	// JobStatus 是运行状态（running / stopping / stopped / finished / failed 等）。
	JobStatus string
	// CacheType 是缓存策略（1 全量重跑，2 复用已有节点，3 脚本未变则复用，4 脚本与参数都未变才复用）。
	CacheType int
	// IsReport 非 nil 时按「是否为报告」过滤。
	IsReport *bool
	// Limit 是返回条数上限（<=0 时取后端默认值，超出后端上限会被截断）。
	Limit int
}

// AnalysisCreator 抽象「为工作流新建一次分析运行」的业务能力（create_analysis_workflow）。
//
// 创建与提交是两步：创建只落库 + 写 params.json（job_status=created），
// 提交运行由 AnalysisRunner 负责（可先建后跑，也可建完即跑）。
type AnalysisCreator interface {
	// CreateAnalysis 在当前激活项目下为指定工作流新建一次分析运行（不提交运行）。
	//
	// RequestParam 按工作流表单解析并落库（空 map 表示无参数）；AnalysisName 为空时
	// 使用工作流名称。工作流必须属于当前激活项目，否则返回错误。
	CreateAnalysis(ctx context.Context, input AnalysisCreateInput) (AnalysisCreateResult, error)
}

// AnalysisUpdater 抽象「更新分析运行」的业务能力（update_analysis_workflow）。
type AnalysisUpdater interface {
	// UpdateAnalysis 更新分析运行的分析名称 / 请求参数 / 缓存策略。
	//
	// RequestParam 非 nil 时按工作流表单重新解析，并重写分析目录下的 params.json
	// （空对象表示清空参数）。运行中的分析不允许修改，返回错误。
	UpdateAnalysis(ctx context.Context, input AnalysisUpdateInput) (AnalysisUpdateResult, error)
}

// AnalysisRunner 抽象「提交/重跑一次分析运行」的业务能力（run_analysis_workflow）。
type AnalysisRunner interface {
	// RunAnalysis 提交分析运行：按工作流 DAG 物化节点并交给所属调度器异步推进。
	//
	// 不等待分析结束（工作流运行是长任务），只返回提交是否成功与所属调度器。
	// 运行中的分析不允许重复提交，返回错误。
	RunAnalysis(ctx context.Context, input AnalysisRunInput) (AnalysisRunResult, error)
}

// WorkflowBrief 是工作流的只读摘要（get_workflow / create_workflow / update_workflow 共用）。
//
// WorkflowKey 是 relation_id（UUID，工作流目录名），WorkflowDir / DagDefinitionPath /
// IOSchemaPath / ReadmeFile 由 utils.WorkflowLayout 推导，模型可据此直接读写目录内文件。
type WorkflowBrief struct {
	WorkflowID  int64  `json:"workflow_id,string"`
	WorkflowKey string `json:"workflow_key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	ProjectID   int64  `json:"project_id,string"`

	WorkflowDir       string `json:"workflow_dir"`
	DagDefinitionPath string `json:"dag_definition_path"`
	IOSchemaPath      string `json:"io_schema_path"`
	ReadmeFile        string `json:"readme_file,omitempty"`

	// NodeCount / EdgeCount 是 DAG 定义中的节点数与连线数（无 DAG 时为 0）。
	NodeCount int `json:"node_count"`
	EdgeCount int `json:"edge_count"`
}

// WorkflowDetail 是单个工作流的只读详情（get_workflow 回传）。
//
// DagDefinition / IOSchema 是磁盘文件的原始 JSON 文本（唯一数据源，不落库）；
// Inputs 是 io_schema.inputs 解析后的表单（运行分析时的参数键）；
// Scripts 是 DAG 中引用到的脚本（按 script_id 去重）。
type WorkflowDetail struct {
	WorkflowBrief
	DagDefinition string                `json:"dag_definition,omitempty"`
	IOSchema      string                `json:"io_schema,omitempty"`
	Inputs        []any                 `json:"inputs,omitempty"`
	ReadmeContent string                `json:"readme_content,omitempty"`
	Scripts       []WorkflowScriptBrief `json:"scripts,omitempty"`
}

// WorkflowScriptBrief 是工作流 DAG 引用到的一个脚本。
type WorkflowScriptBrief struct {
	ScriptID   int64  `json:"script_id,string"`
	ScriptKey  string `json:"script_key"`
	ScriptName string `json:"script_name"`
}

// WorkflowCreateInput 是「创建工作流」的入参。
//
// DagDefinition / IOSchema 是 JSON 文本（可为空）：为空时不写对应文件。
// 合法 JSON 由实现校验，非法时返回错误且不落库。
type WorkflowCreateInput struct {
	ProjectID     int64  `json:"project_id,string"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Category      string `json:"category,omitempty"`
	DagDefinition string `json:"dag_definition,omitempty"`
	IOSchema      string `json:"io_schema,omitempty"`
	Readme        string `json:"readme,omitempty"`
}

// WorkflowCreateResult 是「创建工作流」的结果（回传给模型）。
type WorkflowCreateResult struct {
	WorkflowBrief
	Warnings []string `json:"warnings,omitempty"`
}

// WorkflowUpdateInput 是「更新工作流」的入参。
//
// 所有可选字段用指针表示：nil 表示不修改。DagDefinition / IOSchema 非 nil 时
// 覆盖磁盘文件（空串表示不写，保留原文件）。
type WorkflowUpdateInput struct {
	ProjectID     int64   `json:"project_id,string"`
	WorkflowID    int64   `json:"workflow_id,string"`
	Name          *string `json:"name,omitempty"`
	Description   *string `json:"description,omitempty"`
	Category      *string `json:"category,omitempty"`
	DagDefinition *string `json:"dag_definition,omitempty"`
	IOSchema      *string `json:"io_schema,omitempty"`
	// File 指向要编辑的工作流文件（dag_definition / io_schema / readme）。
	//
	// File 非空时按 ResourceFileEdit 的语义编辑该文件：Content != nil 为整体覆盖，
	// 否则按 OldString → NewString 做唯一替换。DagDefinition / IOSchema 是旧字段，
	// 等价于 File + Content（整体替换）。
	File      ResourceFile `json:"file,omitempty"`
	Content   *string      `json:"content,omitempty"`
	OldString string       `json:"old_string,omitempty"`
	NewString string       `json:"new_string,omitempty"`
}

// WorkflowUpdateResult 是「更新工作流」的结果（回传给模型）。
type WorkflowUpdateResult struct {
	WorkflowBrief
	Changed  []string `json:"changed,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// AnalysisBrief 是一次分析运行（nextflow 行）的只读摘要。
//
// AnalysisKey 是 analysis_id（UUID，对外展示用），AnalysisID 是 nextflow 主键；
// WorkspaceDir / ParamsPath 是分析根目录与本次运行的参数文件（绝对路径）。
type AnalysisBrief struct {
	AnalysisID   int64  `json:"analysis_id,string"`
	AnalysisKey  string `json:"analysis_key"`
	AnalysisName string `json:"analysis_name"`
	ProjectID    int64  `json:"project_id,string"`
	WorkflowID   int64  `json:"workflow_id,string"`
	WorkflowName string `json:"workflow_name,omitempty"`

	JobStatus     string `json:"job_status"`
	SchedulerMode string `json:"scheduler_mode"`
	CacheType     int    `json:"cache_type"`

	WorkspaceDir string         `json:"workspace_dir"`
	ParamsPath   string         `json:"params_path,omitempty"`
	RequestParam map[string]any `json:"request_param,omitempty"`

	NodeCount        int            `json:"node_count"`
	NodeStatusCounts map[string]int `json:"node_status_counts,omitempty"`
}

// AnalysisDetail 是单次分析运行的只读详情（get_analysis_workflow 回传）。
type AnalysisDetail struct {
	AnalysisBrief
}

// AnalysisCreateInput 是「为工作流新建一次分析运行」的入参。
//
// WorkflowID 是工作流主键（pipeline_components_relation.id）；AnalysisName 为空时
// 回退到工作流名称；RequestParam 为 nil / 空 map 时表示本次运行没有参数。
type AnalysisCreateInput struct {
	ProjectID    int64          `json:"project_id,string"`
	WorkflowID   int64          `json:"workflow_id,string"`
	AnalysisName string         `json:"analysis_name,omitempty"`
	RequestParam map[string]any `json:"request_param,omitempty"`
	CacheType    *int           `json:"cache_type,omitempty"`
}

// AnalysisCreateResult 是「为工作流新建一次分析运行」的结果（回传给模型）。
type AnalysisCreateResult struct {
	AnalysisBrief
}

// AnalysisUpdateInput 是「更新分析运行」的入参。
//
// AnalysisName / CacheType / RequestParam 为 nil 表示不修改。
type AnalysisUpdateInput struct {
	ProjectID    int64          `json:"project_id,string"`
	AnalysisID   int64          `json:"analysis_id,string"`
	AnalysisName *string        `json:"analysis_name,omitempty"`
	RequestParam map[string]any `json:"request_param,omitempty"`
	CacheType    *int           `json:"cache_type,omitempty"`
	// File 固定为 params（params.json）；非空时把编辑后的 params.json 解析为 RequestParam。
	File      ResourceFile `json:"file,omitempty"`
	Content   *string      `json:"content,omitempty"`
	OldString string       `json:"old_string,omitempty"`
	NewString string       `json:"new_string,omitempty"`
}

// AnalysisUpdateResult 是「更新分析运行」的结果（回传给模型）。
type AnalysisUpdateResult struct {
	AnalysisBrief
	Changed []string `json:"changed,omitempty"`
}

// AnalysisRunInput 是「提交分析运行」的入参。
//
// RequestParam / CacheType 为 nil 时沿用分析记录上已保存的值；
// 非 nil 时先按新值更新分析记录再提交运行。
type AnalysisRunInput struct {
	ProjectID    int64          `json:"project_id,string"`
	AnalysisID   int64          `json:"analysis_id,string"`
	RequestParam map[string]any `json:"request_param,omitempty"`
	CacheType    *int           `json:"cache_type,omitempty"`
}

// AnalysisRunResult 是「提交分析运行」的结果（回传给模型）。
type AnalysisRunResult struct {
	AnalysisID  int64  `json:"analysis_id,string"`
	AnalysisKey string `json:"analysis_key"`
	Scheduler   string `json:"scheduler"`
	// Started 为 true 表示已成功提交给调度器异步推进。
	Started   bool   `json:"started"`
	JobStatus string `json:"job_status,omitempty"`
}

// ContainerTemplateBrowser 抽象「浏览容器模板」的只读能力（list_container_templates / get_container_template）。
//
// 容器模板是全局资产（不属于某个项目），因此 ListContainerTemplates 不需要 projectID。
type ContainerTemplateBrowser interface {
	// ListContainerTemplates 返回全部可运行容器模板。
	//
	// 每一项对应一行 go_container_template_definition（ID 即对外的容器模板 ID），
	// Name / Description 取自该绑定行关联的 go_container_template_spec。
	ListContainerTemplates(ctx context.Context) ([]ContainerTemplateBrief, error)

	// GetContainerTemplateByID 按主键读取一个可运行容器模板（绑定行 + 绑定的共享运行配置）。
	GetContainerTemplateByID(ctx context.Context, containerTemplateID int64) (ContainerTemplateDetail, error)
}

// DatasetBrowser 抽象「按 ID 查看数据集」的只读能力（get_dataset）。
type DatasetBrowser interface {
	// GetDatasetByID 按主键读取数据集（必须属于当前激活项目，否则返回错误）。
	//
	// 用于解析用户输入里的 @dataset:<id> 引用：返回数据集名称 / 描述与其工作目录的
	// 绝对路径（utils.GetDatasetDir），模型可直接按该路径读取 / 写入数据集文件。
	GetDatasetByID(ctx context.Context, projectID int64, datasetID int64) (DatasetBrief, error)
}

// DatasetWriter 抽象「按 key 准备数据集 / 向数据集添加文件」的写能力
// （get_project_database_path / add_file_to_dataset）。
//
// 与 DatasetBrowser 分开：写操作需要创建数据集、登记文件，属于会改库 / 改盘的能力。
type DatasetWriter interface {
	// EnsureDatasetByKey 返回当前激活项目下 dataset_key 匹配的数据集；不存在时按
	// key 作为名称创建并绑定到项目。第二个返回值表示本次是否新建。
	EnsureDatasetByKey(ctx context.Context, projectID int64, datasetKey string) (DatasetBrief, bool, error)

	// AddFileToDataset 把 Path 指向的文件登记为 DatasetID 的数据集文件。
	//
	// 绝对路径必须在存储根目录（storage.base_dir）内；相对路径相对数据集工作目录
	// （utils.GetDatasetDir）解析。IsPrefix / IsCopy 语义与
	// types.AddFileToDatasetRequest 一致。
	AddFileToDataset(ctx context.Context, in DatasetFileAddInput) (DatasetFileAddResult, error)
}

// DatasetFileAddInput 是一次 add_file_to_dataset 调用的入参。
type DatasetFileAddInput struct {
	// ProjectID 取自执行事实（tool.ProjectIDFrom，当前用户激活项目）。
	ProjectID int64
	// DatasetID 是目标数据集主键（go_dataset）。
	DatasetID int64
	// Path 是要登记的文件路径：绝对路径或相对数据集工作目录的路径。
	Path string
	// Role 是文件角色（go_file.role），空时由实现回退到 DEFAULT。
	Role string
	// FileName 是展示文件名，空时由实现取 Path 的 basename。
	FileName string
	// IsPrefix 为 true 时把 Path 当作前缀（不校验文件是否存在）。
	IsPrefix bool
	// IsCopy 为 true 时把文件复制到数据集目录后再登记。
	IsCopy bool
}

// DatasetFileAddResult 是一次 add_file_to_dataset 调用的结果。
type DatasetFileAddResult struct {
	DatasetID int64  `json:"dataset_id"`
	FileID    int64  `json:"file_id"`
	FilePath  string `json:"file_path"`
	FileName  string `json:"file_name"`
	Role      string `json:"role,omitempty"`
	Format    string `json:"format,omitempty"`
	Size      int64  `json:"size,omitempty"`
}

// ProjectReportBrowser 抽象「按 ID 查看项目报告」的只读能力（get_report）。
//
// 本接口把报告正文（output.md）读回来，供模型回答「这份报告写了什么」或
// 在 update_report 前确认当前内容；撰写报告所需的分析证据由 list_session_resources
// 与会话内分析节点 / 分析运行的结果文件提供。
//
// 由 container 的 projectResourceBrowser 实现，并在构造 builtin.Deps 时注入。
type ProjectReportBrowser interface {
	// GetReportByID 读取报告元信息与正文内容。
	//
	// reportID 必须属于当前激活项目（projectID），否则返回错误；userID 用于校验报告
	// 所属项目已绑定该用户（越权时按 not found 处理）。正文文件不存在时 Content 为空串。
	GetReportByID(ctx context.Context, userID int64, projectID int64, reportID int64) (ProjectReportDetail, error)
}

// ProjectReportDetail 是单个项目报告的只读详情（get_report 回传）。
type ProjectReportDetail struct {
	// ReportID / ProjectID 以字符串形式回传，避免大整数在模型侧丢失精度。
	ReportID  int64  `json:"report_id,string"`
	ProjectID int64  `json:"project_id,string"`
	Title     string `json:"title"`
	// FilePath 是报告正文文件（报告工作目录下的 output.md）的绝对路径。
	FilePath string `json:"file_path"`
	// Content 是报告正文（output.md）的当前内容；文件不存在时为空串。
	Content string `json:"content"`
}

// ScriptBrief 是脚本的只读摘要（工具回传给模型的最小字段集）。
//
// ScriptFilePath / IOSchemaPath / ReadmeFile 由 types.Script.Files（utils.ScriptLayout）
// 推导，模型可直接按这三个路径读写脚本主文件、io_schema.json 与 README.md。
// 所有回传脚本摘要的工具（list_project_scripts / get_script）共用本类型，
// 避免各自维护一份同名字段。
type ScriptBrief struct {
	// ScriptID 以字符串形式回传，避免大整数在模型侧丢失精度。
	ScriptID int64 `json:"script_id,string"`
	// ContainerTemplateID 是脚本绑定的容器模板 ID（go_container_template_definition 主键，
	// 即对外 ContainerTemplate.ID），可直接用于 list_container_templates 的查询结果对账 /
	// 运行配置核对；0 表示脚本尚未绑定容器模板。同样以字符串形式回传以避免精度丢失。
	ContainerTemplateID int64  `json:"container_template_id,omitempty,string"`
	ScriptName          string `json:"script_name"`
	Description         string `json:"description,omitempty"`
	ScriptType          string `json:"script_type,omitempty"`
	ScriptFilePath      string `json:"script_file_path"`
	IOSchemaPath        string `json:"io_schema_path"`
	// ReadmeFile 是脚本目录下 README.md 的绝对路径（脚本说明文档）。
	ReadmeFile string `json:"readme_file,omitempty"`
}

// ScriptDetail 是单个脚本的只读详情（get_script 回传）。
//
// 与 ScriptBrief 分开：列表（list_project_scripts）只需要路径，按 ID 查看单个脚本时
// 才把 README.md 正文一并带回，避免列表把每个脚本的说明文档都塞进上下文。
// 内嵌 ScriptBrief 后 JSON 仍为扁平字段，与 ScriptBrief 的输出保持兼容。
type ScriptDetail struct {
	ScriptBrief
	// ReadmeContent 是 README.md 的正文内容；文件不存在时为空（不视为错误）。
	ReadmeContent string `json:"readme_content,omitempty"`
}

// AnalysisNodeFile 是分析节点产物在 go_file 中的登记记录（list_analysis_node_files 的 output_files）。
//
// 这些记录由系统在节点运行成功且输出校验通过后自动写入，不是用户手工添加：
//   - agent 命令节点：来自命令写出的 output/outputs.json，role 固定为 DEFAULT；
//   - script / scheduler 节点：来自 output_patterns 中 type=file 的条目，role 取条目声明。
type AnalysisNodeFile struct {
	// FileID 是 go_file 主键，可直接传给 get_file 读取正文。
	FileID   int64  `json:"file_id,string"`
	FileName string `json:"file_name,omitempty"`
	// Path 是产物文件的绝对路径。
	Path   string `json:"path"`
	Format string `json:"format,omitempty"`
	// Role 是产物在分析结果表单中发布的角色（缺省 DEFAULT）。
	Role string `json:"role,omitempty"`
	// FileKey 是登记键：agent 节点为 outputs.json 的 key，脚本节点为输出句柄名。
	FileKey     string `json:"file_key,omitempty"`
	Size        int64  `json:"size,omitempty"`
	MD5         string `json:"md5,omitempty"`
	Description string `json:"description,omitempty"`
	// CreatedAt 是登记时间（RFC3339）。
	CreatedAt string `json:"created_at,omitempty"`
}

// AnalysisNodeBrief 是分析节点的只读摘要（工具回传给模型的最小字段集）。
type AnalysisNodeBrief struct {
	AnalysisNodeID int64  `json:"analysis_node_id"`
	NodeName       string `json:"node_name"`
	// CreationSource 是落库的 creation_source 原值（agent / script / scheduler）。
	CreationSource string `json:"creation_source,omitempty"`
	// NodeType 是归一化后的节点类型（agent / script / scheduler）。
	NodeType string `json:"node_type,omitempty"`
	// NodeTypeDescription 是节点类型的中文说明（含可用的更新 / 运行工具指引）。
	NodeTypeDescription string `json:"node_type_description,omitempty"`
	ScriptID            int64  `json:"script_id,omitempty"`
	WorkspaceDir        string `json:"workspace_dir"`
	CommandPath         string `json:"command_path,omitempty"`
	ParamsPath          string `json:"params_path"`
	// OutputDir 是节点输出目录的绝对路径（WorkspaceDir/output），每次运行前会被清空。
	OutputDir    string         `json:"output_dir"`
	RequestParam map[string]any `json:"request_param,omitempty"`
}

// ContainerTemplateBrief 是容器模板的只读摘要（工具回传给模型的最小字段集）。
//
// DefinitionID 是 go_container_template_definition 主键（即对外 ContainerTemplate.ID，
// 也是 script.container_template_id / app_session.container_template_id 引用的值）；
// SpecID 是它关联的 go_container_template_spec 主键；Name / Description 取自该共享配置。
type ContainerTemplateBrief struct {
	DefinitionID int64  `json:"definition_id"`
	SpecID       int64  `json:"spec_id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
}

// ContainerTemplateDetail 是单个容器模板的只读详情（供 get_container_template 回传）。
//
// DisplayName / Description 原样取自绑定行 go_container_template_definition 自身
// （Description 为 longtext）；SpecName 取自关联的共享运行配置，便于模型区分
// 「模板展示名」与「运行配置名」。
type ContainerTemplateDetail struct {
	DefinitionID int64  `json:"definition_id"`
	SpecID       int64  `json:"spec_id"`
	DisplayName  string `json:"display_name"`
	Description  string `json:"description"`
	ImageName    string `json:"image_name,omitempty"`
	PullPolicy   string `json:"pull_policy,omitempty"`
	IsDefault    bool   `json:"is_default"`
	SpecName     string `json:"spec_name,omitempty"`
}

// DatasetBrief 是数据集的只读摘要（供 get_dataset 回传）。
//
// Path 是数据集工作目录的绝对路径（utils.GetDatasetDir），模型可直接在该目录下
// 读取 / 写入文件。
type DatasetBrief struct {
	DatasetID   int64  `json:"dataset_id"`
	DatasetName string `json:"dataset_name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
}

// ProjectReportWriter 抽象「创建 / 更新项目报告」的能力，供 create_report / update_report
// 工具使用。
//
// 与 SkillWriter 同构：Plan* 只校验入参并计算「写入计划」（不写库、不落盘），供工具在
// 授权前把报告正文文件（utils.GetProjectReportOutputFile，即报告目录下的 output.md）
// 与 unified diff 展示给用户；Apply 在授权通过后写库（t_project_report）并落盘正文文件。
//
// 由 container 的 projectReportWriter 实现，并在构造 builtin.Deps 时注入。
type ProjectReportWriter interface {
	// PlanCreate 校验入参并计算新建报告的写入计划（不写库）。
	PlanCreate(ctx context.Context, in ProjectReportCreateInput) (ProjectReportWritePlan, error)
	// PlanEdit 按 old_string → new_string 计算一次唯一文本替换的写入计划（不落盘）。
	PlanEdit(ctx context.Context, in ProjectReportEditInput) (ProjectReportWritePlan, error)
	// PlanUpdate 按结构化字段（title / content）计算写入计划（不落盘）。
	//
	// 与 PlanEdit 互补：调用方无需提供可精确匹配的原文，直接给新标题 / 新正文即可。
	PlanUpdate(ctx context.Context, in ProjectReportUpdateInput) (ProjectReportWritePlan, error)
	// Apply 按计划写库并落盘 output.md，返回保存后的报告信息。
	Apply(ctx context.Context, plan ProjectReportWritePlan) (ProjectReportWriteInfo, error)
}

// ProjectReportCreateInput 是 create_report 的入参。
type ProjectReportCreateInput struct {
	// UserID / ProjectID 取自执行事实（tool.UserIDFrom / tool.ProjectIDFrom）。
	UserID    int64
	ProjectID int64
	// Title 是报告标题（必填）。
	Title string
	// Content 是报告正文（写入 output.md）；为空时按 "# <title>" 生成。
	Content string
}

// ProjectReportEditInput 是 update_report 文本替换模式的入参。
type ProjectReportEditInput struct {
	UserID    int64
	ReportID  int64
	OldString string
	NewString string
}

// ProjectReportUpdateInput 是 update_report 字段覆盖模式的入参。
//
// 字段为空串表示「保持不变」：只改标题时不传 Content，反之亦然。
type ProjectReportUpdateInput struct {
	UserID   int64
	ReportID int64
	// Title 是新的报告标题（空串表示不变）。
	Title string
	// Content 是新的完整正文（空串表示不变）。
	Content string
}

// ProjectReportWritePlan 是一次报告写入的计划，工具据此生成授权前的 diff 预览。
type ProjectReportWritePlan struct {
	UserID    int64
	ReportID  int64
	ProjectID int64
	// Title 是写入后的报告标题。
	Title string
	// FilePath 是报告正文文件（output.md）的绝对路径。
	FilePath string
	// Before 是写入前正文（新建时为空）。
	Before string
	// After 是待写入的正文。
	After string
	// Create 为 true 表示本次是新建报告。
	Create bool
}

// ProjectReportWriteInfo 是报告写入完成后的结果。
type ProjectReportWriteInfo struct {
	ReportID  int64
	ProjectID int64
	Title     string
	FilePath  string
}

// SkillResolver 抽象「按名称解析技能」的能力，供技能工具（get_skill /
// load_skill_resource）在执行期读取技能指令与附属资源。
//
// 声明为接口而不是直接依赖 skill 包，是为了避免 tool ← skill 的循环引用：
// skill 包用它自己的 *Registry 实现本接口，并由 container 在构造 builtin.Deps 时注入。
type SkillResolver interface {
	// Get 按名称解析技能；不存在时 ok 为 false。
	Get(name string) (SkillResource, bool)
	// Names 返回全部可用技能名（用于错误提示）。
	Names() []string
}

// SkillResource 是技能工具执行期需要的最小技能视图。
type SkillResource interface {
	// SkillInfo 返回技能的元信息与指令正文（SKILL.md 内容）。
	SkillInfo() SkillInfo
	// SkillVersion 返回技能版本（manifest.json 优先，回退 frontmatter version）。
	SkillVersion() string
	// SkillResources 返回技能目录下的附属资源相对路径（已排序）。
	SkillResources() []string
	// ReadSkillResource 读取技能目录内的附属资源（相对路径），返回文件内容。
	ReadSkillResource(path string) (content string, err error)
	// ReadSkillSource 返回 SKILL.md 的磁盘原文（含 YAML frontmatter，未做模板渲染）。
	//
	// 供 get_skill 的 raw 模式使用：edit_skill 的 old_string 是在磁盘原文上做唯一匹配，
	// 模型必须拿到未渲染的原文才能精确命中（指令正文可能含 {{ var }} 且不含 frontmatter）。
	ReadSkillSource() (content string, err error)
}

// SkillInfo 是技能的最小元信息 + 指令正文。
type SkillInfo struct {
	Name         string
	Description  string
	SkillPath    string
	SkillFile    string
	Instructions string
	// DisableRender 为 true 时，技能文本（指令正文 / 附属资源）按原文返回，
	// 不做 pongo2 模板渲染（见 skill.Skill.DisableRender）。
	DisableRender bool
}

// SkillWriter 抽象「创建 / 编辑技能」的能力，供 create_skill / edit_skill 工具使用。
//
// 写能力拆成两步：Plan* 只校验并计算「写入计划」（不落盘），供工具在授权前把目标
// 文件与 unified diff 展示给用户；Apply 在授权通过后落盘并热更新注册表。
//
// 由 skill.Writer 实现，并由 container 在构造 builtin.Deps 时注入。
type SkillWriter interface {
	// PlanCreate 校验入参并计算新建技能的写入计划（不落盘）。
	PlanCreate(in SkillCreateInput) (SkillWritePlan, error)
	// PlanEdit 校验入参并计算编辑技能的写入计划（不落盘）。
	PlanEdit(name, oldString, newString string) (SkillWritePlan, error)
	// PlanUpdate 按结构化字段（description / instructions）计算写入计划（不落盘）。
	//
	// 与 PlanEdit 互补：调用方不需要提供可精确匹配的原文，适合长技能或含模板变量的技能。
	PlanUpdate(name string, in SkillUpdateInput) (SkillWritePlan, error)
	// Apply 按计划落盘并注册技能，返回保存后的技能信息。
	Apply(plan SkillWritePlan) (SkillInfo, error)
}

// SkillWritePlan 是一次技能写入的计划，工具据此生成授权前的 diff 预览。
type SkillWritePlan struct {
	// Name 是技能名（同时是技能目录名）。
	Name string
	// FilePath 是目标 SKILL.md 的绝对路径。
	FilePath string
	// Before 是写入前的 SKILL.md 原文（新建时为空）。
	Before string
	// After 是校验通过、待写入的 SKILL.md 原文。
	After string
	// Notice 是写入后需要提醒用户的事项（可为空）。
	//
	// 典型场景：agent.overwrite_builtin_skills=true（默认）时修改内置技能，
	// 该技能会在系统重启后被内置版本覆盖，需要提示用户如何保留修改。
	Notice string
}

// SkillCreateInput 是 create_skill 的入参。
type SkillCreateInput struct {
	// Name 是技能名，同时作为技能目录名（字母数字与连字符，不能与已有技能重名）。
	Name string
	// Description 是注入系统提示词的技能描述（必填）。
	Description string
	// Instructions 是 SKILL.md 的 markdown 正文（不含 frontmatter）。
	Instructions string
}

// SkillUpdateInput 是 edit_skill 结构化更新的入参。
//
// 字段为空串表示「保持不变」：只改 description 时不传 Instructions，反之亦然。
// 与 SkillCreateInput 对齐，调用方直接给新值，无需先从磁盘复制原文。
type SkillUpdateInput struct {
	// Description 是新的技能描述（空串表示不变）。
	Description string
	// Instructions 是 SKILL.md 的新正文（不含 frontmatter；空串表示不变）。
	Instructions string
}

// ProfilePromptWriter 抽象「修改当前 Agent Profile 的 SystemPrompt」的能力，
// 供 update_profile_prompt 工具使用。
//
// 与 SkillWriter 同构：PlanEdit 只读取并计算「写入计划」（不落库），供工具在授权前
// 把 unified diff 展示给用户；Apply 在授权通过后写库。
//
// 由 agentservice.ProfilePromptWriter 实现，并由 container 在构造 builtin.Deps 时注入。
type ProfilePromptWriter interface {
	// PlanEdit 按 Profile ID 计算一次唯一文本替换的写入计划（不落库）。
	PlanEdit(ctx context.Context, profileID int64, oldString, newString string) (ProfilePromptPlan, error)
	// Apply 按计划写回 Profile.SystemPrompt，返回保存后的 Profile 信息。
	Apply(ctx context.Context, plan ProfilePromptPlan) (ProfilePromptInfo, error)
}

// ProfilePromptPlan 是一次 Profile 系统提示词写入的计划，工具据此生成授权前的 diff 预览。
type ProfilePromptPlan struct {
	// ProfileID 是目标 Profile 的 ID。
	ProfileID int64
	// Name 是 Profile 展示名。
	Name string
	// FilePath 是 diff 预览中展示的路径（提示词真实存储在数据库中）。
	FilePath string
	// Before 是写入前的 SystemPrompt。
	Before string
	// After 是替换后的 SystemPrompt。
	After string
	// Notice 是写入后需要提醒用户的事项（可为空）。
	//
	// 典型场景：agent.overwrite_builtin_profile=true（默认）时修改内置 Profile，
	// 该修改会在系统重启后被内置定义覆盖，需要提示用户如何保留修改。
	Notice string
}

// ProfilePromptInfo 是 Profile 系统提示词写入完成后的结果。
type ProfilePromptInfo struct {
	ProfileID int64
	Name      string
	// Notice 与 ProfilePromptPlan.Notice 一致，由工具转告用户。
	Notice string
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
	// ScriptContent 是脚本主文件正文；为空时使用该脚本类型的默认模板。
	ScriptContent string `json:"script_content,omitempty"`
	// IOSchema 是 io_schema.json 原文；为空时由 Inputs / Outputs 生成。
	IOSchema string `json:"io_schema,omitempty"`
	// Readme 是 README.md 正文；为空时不写 README。
	Readme string `json:"readme,omitempty"`
}

// ScriptCreateResult 是「创建脚本」的结果（回传给模型）。
//
// 只包含脚本自身的产物路径：模型据此直接编辑脚本主文件与 io_schema.json，
// 之后再按需调用 create_analysis_node_script 创建节点。
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

// AnalysisNodeParamsBuilder 抽象「按脚本 io_schema 解析请求参数 → 落库并生成 params.json」
// 的业务能力。
//
// 模型依据 io_schema.json 拼出 request_param 后调用该能力：上层按脚本表单
// （analysisform.BuildParseAnalysisResult）把 request_param 解析成结构化配方，
// 用它刷新分析节点的 inputs_patterns / output_patterns / resolved_inputs /
// request_param，再通过 NodeRuntimePreparer.Prepare 把配方写成节点 params.json /
// run.sh / env.sh，使 run_analysis_node 能直接以这份参数运行脚本。
//
// 与 AnalysisNodeRunner / ScriptCreator 一样，它是进程级静态依赖，由 builtin.Deps 在构造工具时注入。
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
	AnalysisNodeID int64 `json:"analysis_node_id,string"`
	ScriptID       int64 `json:"script_id,string"`

	WorkspaceDir string `json:"workspace_dir"`
	ParamsPath   string `json:"params_path"`
	CommandPath  string `json:"command_path,omitempty"`
	// ParamsJSON 是写入 params.json 的实际内容（缩进后的 JSON 文本）。
	ParamsJSON string `json:"params_json"`
	// ResolvedInputs 是按脚本表单解析后的结构化输入（即写入 params.json 的主体）。
	ResolvedInputs map[string]any `json:"resolved_inputs,omitempty"`
	// Warnings 是解析过程中发现的非致命问题（如 io_schema 参数未被提供）。
	Warnings []string `json:"warnings,omitempty"`
}

// MemorySaver 抽象「创建 / 更新一条用户长期记忆」的业务能力。
//
// 它把记忆域（agent.Memory / MemoryManager）的持久化细节收敛在上层实现（见 container），
// tool 包只负责把能力暴露给模型：模型在遭遇可复现的错误后，把「下次如何绕开」的注意事项
// 写入记忆，后续调用会随背景注入出现在系统提示词里。
//
// 与 AnalysisNodeRunner / ScriptCreator 一样，它是进程级静态依赖，由 builtin.Deps 在构造工具时注入。
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
