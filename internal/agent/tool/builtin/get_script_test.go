package builtin

import "testing"

// parseScriptID 需要同时接受裸 ID 与界面「Ask Agent」插入的引用 chip 文本。
func TestParseScriptID(t *testing.T) {
	if id, err := parseScriptID("2109711590457610240"); err != nil || id != 2109711590457610240 {
		t.Fatalf("parseScriptID(raw) = (%d, %v), want (2109711590457610240, nil)", id, err)
	}
	if id, err := parseScriptID("@script:2109711590457610240"); err != nil || id != 2109711590457610240 {
		t.Fatalf("parseScriptID(@script:...) = (%d, %v), want (2109711590457610240, nil)", id, err)
	}
	for _, raw := range []string{"", "abc", "0", "-1", "@script:"} {
		if id, err := parseScriptID(raw); err == nil {
			t.Fatalf("parseScriptID(%q) = (%d, nil), want error", raw, id)
		}
	}
}
