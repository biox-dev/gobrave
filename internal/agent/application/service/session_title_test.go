package service

import "testing"

func TestNormalizeSessionTitle(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain", raw: "GWAS 数据质控流程", want: "GWAS 数据质控流程"},
		{name: "quoted prefix", raw: "标题：「GWAS 数据质控」", want: "GWAS 数据质控"},
		{name: "trailing punctuation", raw: "生成 QQ 图。", want: "生成 QQ 图"},
		{name: "first line only", raw: "分析脚本\n多余的说明", want: "分析脚本"},
		{name: "compress spaces", raw: "  GWAS   数据质控  ", want: "GWAS 数据质控"},
		{name: "empty", raw: "   \n  ", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeSessionTitle(tc.raw); got != tc.want {
				t.Fatalf("normalizeSessionTitle(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	got := truncateRunes("中文🙂abc", 3)
	if want := "中文🙂"; got != want {
		t.Fatalf("truncateRunes = %q, want %q", got, want)
	}
	if got := truncateRunes("abc", 10); got != "abc" {
		t.Fatalf("truncateRunes should not change short strings, got %q", got)
	}
}
