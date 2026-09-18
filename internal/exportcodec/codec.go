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
	WriteScriptFiles(ctx context.Context, req ScriptWriteRequest) (*types.ScriptJSONExportResponse, error)

	// WriteWorkflowFiles 按该版本格式生成工作流导出内容并落盘到 req.WorkflowDir，
	// 同时按该版本的目录布局把工作流引用的脚本快照到 req.WorkflowDir 下，
	// 最后把目录改动提交为一个 git commit，返回写入的 payload，其 Version 必须等于 Version()。
	//
	// 实现需保证 req.WorkflowDir 存在（不存在则创建）并完成 git 提交
	// （见 utils.CommitDirChanges，提交身份由装配时注入）。
	WriteWorkflowFiles(ctx context.Context, req WorkflowWriteRequest) (*types.WorkflowJSONExportResponse, error)

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
