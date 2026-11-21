// Package builtin 提供框架内置的工具（如 get_weather）。
//
// 约定：所有内置工具都直接实现 tool.Tool（通常用 tool.NewFunc 构造），不引入
// adapter 层；新工具在本包新建文件实现后，把构造函数追加到 All()，并在 Deps 中
// 暴露它需要的进程级静态依赖即可接入各 Provider 的 tool-call 链路。
//
// 依赖约定：进程级静态服务（分析节点执行器 / 浏览器 / 技能解析等）通过构造函数
// 从 Deps 注入；随请求变化的事实（用户 / 会话 / 项目 / RuntimeContext）与权限
// 仍由 Provider 在执行工具前注入 ctx。
package builtin

import (
	"sort"

	"github.com/biox-dev/gobrave/internal/agent/skill"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// All 返回框架内置的全部工具。新增内置工具时在此追加，并把所需依赖从 Deps 传入。
//
// 排序按资源域分组（同一资源的 list / get / create / update 相邻），便于对照维护：
// 交互 → 文件 / 数据集 → 容器模板 → 脚本 → 分析节点 → 工作流 → 分析运行 → 报告 → 技能 / Profile。
func All(d Deps) []tool.Tool {
	return []tool.Tool{
		// 交互与通用能力。
		GetWeather(),
		AskUserTool(),
		AgentTool(d),
		TerminalExec(d.Terminal),
		UIAction(d.UIActions),
		SaveMemory(d.MemorySaver),

		// 文件 / 数据集。
		GetFile(d.FileReader),
		GetDataset(d.DatasetBrowser),
		GetProjectDatabasePath(d.DatasetWriter),
		AddFileToDataset(d.DatasetWriter),

		// 容器模板。
		ListContainerTemplates(d.TemplateBrowser),
		GetContainerTemplate(d.TemplateBrowser),

		// 脚本：列出 / 查看 / 创建 / 更新。
		ListProjectScripts(d.ScriptBrowser),
		GetScript(d.ScriptBrowser),
		CreateScript(d.ScriptCreator),
		UpdateScript(d.scriptUpdater(), d.ScriptBrowser),

		// 分析节点：查看 / 创建 / 更新 / 参数 / 运行。
		GetAnalysisNode(d.NodeBrowser),
		CreateAnalysisNodeScript(d.NodeCreator, d.Runner),
		CreateAnalysisNodeAgent(d.AgentRunner),
		UpdateAnalysisNodeScript(d.NodeUpdater, d.Runner, d.NodeBrowser),
		UpdateAnalysisNodeAgent(d.NodeUpdater, d.Runner, d.NodeBrowser),
		BuildAnalysisNodeParams(d.ParamsBuilder),
		RunAnalysisNode(d.Runner),

		// 工作流：列出 / 查看 / 创建 / 更新。
		ListProjectWorkflows(d.WorkflowBrowser),
		GetWorkflow(d.WorkflowBrowser),
		CreateWorkflow(d.WorkflowCreator),
		UpdateWorkflow(d.workflowUpdater(), d.WorkflowBrowser),

		// 分析运行：创建 / 查看 / 更新 / 提交。
		CreateAnalysisWorkflow(d.analysisCreator(), d.AnalysisRunner),
		GetAnalysisWorkflow(d.AnalysisBrowser),
		UpdateAnalysisWorkflow(d.analysisUpdater(), d.AnalysisBrowser),
		RunAnalysisWorkflow(d.AnalysisRunner, d.AnalysisBrowser),

		// 计划（plan）：agent 的步骤树，可引用会话资源 ID（报告只是其中一种消费方式）。
		ListSessionResources(d.SessionResources),
		CreatePlan(d.Plan),
		ListPlans(d.Plan),
		GetPlan(d.Plan),
		UpdatePlan(d.Plan),
		DeletePlan(d.Plan),

		// 项目报告：正文读写（create_report / update_report）+ 按 ID 查看。
		GetReport(d.ReportBrowser),
		CreateReport(d.ReportWriter),
		UpdateReport(d.ReportWriter),

		// 技能：指令正文与附属资源都按需通过工具加载（渐进式披露）。
		skill.GetSkill(d.Skills),
		skill.LoadSkillResource(d.Skills),
		skill.CreateSkill(d.SkillWriter),
		skill.EditSkill(d.SkillWriter),

		// Profile：修改当前 Profile 的系统提示词（update_profile_prompt）。
		UpdateProfilePrompt(d.ProfilePromptWriter),
	}
}

// Names 返回框架内置工具的名称列表（升序）。
//
// 只构造工具定义、不执行工具，因此可以安全地传入零值 Deps。供内置 Profile 显式
// 声明工具白名单使用：名单与 All 同源，新增内置工具会自动进入名单，不会漂移。
func Names() []string {
	tools := All(Deps{})
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if name := t.Definition().Name; name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
