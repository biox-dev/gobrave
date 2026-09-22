// Package exportcodec 定义「导出文件格式版本」的策略（Codec）与注册表（Registry）。
//
// 背景：SaveScript/SaveWorkflow 会把数据库记录导出为 script.json / workflow.json
// 落盘并提交 git，PublishScript/PublishWorkflow 再把它推送到 store 裸仓库；
// InstallScript/InstallWorkflow 则从 store 同步回目录后反向读取这两个文件导入数据库。
//
// 一旦文件格式演进（字段增删、脚本快照的目录布局变化），新旧产物必须能被区分处理，
// 因此导出文件顶层新增 version 字段标识文件格式版本：
//
//   - 写侧：调用方从 Registry 取当前版本（CurrentVersion）的 Codec，由 Codec 决定文件内容
//     与目录布局，生成的 payload 顶层 Version 必须等于 Codec.Version()；
//   - 读侧（InstallScript/InstallWorkflow）：调用方先用 PeekVersion 读出文件里的 version，
//     从 Registry 取对应 Codec，再用 DecodeScript/DecodeWorkflow 解析元数据，
//     并用同一 Codec 解释该版本的目录布局（如脚本快照目录）；
//     脚本文件本身仍随 git 从 store 同步/还原；
//   - 新增版本 = 新增一个 Codec 实现 + 在 internal/container/container.go 多 Register 一行，
//     调用方代码不变；
//   - Codec 实现必须无状态且并发安全：Registry 在启动期装配，之后只读。
package exportcodec

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
)

// 导出文件格式版本（写入 script.json / workflow.json 顶层 version 字段的取值）。
const (
	// VersionV1 是第一版格式：
	//   - script.json   = script_id + script + container_templates + container_images；
	//   - workflow.json = workflow_id + workflow + scripts + container_templates + container_images；
	//     （container_templates / container_images 均为按主键去重后的列表，模板通过 image_id 引用镜像）
	//   - workflow 目录同时把引用的脚本目录快照到 <workflowDir>/script/<scriptID>
	//     （排除脚本目录自身的 .git）。
	VersionV1 = "v1"

	// CurrentVersion 是写侧使用的版本：写侧总是从 Registry 取这个版本的 Codec 落盘，
	// 因此装配时（internal/container/container.go）必须注册该版本。
	CurrentVersion = VersionV1
)

// 导出文件名：写侧落盘（Codec.WriteXxxFiles）与读侧读取（InstallXxx）共用，
// 避免各版本实现各自定义导致读写不一致。
const (
	// ScriptJSONFileName 是脚本导出文件名。
	ScriptJSONFileName = "script.json"
	// WorkflowJSONFileName 是工作流导出文件名。
	WorkflowJSONFileName = "workflow.json"
)

// 安装侧（InstallScript / InstallWorkflow）在解析导出内容后发现关键引用键缺失时返回的错误。
//
// 调用方（handler）据此转 400：这类问题来自导出文件内容本身，属于请求错误，
// 不是服务端故障（与 ErrUnsupportedVersion 同一处理路径）。
var (
	// ErrScriptIDRequired 表示 script.json 里缺少 script_id。
	ErrScriptIDRequired = errors.New("script_id is required in script.json")
	// ErrWorkflowIDRequired 表示 workflow.json 里缺少 workflow_id。
	ErrWorkflowIDRequired = errors.New("workflow_id is required in workflow.json")
)

