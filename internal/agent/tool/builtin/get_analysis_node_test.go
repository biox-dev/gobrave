package builtin

import "testing"

// TestParseAnalysisNodeID 覆盖分析节点 ID 的解析与引用前缀容错。
//
// 界面「Ask Agent」插入的引用 chip 文本是 "@analysis_node:<id>"，
// 模型可能原样传进来，因此解析必须剥离前缀后再转整数。
func TestParseAnalysisNodeID(t *testing.T) {
	if id, err := parseAnalysisNodeID(" 123 "); err != nil || id != 123 {
		t.Fatalf("parseAnalysisNodeID(123) = (%d, %v), want (123, nil)", id, err)
	}
	if id, err := parseAnalysisNodeID("@analysis_node:2109711590457610240"); err != nil || id != 2109711590457610240 {
		t.Fatalf("parseAnalysisNodeID(@analysis_node:...) = (%d, %v), want (2109711590457610240, nil)", id, err)
	}
	for _, raw := range []string{"", "abc", "0", "-1", "@analysis_node:"} {
		if _, err := parseAnalysisNodeID(raw); err == nil {
			t.Fatalf("parseAnalysisNodeID(%q) expected error", raw)
		}
	}
}
