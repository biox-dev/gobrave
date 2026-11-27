package utils

import "testing"

func TestGetAnalysisURLPrefixStripsDataDirOnly(t *testing.T) {
	baseDir := "/data2/brave_analysis_workspace"

	// data 目录之后的部分原样保留，不再拼接 /data-analysis（由前端统一添加）。
	if got, want := GetAnalysisURLPrefix(baseDir, baseDir+"/data/34/analysis_node/100/200/output"), "/34/analysis_node/100/200/output/"; got != want {
		t.Fatalf("GetAnalysisURLPrefix() = %q, want %q", got, want)
	}

	// 目录不在 data 下时原样返回（补上首尾斜杠），不静默丢成空串。
	if got, want := GetAnalysisURLPrefix(baseDir, "/tmp/some/output"), "/tmp/some/output/"; got != want {
		t.Fatalf("GetAnalysisURLPrefix() = %q, want %q", got, want)
	}

	if got := GetAnalysisURLPrefix(baseDir, "   "); got != "" {
		t.Fatalf("GetAnalysisURLPrefix() = %q, want empty", got)
	}
}

func TestPrefixMarkdownImageURLs(t *testing.T) {
	prefix := "/34/analysis_node/100/200/output/"

	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "relative path gets prefixed",
			content: "![a](plot.png)",
			want:    "![a](/34/analysis_node/100/200/output/plot.png)",
		},
		{
			name:    "dot-slash relative path gets prefixed",
			content: "![a](./plot.png)",
			want:    "![a](/34/analysis_node/100/200/output/plot.png)",
		},
		{
			name:    "absolute paths stay untouched",
			content: "![a](/images/2026/01/01/a.png) ![b](/data-analysis/34/a.png)",
			want:    "![a](/images/2026/01/01/a.png) ![b](/data-analysis/34/a.png)",
		},
		{
			name:    "remote url stays untouched",
			content: "![a](https://example.com/a.png)",
			want:    "![a](https://example.com/a.png)",
		},
		{
			name:    "non image markdown stays untouched",
			content: "[link](report.md)",
			want:    "[link](report.md)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PrefixMarkdownImageURLs(tc.content, prefix); got != tc.want {
				t.Fatalf("PrefixMarkdownImageURLs() = %q, want %q", got, tc.want)
			}
		})
	}

	// 空前缀 = 不改写，内容原样返回（幂等：重复执行不会二次拼前缀）。
	content := "![a](/34/analysis_node/100/200/output/plot.png)"
	if got := PrefixMarkdownImageURLs(content, ""); got != content {
		t.Fatalf("PrefixMarkdownImageURLs() = %q, want %q", got, content)
	}
	if got := PrefixMarkdownImageURLs(content, prefix); got != content {
		t.Fatalf("PrefixMarkdownImageURLs() is not idempotent, got %q", got)
	}
}

func TestRewriteMarkdownImageBaseDirPaths(t *testing.T) {
	baseDir := "/data2/brave_analysis_workspace"

	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "absolute baseDir path is rewritten to data-relative path",
			content: "![a](" + baseDir + "/data/36/analysis_node/1/2/output/a.png)",
			want:    "![a](/36/analysis_node/1/2/output/a.png)",
		},
		{
			name:    "nested image dir keeps the rest of the path",
			content: "![a](" + baseDir + "/data/36/project_report/1/output/img/a.png)",
			want:    "![a](/36/project_report/1/output/img/a.png)",
		},
		{
			name:    "data-relative path stays untouched",
			content: "![a](/36/analysis_node/1/2/output/a.png)",
			want:    "![a](/36/analysis_node/1/2/output/a.png)",
		},
		{
			name:    "path outside baseDir stays untouched",
			content: "![a](/tmp/other/a.png) ![b](https://example.com/a.png)",
			want:    "![a](/tmp/other/a.png) ![b](https://example.com/a.png)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RewriteMarkdownImageBaseDirPaths(tc.content, baseDir); got != tc.want {
				t.Fatalf("RewriteMarkdownImageBaseDirPaths() = %q, want %q", got, tc.want)
			}
			// 幂等：改写结果再跑一次不应变化。
			if got := RewriteMarkdownImageBaseDirPaths(RewriteMarkdownImageBaseDirPaths(tc.content, baseDir), baseDir); got != tc.want {
				t.Fatalf("RewriteMarkdownImageBaseDirPaths() is not idempotent, got %q", got)
			}
		})
	}

	if got := RewriteMarkdownImageBaseDirPaths("![a]("+baseDir+"/data/a.png)", ""); got != "![a]("+baseDir+"/data/a.png)" {
		t.Fatalf("RewriteMarkdownImageBaseDirPaths() with empty baseDir changed content, got %q", got)
	}
}
