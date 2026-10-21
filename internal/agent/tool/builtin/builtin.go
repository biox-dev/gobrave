// Package builtin 提供框架内置的工具（如 get_weather）。
//
// 约定：所有内置工具都直接实现 tool.Tool（通常用 tool.NewFunc 构造），不引入
// adapter 层；新工具在本包新建文件实现后，只需把构造函数追加到 All()，即可自动
// 接入各 Provider 的 tool-call 链路，无需改动上层注册逻辑。
//
// 需要会话 / 消息能力的工具（如后续的 CoderAgentTools）应依赖
// session.Service / message.Service，通过工具构造参数或执行期上下文注入。
package builtin

import (
	"github.com/biox-dev/gobrave/internal/agent/skill"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// All 返回框架内置的全部工具。新增内置工具时在此追加。
func All() []tool.Tool {
	return []tool.Tool{
		GetWeather(),
		CreateScript(),
		CreateScriptAnalysis(),
		UpdateScript(),
		ListProjectScripts(),
		GetScript(),
		GetAnalysisNode(),
		ListContainerTemplates(),
		BuildAnalysisNodeParams(),
		RunAnalysisNode(),
		UpdateAgentAnalysis(),
		UpdateScriptAnalysis(),
		CreateAgentAnalysis(),
		ReadFileByID(),
		SaveMemory(),
		// 技能：指令正文与附属资源都按需通过工具加载（渐进式披露）。
		skill.GetSkill(),
		skill.LoadSkillResource(),
	}
}
