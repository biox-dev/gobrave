// Package builtin 提供框架内置的工具（如 get_weather）。
//
// 内置工具与业务解耦：All() 汇总全部工具供统一注册。新增内置工具时，在本包
// 新建一个文件实现该工具，并把它的构造函数追加到 All()，即可自动接入各 Provider
// 的 tool-call 链路，无需改动上层注册逻辑。
package builtin

import "github.com/biox-dev/gobrave/internal/agent/tool"

// All 返回框架内置的全部工具。新增内置工具时在此追加。
func All() []tool.Tool {
	return []tool.Tool{
		GetWeather(),
	}
}
