package builtin

import "github.com/biox-dev/gobrave/internal/agent/skill"

// ReviewGuide 返回一个「代码评审指南」静态技能。
//
// 它演示了 Static 型（纯指令型）技能：没有可执行逻辑（无 fn），只有一段 markdown
// 指令正文。这类技能的 Invoke 会原样返回 Instructions，主要用于把「教模型怎么
// 做」的上下文注入到 system prompt，供模型在没有 function calling 时也能感知。
func ReviewGuide() skill.Skill {
	return skill.NewStatic(skill.Manifest{
		Definition: skill.Definition{
			Name:        "review_guide",
			Description: "代码评审时遵循的评审要点与检查清单。",
		},
		Version: "1.0.0",
		Instructions: `在评审代码时，请按以下检查清单逐项评估：

## 正确性
- 逻辑是否符合预期，边界条件是否覆盖。
- 错误处理是否完整，是否有未处理的 error / panic。

## 可读性
- 命名是否清晰、一致，是否遵循项目既有约定。
- 函数是否职责单一，是否存在过长或过度嵌套。

## 健壮性
- 并发场景下是否存在数据竞争或死锁。
- 资源（连接、文件、goroutine）是否被正确释放。

## 测试
- 关键路径是否有测试覆盖，测试是否确定性。

请输出：问题列表（按严重程度排序）、具体位置、修改建议。`,
	})
}
