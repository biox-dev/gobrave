package builtin

import "github.com/biox-dev/gobrave/internal/agent/runtimecontext"

// Deps 是内置工具集的进程级静态依赖。
//
// 这些能力在进程启动时由 container 一次性装配（见 buildAgentClient），随工具构造函数
// 注入，而不是在每次调用时塞进 context：工具依赖因此对编译器可见、可单测，也不再需要
// 为每个能力维护一套 With/From。
//
// 与业务相关的动态事实（RuntimeContext / TaskFacts / 权限）仍然走工具执行 ctx，
// 因为它们随请求变化、且 RuntimeContext 会随 agent_tasks.request 序列化落库。
//
// 字段按工具消费者拆成窄接口（见 runtimecontext 包），容器用一个实现即可同时满足；
// 测试 fake 只需实现被用到的那一个。
type Deps struct {
	// Runner 运行分析节点（run_analysis_node / create_script_analysis / update_*）。
	Runner runtimecontext.AnalysisNodeRunner
	// AgentRunner 创建并（可选）运行命令型分析节点（create_agent_analysis）。
	AgentRunner runtimecontext.AgentAnalysisRunner
	// Probe 在临时容器中同步执行命令（probe_container_env）。
	Probe runtimecontext.ContainerEnvProbe
	// FileReader 按 go_file 主键读取文件（read_file_by_id）。
	FileReader runtimecontext.FileReader
	// ScriptCreator 创建脚本（create_script）；同时实现 ScriptUpdater 时也用于 update_script。
	ScriptCreator runtimecontext.ScriptCreator
	// NodeCreator 为已有脚本创建分析节点（create_script_analysis）。
	NodeCreator runtimecontext.AnalysisNodeCreator
	// ParamsBuilder 按 io_schema 生成节点参数（build_analysis_node_params）。
	ParamsBuilder runtimecontext.AnalysisNodeParamsBuilder
	// NodeUpdater 更新分析节点（update_agent_analysis / update_script_analysis）。
	NodeUpdater runtimecontext.AnalysisNodeUpdater
	// MemorySaver 保存用户长期记忆（save_memory）。
	MemorySaver runtimecontext.MemorySaver

	// 以下为只读浏览能力，按资源拆成窄接口。
	ScriptBrowser   runtimecontext.ScriptBrowser
	NodeBrowser     runtimecontext.AnalysisNodeBrowser
	TemplateBrowser runtimecontext.ContainerTemplateBrowser
	DatasetBrowser  runtimecontext.DatasetBrowser
	ReportContext   runtimecontext.ProjectReportContextResolver

	// Skills 技能解析能力（get_skill / load_skill_resource）。
	Skills runtimecontext.SkillResolver
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