// Codec 是一套导出文件格式版本对应的策略实现。
//
// Codec 实现直接持有 interfaces.WorkflowService（写侧用它按主键生成导出 payload），
// 由 DI 容器在装配 Registry 时注入（见 internal/container/container.go）。
//
// WriteXxxFiles 负责「按该版本的格式生成文件、落盘，并把目录改动提交为一个 git commit」；
// DecodeXxx / ScriptSnapshotDir 只负责解析内容与解释目录布局。
// git 提交本身与格式版本无关，由 utils.CommitDirChanges 统一实现，各版本 Codec 直接复用，
// 因此调用方（handler）落盘后无需再单独提交。
//
// 方法分成三组，分别对应两个方向的调用方与目录布局：
//
//   - 写侧：SaveScript/SaveWorkflow/PublishScript/PublishWorkflow；
//   - 读侧：InstallScript/InstallWorkflow；
//   - 布局：安装侧要按该版本约定找到 workflow 目录里的脚本快照。
type Codec interface {
	// Version 返回该策略对应的文件格式版本，等于产物顶层 version 字段的取值，
	// 同时是它在 Registry 里的键（Registry.Get(Version()) 必须命中自己）。
	//
	// 同一个版本号必须既能写出、也能读回，因此 WriteXxxFiles 生成的 payload
	// 的 Version 字段必须由 Version() 赋值。
	Version() string

	// ===== 写侧 =====

	// WriteScriptFiles 按该版本格式生成脚本导出内容并落盘到 req.ScriptDir
	// （文件名、是否附带 container_templates 等由版本决定），把目录改动提交为一个
	// git commit，返回写入的 payload，其 Version 必须等于 Version()。
	//
	// 实现需保证 req.ScriptDir 存在（不存在则创建）并完成 git 提交
	// （见 utils.CommitDirChanges，提交身份由装配时注入）。
	WriteCommitScriptFiles(ctx context.Context, req ScriptWriteRequest) (*types.ScriptJSONExportResponse, error)

	// WriteWorkflowFiles 按该版本格式生成工作流导出内容并落盘到 req.WorkflowDir，
	// 同时按该版本的目录布局把工作流引用的脚本快照到 req.WorkflowDir 下，
	// 最后把目录改动提交为一个 git commit，返回写入的 payload，其 Version 必须等于 Version()。
	//
	// 实现需保证 req.WorkflowDir 存在（不存在则创建）并完成 git 提交
	// （见 utils.CommitDirChanges，提交身份由装配时注入）。
	WriteCommmitWorkflowFiles(ctx context.Context, req WorkflowWriteRequest) (*types.WorkflowJSONExportResponse, error)

	// ===== 读侧 =====

	// DecodeScript 解析 script.json 的内容（raw 为文件字节），返回带 Version 的 payload。
	//
	// 只做解析与该版本特有的字段规整，不负责业务字段覆盖
	// （ID/ProjectID/StoreID/URL/Message/创建更新时间由 handler 决定）。
	// ScriptID 非空等校验同样由 handler 负责，便于统一转 400。
	DecodeScript(raw []byte) (*types.ScriptJSONExportResponse, error)

	// DecodeWorkflow 解析 workflow.json 的内容（raw 为文件字节），返回带 Version 的 payload。
	//
	// 该方法的职责边界与 DecodeScript 一致；v1 会在这里把 workflow.dag_definition
	// 从对象规整成字符串（等价原先 handler 内的 normalizeInstalledWorkflowMap），
	// 因为 dag_definition 落库是字符串列。
	DecodeWorkflow(raw []byte) (*types.WorkflowJSONExportResponse, error)

	// ===== 安装侧 =====

	// InstallScript 解析 req.Raw（内部调用本 Codec 的 DecodeScript）并把导出内容持久化：
	// 先按主键 upsert 容器镜像 / 运行配置 / 绑定行，再按 script_id upsert script 行。
	//
	// 与 DecodeScript 的分工：DecodeScript 只负责解析，本方法负责落库与安装特有的校验
	// （script.json 缺少 script_id 时返回 ErrScriptIDRequired，调用方据此转 400）。
	InstallScript(ctx context.Context, req ScriptInstallRequest) (*ScriptInstallResult, error)

	// InstallWorkflow 解析 req.Raw（内部调用本 Codec 的 DecodeWorkflow）并把导出内容持久化：
	// 先按主键 upsert 容器资产，再 upsert workflow 行与它引用的 script 行，
	// 最后把 workflow 目录里的脚本快照（见 ScriptSnapshotDir）还原到本地脚本目录。
	//
	// workflow.json 缺少 workflow_id 时返回 ErrWorkflowIDRequired，调用方据此转 400。
	InstallWorkflow(ctx context.Context, req WorkflowInstallRequest) (*WorkflowInstallResult, error)

	// ===== 目录布局 =====

	// ScriptSnapshotDir 返回该版本布局下 workflow 目录内承载某个脚本快照的目录。
	//
	// 写侧（WriteWorkflowFiles）把脚本快照进去，安装侧（InstallWorkflow）从同一位置
	// 还原到脚本目录，两边都通过本方法取路径，避免各自写死字面量。
	ScriptSnapshotDir(workflowDir, scriptID string) string

	// ScriptIDFromExportScript 从 workflow.json 的脚本条目里取脚本 ID（component_id）。
	//
	// 条目在 payload 里是 map（不是强类型），字段名属于该版本的格式知识，
	// 因此由 Codec 解释；取不到时返回空字符串，由调用方决定是跳过还是报错。
	ScriptIDFromExportScript(item map[string]any) string
}

