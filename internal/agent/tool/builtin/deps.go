package builtin

import (
	"github.com/biox-dev/gobrave/internal/agent/native"
	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
)

// Deps 是内置工具集的进程级静态依赖。
//
// 这些能力在进程启动时由 container 一次性装配（见 buildAgentClient），随工具构造函数
// 注入，而不是在每次调用时塞进 context：工具依赖因此对编译器可见、可单测，也不再需要
// 为每个能力维护一套 With/From。
// 与业务相关的动态事实（RuntimeContext / TaskFacts / 权限）仍然走工具执行 ctx，
// 因为它们随请求变化、且 RuntimeContext 会随 agent_tasks.request 序列化落库。
//
// 字段按工具消费者拆成窄接口（见 runtimecontext 包），容器用一个实现即可同时满足；
// 测试 fake 只需实现被用到的那一个。
type Deps struct {
	// Runner 运行分析节点（run_analysis_node / create_analysis_node_script / update_*）。
	Runner runtimecontext.AnalysisNodeRunner
	// AgentRunner 创建并（可选）运行命令型分析节点（create_analysis_node_agent）。
	AgentRunner runtimecontext.AgentAnalysisRunner
	// Terminal 向用户的持久终端会话写入命令（terminal_exec）。
	Terminal runtimecontext.ContainerTerminalRunner
	// UIActions 向当前用户的在线前端推送 UI 动作（ui_action）。
	UIActions runtimecontext.UIActionPusher
	// FileReader 按 go_file 主键解析文件路径并读取 Excel 工作簿预览（get_file）。
	FileReader runtimecontext.FileReader
	// ScriptCreator 创建脚本（create_script）；同时实现 ScriptUpdater 时也用于 update_script。
	ScriptCreator runtimecontext.ScriptCreator
	// NodeCreator 为已有脚本创建分析节点（create_analysis_node_script）。
	NodeCreator runtimecontext.AnalysisNodeCreator
	// ParamsBuilder 按 io_schema 生成节点参数（build_analysis_node_params）。
	ParamsBuilder runtimecontext.AnalysisNodeParamsBuilder
	// NodeUpdater 更新分析节点（update_analysis_node_agent / update_analysis_node_script）。
	NodeUpdater runtimecontext.AnalysisNodeUpdater
	// WorkflowCreator 创建工作流（create_workflow）；同时实现 WorkflowUpdater 时也用于 update_workflow。
	WorkflowCreator runtimecontext.WorkflowCreator
	// AnalysisRunner 提交分析运行（run_analysis_workflow）；同时实现 AnalysisUpdater 时也用于
	// update_analysis_workflow，实现 AnalysisCreator 时也用于 create_analysis_workflow。
	AnalysisRunner runtimecontext.AnalysisRunner
	// MemorySaver 保存用户长期记忆（save_memory）。
	MemorySaver runtimecontext.MemorySaver

	// 以下为只读浏览能力，按资源拆成窄接口。
	ScriptBrowser   runtimecontext.ScriptBrowser
	NodeBrowser     runtimecontext.AnalysisNodeBrowser
	WorkflowBrowser runtimecontext.WorkflowBrowser
	AnalysisBrowser runtimecontext.AnalysisBrowser
	TemplateBrowser runtimecontext.ContainerTemplateBrowser
	DatasetBrowser  runtimecontext.DatasetBrowser
	ReportContext   runtimecontext.ProjectReportContextResolver
	// DatasetWriter 准备已知 key 的数据集 / 向数据集登记文件
	// （get_project_database_path / add_file_to_dataset）。
	DatasetWriter runtimecontext.DatasetWriter

	// Skills 技能解析能力（get_skill / load_skill_resource）。
	Skills runtimecontext.SkillResolver
	// SkillWriter 技能写能力（create_skill / edit_skill）。
	SkillWriter runtimecontext.SkillWriter
	// ProfilePromptWriter 修改当前 Profile 系统提示词的能力（update_profile_prompt）。
	ProfilePromptWriter runtimecontext.ProfilePromptWriter

	// SubAgentSessions 子 Agent 的会话 / 消息落库（agent 工具使用）。
	//
	// 进程级服务（会话 + 消息服务）由 container 装配成该端口；agent 工具的其余
	// 请求级依赖（LLM 客户端工厂 / Runtime）在执行期从工具 ctx 读取。
	SubAgentSessions native.SubAgentSessionStore
}

// scriptUpdater 返回「更新脚本基本信息」能力。
//
// 容器侧的脚本业务实现通常同时实现 ScriptCreator 与 ScriptUpdater（同一份读改写逻辑），
// 因此 Deps 只暴露 ScriptCreator，这里按需断言，避免调用方重复装配两个字段。
func (d Deps) scriptUpdater() runtimecontext.ScriptUpdater {
	if updater, ok := d.ScriptCreator.(runtimecontext.ScriptUpdater); ok {
		return updater
	}
	return nil
}

// workflowUpdater 返回「更新工作流」能力。
//
// 容器侧的工作流实现同时实现 WorkflowCreator 与 WorkflowUpdater（同一份读改写逻辑），
// 因此 Deps 只暴露 WorkflowCreator，这里按需断言。
func (d Deps) workflowUpdater() runtimecontext.WorkflowUpdater {
	if updater, ok := d.WorkflowCreator.(runtimecontext.WorkflowUpdater); ok {
		return updater
	}
	return nil
}

// analysisUpdater 返回「更新分析运行」能力。
//
// 容器侧的分析运行实现同时实现 AnalysisRunner 与 AnalysisUpdater（共用同一份保存流程），
// 因此 Deps 只暴露 AnalysisRunner，这里按需断言。
func (d Deps) analysisUpdater() runtimecontext.AnalysisUpdater {
	if updater, ok := d.AnalysisRunner.(runtimecontext.AnalysisUpdater); ok {
		return updater
	}
	return nil
}

// analysisCreator 返回「为工作流创建一次分析运行」能力。
//
// 容器侧的分析运行实现同时实现 AnalysisRunner / AnalysisUpdater / AnalysisCreator
// （共用同一份保存流程），因此 Deps 只暴露 AnalysisRunner，这里按需断言。
func (d Deps) analysisCreator() runtimecontext.AnalysisCreator {
	if creator, ok := d.AnalysisRunner.(runtimecontext.AnalysisCreator); ok {
		return creator
	}
	return nil
}
