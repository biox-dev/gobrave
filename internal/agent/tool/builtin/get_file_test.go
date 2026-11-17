package builtin

import "testing"

// parseFileID 必须同时接受裸 ID（内置工具统一口径）与带 @file: 前缀的引用文本。
func TestParseFileID(t *testing.T) {
	if id, err := parseFileID("2109711590457610240"); err != nil || id != 2109711590457610240 {
		t.Fatalf("parseFileID(裸 ID) = (%d, %v), want (2109711590457610240, nil)", id, err)
	}
	if id, err := parseFileID("@file:2109711590457610240"); err != nil || id != 2109711590457610240 {
		t.Fatalf("parseFileID(@file:...) = (%d, %v), want (2109711590457610240, nil)", id, err)
	}

	for _, raw := range []string{"", "  ", "abc", "0", "-1", "@file:"} {
		if _, err := parseFileID(raw); err == nil {
			t.Fatalf("parseFileID(%q) = nil error, want error", raw)
		}
	}
}

// normalizeFileFormat 优先用元信息里的 format，为空时回退到路径后缀。
func TestNormalizeFileFormat(t *testing.T) {
	cases := []struct {
		format string
		path   string
		want   string
	}{
		{format: "CSV", path: "/data/a.csv", want: "csv"},
		{format: ".xlsx", path: "/data/a.xlsx", want: "xlsx"},
		{format: "", path: "/data/a.bam", want: "bam"},
		{format: "  ", path: "/data/a.VCF", want: "vcf"},
	}
	for _, c := range cases {
		if got := normalizeFileFormat(c.format, c.path); got != c.want {
			t.Fatalf("normalizeFileFormat(%q, %q) = %q, want %q", c.format, c.path, got, c.want)
		}
	}
}
