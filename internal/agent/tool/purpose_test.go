package tool

import (
	"context"
	"testing"
)

// TestListInjectsPurposeProperty 校验 List 在每个工具 schema 中追加 purpose 属性。
func TestListInjectsPurposeProperty(t *testing.T) {
	reg := NewRegistryWith(NewFunc("demo", "demo tool",
		Schema("", map[string]any{"id": StringProperty("ID")}, "id"),
		func(_ context.Context, _ struct{}) (any, error) { return nil, nil }))

	defs := reg.List()
	if len(defs) != 1 {
		t.Fatalf("List() 返回 %d 个定义，期望 1", len(defs))
	}
	props, _ := defs[0].InputSchema["properties"].(map[string]any)
	if _, ok := props[PurposePropertyName]; !ok {
		t.Fatalf("properties = %v，缺少 %q", props, PurposePropertyName)
	}
	if _, ok := props["id"]; !ok {
		t.Fatalf("properties = %v，原有属性 id 丢失", props)
	}
	// 不进入 required：避免约束解码下小模型漏填导致调用失败。
	if req, _ := defs[0].InputSchema["required"].([]string); len(req) > 0 {
		for _, name := range req {
			if name == PurposePropertyName {
				t.Fatalf("required = %v，不应包含 %q", req, PurposePropertyName)
			}
		}
	}
}

// TestListDoesNotMutateToolDefinition 校验 List 返回拷贝，不污染工具自身定义。
func TestListDoesNotMutateToolDefinition(t *testing.T) {
	tl := NewFunc("demo", "demo tool",
		Schema("", map[string]any{"id": StringProperty("ID")}, "id"),
		func(_ context.Context, _ struct{}) (any, error) { return nil, nil })
	reg := NewRegistryWith(tl)

	_ = reg.List()

	props, _ := tl.Definition().InputSchema["properties"].(map[string]any)
	if _, ok := props[PurposePropertyName]; ok {
		t.Fatalf("List() 修改了工具自身的 Definition: properties = %v", props)
	}
}

// TestListSkipsSchemaWithoutProperties 校验非 object schema 不被改写。
func TestListSkipsSchemaWithoutProperties(t *testing.T) {
	tl := NewFunc("demo", "demo tool", nil,
		func(_ context.Context, _ struct{}) (any, error) { return nil, nil })
	reg := NewRegistryWith(tl)

	defs := reg.List()
	if defs[0].InputSchema != nil {
		t.Fatalf("InputSchema = %v，期望保持 nil", defs[0].InputSchema)
	}
}
