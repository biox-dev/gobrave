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
func All(d Deps) []tool.Tool {
	return []tool.Tool{
		GetWeather(),
		CreateScript(d.ScriptCreator),
		CreateScriptAnalysis(d.NodeCreator, d.Runner),
		UpdateScript(d.scriptUpdater()),
		ListProjectScripts(d.ScriptBrowser),
		GetScript(d.ScriptBrowser),
		GetAnalysisNode(d.NodeBrowser),
		ListContainerTemplates(d.TemplateBrowser),
		GetContainerTemplate(d.TemplateBrowser),
		TerminalExec(d.Terminal),
		GetDataset(d.DatasetBrowser),
		GetProjectDatabasePath(d.DatasetWriter),
		AddFileToDataset(d.DatasetWriter),
		BuildAnalysisNodeParams(d.ParamsBuilder),
		RunAnalysisNode(d.Runner),
		UpdateAgentAnalysis(d.NodeUpdater, d.Runner, d.NodeBrowser),
		UpdateScriptAnalysis(d.NodeUpdater, d.Runner, d.NodeBrowser),
		CreateAgentAnalysis(d.AgentRunner),
		ReadFileByID(d.FileReader),
		SaveMemory(d.MemorySaver),
		GetReportContext(d.ReportContext),
		// 技能：指令正文与附属资源都按需通过工具加载（渐进式披露）。
		skill.GetSkill(d.Skills),
		skill.LoadSkillResource(d.Skills),
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
