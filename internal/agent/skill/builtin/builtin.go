// Package builtin 提供框架内置的技能（如 echo）。
//
// 内置技能与业务解耦：All() 汇总全部技能供统一注册。新增内置技能时，在本包
// 新建一个文件实现该技能，并把它的构造函数追加到 All()，即可自动接入各 Provider
// 的 skill-call 链路，无需改动上层注册逻辑。
package builtin

import (
	"context"

	"github.com/biox-dev/gobrave/internal/agent/skill"
)

// All 返回框架内置的全部技能。新增内置技能时在此追加。
func All() []skill.Skill {
	return []skill.Skill{
		Echo(),
		// GetWeather(),
		ReviewGuide(),
	}
}

// Echo 是一个示例技能：原样返回输入文本。
//
// 它演示了 Func 型（强类型函数）技能的最小实现，同时作为 SkillRunner / Invoker
// 的测试目标。
func Echo() skill.Skill {
	return skill.NewFunc(skill.Manifest{
		Definition: skill.Definition{
			Name:        "echo",
			Description: "原样返回输入的文本。",
			InputSchema: skill.Schema("echo 入参", map[string]any{
				"text": skill.StringProperty("要回显的文本"),
			}, "text"),
		},
		Version:      "1.0.0",
		Instructions: "原样回显文本。",
	}, func(_ context.Context, in struct {
		Text string `json:"text"`
	}) (string, error) {
		return in.Text, nil
	})
}
