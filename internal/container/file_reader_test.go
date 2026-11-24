package container

import (
	"testing"
)

func TestNormalizeFormat(t *testing.T) {
	cases := []struct {
		format string
		path   string
		want   string
	}{
		{"xlsx", "a/b.xlsx", "xlsx"},
		{"", "a/b.CSV", "csv"},
		{".tsv", "a/b.tsv", "tsv"},
		{"", "a/b.unknown", "unknown"},
	}
	for _, tc := range cases {
		if got := normalizeFormat(tc.format, tc.path); got != tc.want {
			t.Fatalf("normalizeFormat(%q,%q)=%q want %q", tc.format, tc.path, got, tc.want)
		}
	}
}
