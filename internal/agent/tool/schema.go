package tool

// 本文件提供构建工具入参 JSON Schema 的最小化便捷构造器。
//
// 默认产出 OpenAI 风格的 object schema：{type: "object", properties: {...}, required: [...]}。
// 需要更复杂结构（嵌套、数组、枚举、oneOf 等）时可直接手写 map[string]any 传入，
// 或在此基础上扩展。

// Schema 构建一个 object 类型的 JSON Schema，用于描述工具入参。
func Schema(description string, properties map[string]any, required ...string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	if description != "" {
		s["description"] = description
	}
	return s
}

// StringProperty 构造一个 string 类型属性。
func StringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// NumberProperty 构造一个 number 类型属性。
func NumberProperty(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

// IntegerProperty 构造一个 integer 类型属性。
func IntegerProperty(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

// BooleanProperty 构造一个 boolean 类型属性。
func BooleanProperty(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}
