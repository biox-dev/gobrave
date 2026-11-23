package types

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

type JSONMap map[string]any

func (m JSONMap) Value() (driver.Value, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	buf, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

func (m *JSONMap) Scan(value any) error {
	if m == nil {
		return fmt.Errorf("types.JSONMap: scan on nil pointer")
	}

	data, err := normalizeJSONScanInput(value)
	if err != nil {
		return err
	}
	if len(data) == 0 || string(data) == "null" {
		*m = JSONMap{}
		return nil
	}

	out := map[string]any{}
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*m = out
	return nil
}

func (m JSONMap) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(map[string]any(m))
}

type JSONSlice []any

func (s JSONSlice) Value() (driver.Value, error) {
	if s == nil {
		return []byte("[]"), nil
	}
	buf, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

func (s *JSONSlice) Scan(value any) error {
	if s == nil {
		return fmt.Errorf("types.JSONSlice: scan on nil pointer")
	}

	data, err := normalizeJSONScanInput(value)
	if err != nil {
		return err
	}
	if len(data) == 0 || string(data) == "null" {
		*s = JSONSlice{}
		return nil
	}

	out := make([]any, 0)
	if err := json.Unmarshal(data, &out); err == nil {
		*s = out
		return nil
	}

	// Backward compatibility: some historical rows stored object-shaped JSON
	// in columns now treated as array-like fields.
	obj := map[string]any{}
	if err := json.Unmarshal(data, &obj); err == nil {
		if len(obj) == 0 {
			*s = JSONSlice{}
			return nil
		}
		*s = JSONSlice{obj}
		return nil
	}

	return fmt.Errorf("types.JSONSlice: unsupported JSON payload: %s", string(data))
}

func (s JSONSlice) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]any(s))
}

func normalizeJSONScanInput(value any) ([]byte, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case []byte:
		return []byte(strings.TrimSpace(string(v))), nil
	case string:
		return []byte(strings.TrimSpace(v)), nil
	default:
		return nil, fmt.Errorf("unsupported JSON scan type %T", value)
	}
}

