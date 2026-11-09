package config

import "testing"

func TestStorageDrive(t *testing.T) {
	cases := []struct {
		name string
		cfg  *Config
		want string
	}{
		{name: "nil config", cfg: nil, want: ""},
		{name: "nil storage", cfg: &Config{}, want: ""},
		{name: "empty base dir", cfg: &Config{Storage: &StorageConfig{}}, want: ""},
		{name: "windows drive", cfg: &Config{Storage: &StorageConfig{BaseDir: `D:\workspace\gobrave\.gobrave`}}, want: `D:\`},
		{name: "windows drive with slash", cfg: &Config{Storage: &StorageConfig{BaseDir: `D:/workspace/gobrave`}}, want: `D:\`},
		{name: "lowercase drive", cfg: &Config{Storage: &StorageConfig{BaseDir: `c:\gobrave`}}, want: `c:\`},
		{name: "linux path", cfg: &Config{Storage: &StorageConfig{BaseDir: "/home/admin/.gobrave"}}, want: ""},
		{name: "padded windows drive", cfg: &Config{Storage: &StorageConfig{BaseDir: "  D:\\gobrave  "}}, want: `D:\`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StorageDrive(tc.cfg); got != tc.want {
				t.Errorf("StorageDrive() = %q, want %q", got, tc.want)
			}
		})
	}
}