// ScriptWriteRequest 是写脚本导出文件的入参。
//
// ScriptPK 是 script 表主键（int64，不是 script_id），ScriptDir 是脚本目录绝对路径；
// CommitMessage 是落盘后 git 提交使用的 message（为空时使用默认文案）。
type ScriptWriteRequest struct {
	ScriptPK      int64
	ScriptDir     string
	CommitMessage string
}

// WorkflowWriteRequest 是写工作流导出文件的入参。
//
// WorkflowPK 是 workflow 表主键（int64，不是 relation_id）；
// ProjectID 是 project.project_id（字符串，注意与 workflow.ProjectID int64 外键区分）；
// BaseDir 是 storage.base_dir；WorkflowDir 是 workflow 目录绝对路径；
// CommitMessage 是落盘后 git 提交使用的 message（为空时使用默认文案）。
type WorkflowWriteRequest struct {
	WorkflowPK    int64
	ProjectID     string
	BaseDir       string
	WorkflowDir   string
	CommitMessage string
}

// ScriptInstallRequest 是安装脚本（InstallScript）的入参。
//
// Raw 是 script.json 的文件内容，与 DecodeScript 的入参一致；InstallScript 内部会调用
// DecodeScript 解析它，因此调用方只需把文件原样读进来，不需要（也不应）再自行解析。
//
// 其余字段是「落库上下文」：导出文件只描述组件内容，安装到哪个 project、来自哪个 store
// 以及安装成什么 script_id 由调用方（handler）决定。
//
// 字段语义与 handler 原实现保持一致：
//   - ProjectID 是 project 表主键（写入 script.project_id），不是 project.project_id；
//   - StoreURL / StoreMessage 非空时覆盖 script 行的 url / message；
//   - CreateMode 为 true 时作为副本新增（component_name 追加 _Copy 后缀、store_id 置 0），
//     否则按 ScriptID 在目标 project 内存在与否决定更新或新增。
//
// 目录相关说明：脚本安装不涉及文件系统还原（脚本文件已随 git 同步），故不含 BaseDir。
//
// 由导出文件内容推导的脚本文件仍由 handler 用 git 从 store 同步/还原（与格式版本无关）。
type ScriptInstallRequest struct {
	// Raw 是 script.json 文件内容（与 DecodeScript 的入参一致）。
	Raw []byte
	// ProjectID 是安装到的目标 project 主键（project.ID）。
	ProjectID int64
	// StoreID 是发布来源 store 主键（写入 script.store_id）。
	StoreID int64
	// StoreURL 非空时覆盖 script.url。
	StoreURL string
	// StoreMessage 非空时覆盖 script.message。
	StoreMessage string
	// ScriptID 是安装后的 script_id：create=true 时调用方已生成新 uuid，
	// 否则沿用导出文件/发布方记录的 script_id。
	ScriptID string
	// CreateMode 为 true 时作为副本新增，而不是按 script_id upsert 原脚本。
	CreateMode bool
}