// AnalysisWorkflow maps to Python brave's nextflow table.
type AnalysisWorkflow struct {
	// ID uint `json:"id" gorm:"primaryKey;autoIncrement"`
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`
	// ProjectID   string `json:"project" gorm:"column:project;type:varchar(255)"`
	ProjectID   int64  `json:"project_id,string" gorm:"column:project_id;type:bigint"`
	AnalysisID  string `json:"analysis_id" gorm:"column:analysis_id;type:varchar(255)"`
	ComponentID string `json:"component_id" gorm:"column:component_id;type:varchar(255)"`
	// WorkflowID 是工作流主键（pipeline_components_relation.id），不再是 relation_id UUID。
	// 列名 workflow_id（历史列名为 relation_id，迁移见 migrations/2026-09-21-analysis-workflow-id）。
	WorkflowID int64 `json:"workflow_id,string" gorm:"column:workflow_id;type:bigint;index:idx_nextflow_workflow_id"`
	// AnalysisType        string    `json:"analysis_type" gorm:"column:analysis_type;type:varchar(255)"`
	AnalysisName   string `json:"analysis_name" gorm:"column:analysis_name;type:varchar(255)"`
	InputFile      string `json:"input_file" gorm:"column:input_file;type:varchar(255)"`
	AnalysisMethod string `json:"analysis_method" gorm:"column:analysis_method;type:varchar(255)"`
	// WorkspaceDir 是本次分析的根目录。落库时相对 storage.base_dir 存储，
	// 读取时由 repository 还原成绝对路径，因此 base_dir 变更只需拷贝目录，
	// 无需回写数据库。其余路径全部由它派生（见下方 gorm:"-" 字段）。
	WorkspaceDir        string `json:"workspace_dir" gorm:"column:workspace_dir;type:varchar(255)"`
	RequestParam        string `json:"request_param" gorm:"column:request_param;type:longtext"`
	OutputFormat        string `json:"output_format" gorm:"column:output_format;type:longtext"`
	PipelineScript      string `json:"pipeline_script" gorm:"column:pipeline_script;type:varchar(255)"`
	ParseAnalysisModule string `json:"parse_analysis_module" gorm:"column:parse_analysis_module;type:varchar(255)"`
	ProcessID           string `json:"process_id" gorm:"column:process_id;type:varchar(255)"`
	ScriptConfigFile    string `json:"script_config_file" gorm:"column:script_config_file;type:varchar(255)"`
	JobID               string `json:"job_id" gorm:"column:job_id;type:varchar(255)"`
	Ports               string `json:"ports" gorm:"column:ports;type:varchar(255)"`
	URL                 string `json:"url" gorm:"column:url;type:varchar(255)"`
	JobStatus           string `json:"job_status" gorm:"column:job_status;type:varchar(255)"`
	// ServerStatus        string `json:"server_status" gorm:"column:server_status;type:varchar(255)"`
	// SchedulerMode 记录推进本次分析的调度器，用于进程重启后把分析交回正确的调度器恢复。
	// 历史数据该列为空，按 legacy DAG 调度器处理（见 NormalizeSchedulerMode）。
	SchedulerMode    string    `json:"scheduler_mode" gorm:"column:scheduler_mode;type:varchar(64);default:''"`
	IsReport         bool      `json:"is_report" gorm:"column:is_report;default:false"`
	CacheType        int       `json:"cache_type" gorm:"column:cache_type;default:1"`
	Used             bool      `json:"used" gorm:"column:used;default:true"`
	DataComponentIDs string    `json:"data_component_ids" gorm:"column:data_component_ids;type:text"`
	ExtraProjectIDs  string    `json:"extra_project_ids" gorm:"column:extra_project_ids;type:longtext"`
	CreatedAt        time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt        time.Time `json:"updated_at" gorm:"column:updated_at"`
}

// layout 返回本次分析在磁盘上的布局。
//
// 派生路径不再是结构体字段：唯一的输入是 WorkspaceDir，磁盘布局只在
// utils.AnalysisLayoutFor 定义一处，调用方通过下方方法取值，避免字段在构造后
// 忘记 hydrate 而读到空路径。
func (t *AnalysisWorkflow) layout() utils.AnalysisLayout {
	if t == nil {
		return utils.AnalysisLayout{}
	}
	return utils.AnalysisLayoutFor(t.WorkspaceDir)
}

// ParamsFile 返回分析参数文件（<WorkspaceDir>/params.json）。
func (t *AnalysisWorkflow) ParamsFile() string { return t.layout().ParamsPath }

// CommandFile 返回分析主脚本（<WorkspaceDir>/run.sh）。
func (t *AnalysisWorkflow) CommandFile() string { return t.layout().CommandPath }

// CommandLogFile 返回分析命令日志（<WorkspaceDir>/run.log）。
func (t *AnalysisWorkflow) CommandLogFile() string { return t.layout().CommandLogPath }

// TraceFile 返回调度轨迹日志（<WorkspaceDir>/trace.log）。
func (t *AnalysisWorkflow) TraceFile() string { return t.layout().TraceFile }

// WorkflowLogFile 返回工作流日志（<WorkspaceDir>/workflow.log）。
func (t *AnalysisWorkflow) WorkflowLogFile() string { return t.layout().WorkflowLogFile }

// ExecutorLogFile 返回执行器日志（<WorkspaceDir>/.nextflow.log）。
func (t *AnalysisWorkflow) ExecutorLogFile() string { return t.layout().ExecutorLogFile }

// MarshalJSON 在序列化时补齐派生路径字段。
//
// 这些路径已不再是结构体字段（只由 WorkspaceDir 推导），但前端与既有接口契约
// 仍按 params_path / command_path / ... 的 JSON key 读取，因此序列化时按原 key
// 输出，响应形状与重构前保持一致。
func (t AnalysisWorkflow) MarshalJSON() ([]byte, error) {
	type alias AnalysisWorkflow
	return json.Marshal(struct {
		alias
		ParamsPath      string `json:"params_path"`
		CommandPath     string `json:"command_path"`
		CommandLogPath  string `json:"command_log_path"`
		TraceFile       string `json:"trace_file"`
		WorkflowLogFile string `json:"workflow_log_file"`
		ExecutorLogFile string `json:"executor_log_file"`
	}{
		alias:           alias(t),
		ParamsPath:      t.ParamsFile(),
		CommandPath:     t.CommandFile(),
		CommandLogPath:  t.CommandLogFile(),
		TraceFile:       t.TraceFile(),
		WorkflowLogFile: t.WorkflowLogFile(),
		ExecutorLogFile: t.ExecutorLogFile(),
	})
}

func (t *AnalysisWorkflow) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

// CompileParams 返回编译期使用的参数：解析后的 request_param + analysis_name。
//
// request_param 只保存分析参数，analysis_name 是独立的列；编译期为实例命名
// （见 compiler 的 expand 阶段）需要它，因此在这里合并，而不写回 request_param。
func (t *AnalysisWorkflow) CompileParams() map[string]any {
	params := map[string]any{}
	if t == nil {
		return params
	}
	if strings.TrimSpace(t.RequestParam) != "" {
		// 解析失败时按空 map 处理，保持调度不中断。
		if err := json.Unmarshal([]byte(t.RequestParam), &params); err != nil {
			params = map[string]any{}
		}
	}
	if name := strings.TrimSpace(t.AnalysisName); name != "" {
		params["analysis_name"] = name
	}
	return params
}

const (
	CacheTypeRerunAll                          = 1
	CacheTypeReuseExistingNode                 = 2
	CacheTypeReuseWhenScriptUnchanged          = 3
	CacheTypeReuseWhenScriptAndParamsUnchanged = 4
)

// analysis.job_status 取值。所有调度器（legacy dag_v1 / node_v1 / dynamic_v2 /
// dataflow_v3）共用同一套状态字符串，避免各 orchestrator 各自维护一份常量，
// 导致统一恢复入口在判断 running / stopping 时出现不一致。
const (
	// AnalysisStatusRunning 表示分析已被某个调度器接管并正在推进，是恢复扫描的入口状态。
	AnalysisStatusRunning = "running"
	// AnalysisStatusStopping 表示已收到停止请求，等待所属调度器收敛到终态。
	AnalysisStatusStopping = "stopping"
	// AnalysisStatusStopped 是用户停止后的终态。
	AnalysisStatusStopped = "stopped"
	// AnalysisStatusFinished 是正常跑完的终态。
	AnalysisStatusFinished = "finished"
	// AnalysisStatusFailed 是异常终止的终态。
	AnalysisStatusFailed = "failed"
)

// analysis.scheduler_mode 取值，标识推进该分析的调度器，同时是
// dag/scheduler.Registry 的查找键。名称直接对应具体实现，不再带 V1/V2/V3 后缀。
const (
	// SchedulerModeDag 是静态图调度器（internal/dag/scheduler/dag）。
	// 历史空值 / dag_v1 / node_v1 都归一到此。
	SchedulerModeDag = "dag"
	// SchedulerModeDynamic 是动态物化调度器（internal/dag/scheduler/dynamic）。
	SchedulerModeDynamic = "dynamic"
	// SchedulerModeDataflow 是数据流调度器（internal/dag/scheduler/dataflow）。
	SchedulerModeDataflow = "dataflow"
)

// NormalizeSchedulerMode 把持久化的 scheduler_mode 归一化为当前规范名。
//
// 历史值（空值 / dag_v1 / dynamic_v2 / dataflow_v3 / node_v1）会被折叠到当前
// 名称，保证旧数据仍能解析到正确的调度器；未知值一律回退到默认的静态图调度器。
func NormalizeSchedulerMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case SchedulerModeDynamic, "dynamic_v2":
		return SchedulerModeDynamic
	case SchedulerModeDataflow, "dataflow_v3":
		return SchedulerModeDataflow
	default:
		// 空值、dag、dag_v1、node_v1 以及任何未知值都视为默认静态图调度器。
		return SchedulerModeDag
	}
}

func (AnalysisWorkflow) TableName() string {
	return "nextflow"
}

// AnalysisNode maps to Python brave's analysis_nodes table.
type AnalysisNode struct {
	// ID                     uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	AnalysisNodeID string `json:"analysis_node_id" gorm:"column:analysis_node_id;type:varchar(255)"`
	// ProjectID      string `json:"project_id" gorm:"column:project_id;type:varchar(255);index:idx_analysis_nodes_project_id"`
	ProjectID int64 `json:"project_id,string" gorm:"column:project_id;type:bigint"`
	// AnalysisID string `json:"analysis_id" gorm:"column:analysis_id;type:varchar(255);index:idx_analysis_nodes_analysis_id;index:idx_analysis_nodes_analysis_id_status,priority:1;index:idx_analysis_nodes_analysis_id_node_id,priority:1"`
	AnalysisID int64  `json:"analysis_id,string" gorm:"column:analysis_id;type:bigint"`
	NodeID     string `json:"node_id" gorm:"column:node_id;type:varchar(255);index:idx_analysis_nodes_analysis_id_node_id,priority:2"`
	NodeName   string `json:"node_name" gorm:"column:node_name;type:varchar(255)"`
	SampleID   string `json:"sample_id" gorm:"column:sample_id;type:varchar(255)"`
	// ScriptID       string `json:"script_id" gorm:"column:script_id;type:varchar(255)"`
	ScriptID int64 `json:"script_id,string" gorm:"column:script_id;type:bigint"`
	// ContainerTemplateID 是节点级容器模板（go_container_template_definition 主键）。
	//
	// 由 Agent 创建的「命令节点」没有脚本，直接用它启动容器；由脚本创建的节点保持 0，
	// 沿用脚本绑定的 container_template_id（见 dag/executor.ContainerExecutor）。
	ContainerTemplateID int64 `json:"container_template_id,string" gorm:"column:container_template_id;type:bigint"`
	// ScriptName 不是 analysis_nodes 的列，而是按 ScriptID join pipeline_components
	// 取到的脚本名称（component_name 列）。只有显式 join 的查询才会填充，
	// 因此用 gorm:"-" 排除在 schema 之外，避免其他查询误选该列。
	ScriptName             string    `json:"script_name" gorm:"-"`
	InputsPatterns         JSONMap   `json:"inputs_patterns" gorm:"column:inputs_patterns;type:json"`
	ResolvedInputs         JSONMap   `json:"resolved_inputs" gorm:"column:resolved_inputs;type:json"`
	OutputPatterns         JSONMap   `json:"output_patterns" gorm:"column:output_patterns;type:json"`
	ResolvedOutputs        JSONMap   `json:"resolved_outputs" gorm:"column:resolved_outputs;type:json"`
	OutputFileIDs          JSONMap   `json:"output_file_ids" gorm:"column:output_file_ids;type:json"` // 输出句柄 -> go_file id，供下游 SelectAssay 输入还原为 {"files": "<id>"}
	RequestParam           string    `json:"request_param" gorm:"column:request_param;type:longtext"`
	CPU                    int       `json:"cpu" gorm:"column:cpu"`
	Memory                 string    `json:"memory" gorm:"column:memory;type:varchar(64)"`
	Disk                   string    `json:"disk" gorm:"column:disk;type:varchar(64)"`
	GPU                    int       `json:"gpu" gorm:"column:gpu"`
	Status                 string    `json:"status" gorm:"column:status;type:varchar(64);index:idx_analysis_nodes_analysis_id_status,priority:2"`
	ServerStatus           string    `json:"server_status" gorm:"column:server_status;type:varchar(64)"`
	PID                    int       `json:"pid" gorm:"column:pid"`
	JobID                  string    `json:"job_id" gorm:"column:job_id;type:varchar(255)"`
	Executor               string    `json:"executor" gorm:"column:executor;type:varchar(64)"`
	Retry                  int       `json:"retry" gorm:"column:retry;default:0"`
	MaxRetry               int       `json:"max_retry" gorm:"column:max_retry;default:3"`
	ExitCode               int       `json:"exit_code" gorm:"column:exit_code"`
	ErrorMessage           string    `json:"error_message" gorm:"column:error_message;type:text"`
	RerunReason            string    `json:"rerun_reason" gorm:"column:rerun_reason;type:text"`
	InputHash              string    `json:"input_hash" gorm:"column:input_hash;type:varchar(255)"`
	CacheHit               bool      `json:"cache_hit" gorm:"column:cache_hit"`
	UpstreamIDs            JSONSlice `json:"upstream_ids" gorm:"column:upstream_ids;type:json"`
	DownstreamIDs          JSONSlice `json:"downstream_ids" gorm:"column:downstream_ids;type:json"`
	InputValidationErrors  JSONSlice `json:"input_validation_errors" gorm:"column:input_validation_errors;type:json"`
	OutputValidationErrors JSONSlice `json:"output_validation_errors" gorm:"column:output_validation_errors;type:json"`
	// WorkspaceDir 是节点根目录。落库时相对 storage.base_dir 存储，
	// 读取时由 repository 还原成绝对路径。其余路径全部由它派生。
	WorkspaceDir   string     `json:"workspace_dir" gorm:"column:workspace_dir;type:varchar(255)"`
	CommandMD5     string     `json:"command_md5" gorm:"column:command_md5;type:varchar(64)"`
	ParamsMD5      string     `json:"params_md5" gorm:"column:params_md5;type:varchar(64)"`
	CreationSource string     `json:"creation_source" gorm:"column:creation_source;type:varchar(32)"`
	StartedAt      *time.Time `json:"started_at" gorm:"column:started_at"`
	FinishedAt     *time.Time `json:"finished_at" gorm:"column:finished_at"`
	CreatedAt      time.Time  `json:"created_at" gorm:"column:created_at"`
	UpdatedAt      time.Time  `json:"updated_at" gorm:"column:updated_at"`
}

// layout 返回节点在磁盘上的布局。
//
// 派生路径不再是结构体字段：唯一的输入是 WorkspaceDir，磁盘布局只在
// utils.NodeLayoutFor 定义一处，调用方通过下方方法取值，避免字段在构造后
// 忘记 hydrate 而读到空路径。
func (t *AnalysisNode) layout() utils.NodeLayout {
	if t == nil {
		return utils.NodeLayout{}
	}
	return utils.NodeLayoutFor(t.WorkspaceDir)
}

// AnalysisNodeOutputsFileName 是节点把解析后的输出写回的文件名，位于节点输出目录下。
const AnalysisNodeOutputsFileName = "outputs.json"

// OutputDirectory 返回节点写出产物的目录（<WorkspaceDir>/output）。
// WorkspaceDir 为空时返回空串，避免产生悬空的路径。
func (t *AnalysisNode) OutputDirectory() string { return t.layout().OutputDir }

// CacheDirectory 返回节点中间缓存目录（<WorkspaceDir>/cached）。
func (t *AnalysisNode) CacheDirectory() string { return t.layout().CacheDir }

// ParamsFile 返回节点参数文件（<WorkspaceDir>/params.json）。
func (t *AnalysisNode) ParamsFile() string { return t.layout().ParamsPath }

// CommandFile 返回节点运行脚本（<WorkspaceDir>/run.sh）。
func (t *AnalysisNode) CommandFile() string { return t.layout().CommandPath }

// LogFile 返回节点命令日志（<WorkspaceDir>/command.log）。
func (t *AnalysisNode) LogFile() string { return t.layout().LogPath }

// OutputsFile 返回节点 outputs.json 的路径（OutputDirectory 下的 outputs.json）。
//
// 节点完成时由 dag 的 nodeOutputResolver 读取；路径推导只保留这一处，调用方不再
// 各自拼 filepath.Join(dir, "outputs.json")。无法推导输出目录时返回空串。
func (t *AnalysisNode) OutputsFile() string {
	dir := t.OutputDirectory()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, AnalysisNodeOutputsFileName)
}

// AnalysisNodeOutputMarkdownFileName 是节点输出目录下的分析报告文件名。
const AnalysisNodeOutputMarkdownFileName = "output.md"

// OutputMarkdownFile 返回节点分析报告（OutputDirectory 下的 output.md）的路径。
//
// 与 OutputsFile 同一口径：路径只由 OutputDirectory 推导，调用方不再各自拼
// filepath.Join(dir, "output.md")；无法推导输出目录时返回空串。
func (t *AnalysisNode) OutputMarkdownFile() string {
	dir := t.OutputDirectory()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, AnalysisNodeOutputMarkdownFileName)
}

// MarshalJSON 在序列化时补齐派生路径字段。
//
// 这些路径已不再是结构体字段（只由 WorkspaceDir 推导），但前端与既有接口契约
// 仍按 log_path / output_dir / cache_dir / command_path / params_path 的 JSON key
// 读取，因此序列化时按原 key 输出，响应形状与重构前保持一致。
func (t AnalysisNode) MarshalJSON() ([]byte, error) {
	type alias AnalysisNode
	return json.Marshal(struct {
		alias
		LogPath     string `json:"log_path"`
		OutputDir   string `json:"output_dir"`
		CacheDir    string `json:"cache_dir"`
		CommandPath string `json:"command_path"`
		ParamsPath  string `json:"params_path"`
	}{
		alias:       alias(t),
		LogPath:     t.LogFile(),
		OutputDir:   t.OutputDirectory(),
		CacheDir:    t.CacheDirectory(),
		CommandPath: t.CommandFile(),
		ParamsPath:  t.ParamsFile(),
	})
}

// IsCommandNode 报告该节点是否为 Agent 创建的命令节点。
//
// 命令节点没有脚本：它的容器模板由节点自身承载（ContainerTemplateID），run.sh 由
// 创建 / 更新入口直接写盘（见 prepare.WriteCommandRunScript），因此运行期跳过脚本
// 相关的解析与软链。
func (t *AnalysisNode) IsCommandNode() bool {
	return t != nil && t.ScriptID <= 0 && t.ContainerTemplateID > 0
}

// analysis_nodes.creation_source 取值：节点类型只有这三种。
//
// 取值在节点创建时由创建方写入，之后不再变化：
//   - agent：Agent 会话直接创建的命令型节点（无脚本，run.sh 由 Agent 生成）；
//   - script：由脚本契约创建的节点（前端独立节点 / create_analysis_node_script）；
//   - scheduler：由 DAG 调度器按流程物化的节点（绑定脚本）。
const (
	// AnalysisNodeCreationSourceAgent 是 Agent 命令节点（无脚本）。
	AnalysisNodeCreationSourceAgent = "agent"
	// AnalysisNodeCreationSourceScript 是脚本节点（绑定脚本，参数按 io_schema 解析）。
	AnalysisNodeCreationSourceScript = "script"
	// AnalysisNodeCreationSourceScheduler 是 DAG 调度器物化的节点（绑定脚本）。
	AnalysisNodeCreationSourceScheduler = "scheduler"
)

// NodeType 返回节点的归一化类型：agent / script / scheduler。
//
// 优先读取 creation_source（三种规范值）；同时兼容历史取值：
//   - standalone（前端独立节点）视为 script；
//   - agent_session（Agent 命令节点）视为 agent；
//   - 旧 create_analysis_node_script 曾把脚本节点标记为 agent，这里按「绑定脚本」纠正为 script。
//
// creation_source 为空或未知时按节点形状推断；仍无法判定时返回空串。
func (t *AnalysisNode) NodeType() string {
	if t == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(t.CreationSource)) {
	case AnalysisNodeCreationSourceScheduler:
		return AnalysisNodeCreationSourceScheduler
	case AnalysisNodeCreationSourceScript, "standalone":
		return AnalysisNodeCreationSourceScript
	case AnalysisNodeCreationSourceAgent, "agent_session":
		// 历史数据：脚本节点曾被标记为 agent，按形状归一为 script。
		if t.ScriptID > 0 {
			return AnalysisNodeCreationSourceScript
		}
		return AnalysisNodeCreationSourceAgent
	}

	// creation_source 缺失或未知：按节点形状推断。
	if t.IsCommandNode() {
		return AnalysisNodeCreationSourceAgent
	}
	if t.ScriptID > 0 {
		return AnalysisNodeCreationSourceScript
	}
	return ""
}

// NodeTypeDescription 返回节点类型的中文说明，便于工具结果直接回给模型。
func (t *AnalysisNode) NodeTypeDescription() string {
	switch t.NodeType() {
	case AnalysisNodeCreationSourceAgent:
		return "Agent 命令节点（无脚本，run.sh 由 Agent 生成）"
	case AnalysisNodeCreationSourceScript:
		return "脚本节点（绑定脚本，参数按脚本 io_schema 解析）"
	case AnalysisNodeCreationSourceScheduler:
		return "调度器节点（由 DAG 调度器按流程物化，绑定脚本）"
	default:
		return "未知节点类型"
	}
}

func (t *AnalysisNode) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}
func (AnalysisNode) TableName() string {
	return "analysis_nodes"
}

// AnalysisEdge maps to Python brave's analysis_edges table.
type AnalysisEdge struct {
	ID             uint   `json:"id" gorm:"primaryKey;autoIncrement"`
	AnalysisEdgeID string `json:"analysis_edge_id" gorm:"column:analysis_edge_id;type:varchar(255)"`
	// AnalysisID     string `json:"analysis_id" gorm:"column:analysis_id;type:varchar(255);index:idx_analysis_edges_analysis_id"`
	AnalysisID int64 `json:"analysis_id,string" gorm:"column:analysis_id;type:bigint"`

	SourceNode   string    `json:"source_node" gorm:"column:source_node;type:varchar(255)"`
	TargetNode   string    `json:"target_node" gorm:"column:target_node;type:varchar(255)"`
	SourceHandle string    `json:"source_handle" gorm:"column:source_handle;type:varchar(255)"`
	TargetHandle string    `json:"target_handle" gorm:"column:target_handle;type:varchar(255)"`
	CreatedAt    time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt    time.Time `json:"updated_at" gorm:"column:updated_at"`
}

func (AnalysisEdge) TableName() string {
	return "analysis_edges"
}

// AnalysisControllerSaveInput 是保存一次分析的入参。
//
// RequestParam 只承载分析参数；分析主键、工作流、名称、缓存策略等元数据
// 由以下显式字段承载，不再混进 RequestParam，保证落库的 request_param 纯净。
type AnalysisControllerSaveInput struct {
	RequestParam        map[string]any
	ParseAnalysisResult map[string]any
	DagRuntime          map[string]any
	Project             *Project
	IsRunNode           bool
	IsReport            bool
	// AnalysisID 为空时由服务层生成新主键。
	AnalysisID int64
	// WorkflowID 是工作流主键（pipeline_components_relation.id）。
	WorkflowID int64
	// NodeName 是分析名称，落库到 nextflow.analysis_name。
	NodeName string
	// CacheType 是缓存策略，落库到 nextflow.cache_type。
	CacheType int
	// SchedulerMode 由各控制入口显式声明，标识谁来推进这次分析。
	// 为空时保持库中原值不变（更新路径）或按 legacy 处理（新建路径）。
	SchedulerMode string
}
