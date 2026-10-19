package coder

import "testing"

// TestAllToolsWellFormed 校验每个 coder 工具都有唯一名称、非空描述与 object schema。
func TestAllToolsWellFormed(t *testing.T) {
	seen := make(map[string]bool)
	for _, tl := range All() {
		def := tl.Definition()
		if def.Name == "" {
			t.Fatalf("tool with empty name: %#v", def)
		}
		if seen[def.Name] {
			t.Fatalf("duplicate tool name: %s", def.Name)
		}
		seen[def.Name] = true

		if def.Description == "" {
			t.Errorf("tool %s: empty description", def.Name)
		}
		if def.InputSchema == nil {
			t.Errorf("tool %s: nil input schema", def.Name)
		}
		if got, _ := def.InputSchema["type"].(string); got != "object" {
			t.Errorf("tool %s: input schema type = %q, want object", def.Name, got)
		}
	}
}