// ScriptInstallResult 是 InstallScript 的结果。
type ScriptInstallResult struct {
	// ScriptID 是落库后的 script_id；InstalledScriptID 是 script 表主键。
	ScriptID          string
	InstalledScriptID int64
	// ContainerImageCount / ContainerTemplateSpecCount / ContainerTemplateDefinitionCount
	// 分别是本次按主键 upsert 的镜像、运行配置与绑定行数量（导出文件没有对应列表时为 0）。
	ContainerImageCount              int
	ContainerTemplateSpecCount       int
	ContainerTemplateDefinitionCount int
}

// WorkflowInstallRequest 是安装工作流（InstallWorkflow）的入参。
//
// Raw 是 workflow.json 的文件内容，与 DecodeWorkflow 的入参一致；InstallWorkflow 内部会
// 调用 DecodeWorkflow 解析它。其余字段语义与 ScriptInstallRequest 一致，额外多出：
//   - ProjectCode 是 project.project_id（字符串），用于推导本地目录布局
//     （utils.GetScriptFileDir），注意与 ProjectID（int64 主键）区分；
//   - BaseDir 是 storage.base_dir；
//   - WorkflowDir 是已从 store 同步出的 workflow 目录，脚本快照位于
//     <WorkflowDir>/script/<scriptID>（ScriptSnapshotDir），安装时还原到脚本目录。
type WorkflowInstallRequest struct {
	// Raw 是 workflow.json 文件内容（与 DecodeWorkflow 的入参一致）。
	Raw []byte
	// ProjectID 是安装到的目标 project 主键（project.ID）。
	ProjectID int64
	// ProjectCode 是 project.project_id（字符串），仅用于推导本地目录。
	ProjectCode string
	// StoreID 是发布来源 store 主键。
	StoreID int64
	// StoreURL 非空时覆盖 workflow / script 行的 url。
	StoreURL string
	// StoreMessage 非空时覆盖 workflow / script 行的 message。
	StoreMessage string
	// BaseDir 是 storage.base_dir。
	BaseDir string
	// WorkflowDir 是已同步出的 workflow 目录（脚本快照的父目录）。
	WorkflowDir string
}

// WorkflowInstallResult 是 InstallWorkflow 的结果。
type WorkflowInstallResult struct {
	// WorkflowID 是导出文件里的 workflow_id；InstalledWorkflowID 是 workflow 表主键。
	WorkflowID          string
	InstalledWorkflowID int64
	// InstalledScriptCount 是本次安装（新增或更新）的 script 行数。
	InstalledScriptCount int
	// ContainerImageCount / ContainerTemplateSpecCount / ContainerTemplateDefinitionCount
	// 分别是本次按主键 upsert 的镜像、运行配置与绑定行数量。
	ContainerImageCount              int
	ContainerTemplateSpecCount       int
	ContainerTemplateDefinitionCount int
}

// ScriptMaterializeRequest 曾用于按版本布局还原脚本文件；经确认安装侧的脚本文件
// 全部由 git 同步/还原，且缺失文件的补齐是 SaveScript 的职责，故该方法已删除。
// 若将来某个版本需要额外的落盘步骤，再按需新增该版本专用的方法。

// fileVersion 只承载导出文件顶层的 version 字段。
type fileVersion struct {
	Version string `json:"version"`
}

// PeekVersion 只解析导出文件（script.json / workflow.json）顶层的 version 字段，
// 其余字段一律忽略，供调用方在从 Registry 取 Codec 之前先窥探文件版本。
//
// version 缺失或为空时返回空字符串（是否接受该文件由调用方决定，例如按 CurrentVersion 读回，
// 或直接判为不支持）；raw 不是合法 JSON 时返回解析错误。
func PeekVersion(raw []byte) (string, error) {
	var probe fileVersion
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", err
	}
	return strings.TrimSpace(probe.Version), nil
}
