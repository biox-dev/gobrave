package runtimecontext

import "context"

// 本文件定义「组件仓库（store）」相关端口，供 builtin 的
// page_store / publish_workflow / publish_script / install_workflow / install_script 使用。
//
// store 是一个裸 git 仓库目录（storage.base_dir/store/<path_name>）：本地发布把
// 脚本 / 工作流目录 push 进去，远程下载则 clone 上游仓库；安装时从 store 同步回
// 项目目录并按导出文件（script.json / workflow.json）落库。
//
// 三个能力按工具消费者拆成窄接口：
//
//   - StoreBrowser            —— 分页查询 store（page_store）；
//   - StoreComponentPublisher —— 把本地脚本 / 工作流发布到 store（publish_workflow / publish_script）；
//   - StoreComponentInstaller —— 从 store 安装脚本 / 工作流（install_workflow / install_script）。
//
// 由 container 的 storeTools 实现，并在构造 builtin.Deps 时注入。

// StorePageInput 是分页查询 store 的入参。
//
// StoreType 只会取 workflow / script：store 表按组件类型分区，两类组件的安装语义不同
// （工作流安装会连带还原脚本快照），因此查询必须显式指定其一。
type StorePageInput struct {
	StoreType string `json:"store_type"`
	Keywords  string `json:"keywords,omitempty"`
	Page      int    `json:"page,omitempty"`
	PageSize  int    `json:"page_size,omitempty"`
	SortBy    string `json:"sort_by,omitempty"`
	SortOrder string `json:"sort_order,omitempty"`
}

// StoreItem 是分页结果里的单条 store 摘要。
//
// Description 是 store 仓库 HEAD 提交里 workflow.json / script.json 记录的 description
// （只读推导、不落库，读不到时为空串）。
// Installed / InstalledID 描述「当前激活项目是否已安装该 store」：
//   - workflow：Installed 为 true 即项目内已有同一 store 的工作流，InstalledID 是 workflow 主键；
//   - script：InstalledID 是已安装脚本的主键（0 表示未安装）。
type StoreItem struct {
	StoreID     int64    `json:"store_id,string"`
	StoreType   string   `json:"store_type"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Origin      string   `json:"origin,omitempty"`
	Category    string   `json:"category,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	PathName    string   `json:"path_name,omitempty"`
	StorePath   string   `json:"store_path,omitempty"`
	Installed   bool     `json:"installed"`
	InstalledID string   `json:"installed_id,omitempty"`
}

// StorePageResult 是 page_store 的分页结果。
type StorePageResult struct {
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
	Stores   []StoreItem `json:"stores"`
}

// StoreBrowser 抽象「分页查询 store」的只读能力。
//
// userID 用于解析当前激活项目，进而回填 installed / installed_id。
type StoreBrowser interface {
	PageStore(ctx context.Context, userID int64, in StorePageInput) (StorePageResult, error)
}

// StorePublishWorkflowInput 是发布工作流到 store 的入参。
//
// ProjectID 取自执行事实（当前激活项目），用于校验工作流归属；
// WorkflowID 是 workflow 表主键（int64），不是 workflow.json 里的 relation_id。
type StorePublishWorkflowInput struct {
	ProjectID  int64
	WorkflowID int64
	Message    string
}

// StorePublishScriptInput 是发布脚本到 store 的入参。
//
// ProjectID 取自执行事实（当前激活项目），用于校验脚本归属；
// ScriptID 是 script 表主键（int64），不是脚本目录名。
type StorePublishScriptInput struct {
	ProjectID int64
	ScriptID  int64
	Message   string
}

// StorePublishResult 是发布的结果。
//
// ComponentID 是组件业务标识（workflow_id / script_id，即目录名），便于后续在 store 中定位；
// UpToDate 为 true 表示本次 push 没有新提交（幂等成功，与 handler 的 "already up-to-date" 一致）。
type StorePublishResult struct {
	StoreID     int64  `json:"store_id,string"`
	StoreType   string `json:"store_type"`
	StoreName   string `json:"store_name"`
	PathName    string `json:"path_name"`
	StorePath   string `json:"store_path"`
	Pushed      bool   `json:"pushed"`
	UpToDate    bool   `json:"up_to_date"`
	ComponentID string `json:"component_id,omitempty"`
}

// StoreComponentPublisher 抽象「把本地组件发布到 store」的写能力。
//
// 发布 = 生成导出文件并提交（script.json / workflow.json + 脚本快照）→ push 到
// store 裸仓库；store 记录（store 表）在首次发布时创建，之后沿用原 PathName。
type StoreComponentPublisher interface {
	PublishWorkflow(ctx context.Context, in StorePublishWorkflowInput) (StorePublishResult, error)
	PublishScript(ctx context.Context, in StorePublishScriptInput) (StorePublishResult, error)
}

// StoreInstallWorkflowInput 是从 store 安装工作流的入参。
//
// ProjectID 是安装到的目标项目（project 表主键）；StoreID 是 store 表主键。
type StoreInstallWorkflowInput struct {
	ProjectID int64
	StoreID   int64
}

// StoreInstallScriptInput 是从 store 安装脚本的入参。
//
// CreateMode 为 true 时作为副本新增（生成新的 script_id），否则按导出文件里的
// script_id 在目标项目内更新或新增。
type StoreInstallScriptInput struct {
	ProjectID  int64
	StoreID    int64
	CreateMode bool
}

// StoreInstallResult 是安装的结果。
//
// ComponentID 是导出文件里的业务标识（workflow_id / script_id）；
// InstalledID 是落库后的主键（workflow / script 表 id）。
type StoreInstallResult struct {
	StoreID                          int64  `json:"store_id,string"`
	StorePath                        string `json:"store_path"`
	TargetDir                        string `json:"target_dir"`
	ComponentID                      string `json:"component_id"`
	InstalledID                      int64  `json:"installed_id,string"`
	InstalledScriptCount             int    `json:"installed_script_count,omitempty"`
	ContainerTemplateSpecCount       int    `json:"container_template_spec_count,omitempty"`
	ContainerTemplateDefinitionCount int    `json:"container_template_definition_count,omitempty"`
}

// StoreComponentInstaller 抽象「从 store 安装组件」的写能力。
type StoreComponentInstaller interface {
	InstallWorkflow(ctx context.Context, in StoreInstallWorkflowInput) (StoreInstallResult, error)
	InstallScript(ctx context.Context, in StoreInstallScriptInput) (StoreInstallResult, error)
}
