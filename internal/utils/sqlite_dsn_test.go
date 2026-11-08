package utils

import (
	"net/url"
	"runtime"
	"testing"
)

func TestBuildSQLiteDSN(t *testing.T) {
	tests := []struct {
		name        string
		dbPath      string
		pragmas     []string
		windowsOnly bool
		want        string
	}{
		{
			name:    "unix absolute path",
			dbPath:  "/var/lib/gobrave/gobrave.db",
			pragmas: []string{"busy_timeout(5000)", "journal_mode(WAL)", "foreign_keys(on)"},
			want:    "file:///var/lib/gobrave/gobrave.db?_pragma=busy_timeout%285000%29&_pragma=journal_mode%28WAL%29&_pragma=foreign_keys%28on%29",
		},
		{
			name:        "windows drive path",
			dbPath:      `C:\workspace\gobrave\.gobrave\db\gobrave.db`,
			pragmas:     []string{"journal_mode(WAL)"},
			windowsOnly: true,
			want:        "file:C:/workspace/gobrave/.gobrave/db/gobrave.db?_pragma=journal_mode%28WAL%29",
		},
		{
			name:        "windows path with spaces",
			dbPath:      `C:\Program Files\gobrave\gobrave.db`,
			pragmas:     []string{"busy_timeout(5000)"},
			windowsOnly: true,
			want:        "file:C:/Program%20Files/gobrave/gobrave.db?_pragma=busy_timeout%285000%29",
		},
		{
			name:   "no pragmas",
			dbPath: "/tmp/gobrave.db",
			want:   "file:///tmp/gobrave.db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.windowsOnly && runtime.GOOS != "windows" {
				t.Skip("windows path semantics")
			}

			got := BuildSQLiteDSN(tt.dbPath, tt.pragmas...)
			if got != tt.want {
				t.Fatalf("BuildSQLiteDSN() = %q, want %q", got, tt.want)
			}

			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("invalid URI %q: %v", got, err)
			}
			if u.Scheme != "file" {
				t.Fatalf("scheme = %q, want file", u.Scheme)
			}
			if n := len(u.Query()["_pragma"]); n != len(tt.pragmas) {
				t.Fatalf("pragmas = %d, want %d", n, len(tt.pragmas))
			}
		})
	}
}
