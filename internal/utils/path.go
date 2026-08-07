package utils

import (
	"os"
	"path/filepath"
	"strings"
)

const configDir = "BRAVE_CONFIG_DIR"

// ResolveExternalPath resolves project external file paths.
// If BRAVE_CONFIG_DIR is set, the path is resolved relative to it;
// otherwise, it is resolved relative to current working directory.
func ResolveExternalPath(relativePath string) (string, error) {
	if filepath.IsAbs(relativePath) {
		return filepath.Abs(relativePath)
	}

	baseDir := os.Getenv(configDir)
	if baseDir != "" {
		return filepath.Abs(filepath.Join(baseDir, relativePath))
	}

	return filepath.Abs(relativePath)
}

// ResolveConfiguredPath resolves storage path by this rule:
// - when configuredPath is empty, use executableDir/defaultRelativeToExecutable
// - otherwise, use configuredPath directly.
func ResolveConfiguredPath(configuredPath string, defaultRelativeToExecutable string) (string, error) {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath != "" {
		return configuredPath, nil
	}

	executablePath, err := os.Executable()
	if err != nil {
		return "", err
	}

	executableDir := filepath.Dir(executablePath)
	return filepath.Abs(filepath.Join(executableDir, defaultRelativeToExecutable))
}

func ResolveImageDir(baseDir string) string {
	return filepath.Join(baseDir, "images")
}
