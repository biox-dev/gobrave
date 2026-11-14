package logger

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brokenStdout 用一个已关闭的文件冒充无效 stdout：桌面端 GUI 构建
// （Windows -H windowsgui）就是这种情况，对 stdout 的写入会失败。
func brokenStdout(t *testing.T) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("创建临时文件失败: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭临时文件失败: %v", err)
	}
	orig := os.Stdout
	os.Stdout = f
	t.Cleanup(func() { os.Stdout = orig })
}

// resetLogger 在用例结束后把全局日志切到独立的临时文件，避免污染真实日志。
func resetLogger(t *testing.T, logPath string) {
	t.Helper()
	t.Cleanup(func() { Configure(Options{Path: logPath + ".after", Color: "never"}) })
}

// Configure 必须保证文件日志先于 stdout：io.MultiWriter 在第一个 writer 出错后
// 就不再继续，若文件排在后面，stdout 无效会让落盘静默失效。
func TestConfigureFileFirstSurvivesBrokenStdout(t *testing.T) {
	brokenStdout(t)
	logPath := filepath.Join(t.TempDir(), "app.log")

	Configure(Options{Path: logPath, Color: "never"})
	resetLogger(t, logPath)

	Infof(context.Background(), "桌面端冒烟日志 %s", "file-first")

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}
	if !strings.Contains(string(data), "桌面端冒烟日志 file-first") {
		t.Fatalf("日志未落盘，实际内容: %q", string(data))
	}
}

// Output=file 时不应再写 stdout。
func TestConfigureOutputFileSkipsStdout(t *testing.T) {
	stdoutSink := filepath.Join(t.TempDir(), "stdout.txt")
	f, err := os.Create(stdoutSink)
	if err != nil {
		t.Fatalf("创建临时文件失败: %v", err)
	}
	orig := os.Stdout
	os.Stdout = f
	t.Cleanup(func() {
		os.Stdout = orig
		_ = f.Close()
	})

	logPath := filepath.Join(t.TempDir(), "app.log")
	Configure(Options{Path: logPath, Color: "never", Output: "file"})
	resetLogger(t, logPath)

	Infof(context.Background(), "仅文件 %s", "file-only")

	if data, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(data), "仅文件 file-only") {
		t.Fatalf("日志未写入文件: %q, %v", string(data), err)
	}
	if data, err := os.ReadFile(stdoutSink); err != nil || strings.Contains(string(data), "仅文件") {
		t.Fatalf("Output=file 不应写 stdout: %q, %v", string(data), err)
	}
}

// Output=stdout 时不创建日志文件。
func TestConfigureOutputStdoutSkipsFile(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "app.log")
	Configure(Options{Path: logPath, Color: "never", Output: "stdout"})
	resetLogger(t, logPath)

	Infof(context.Background(), "仅 stdout")

	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("Output=stdout 不应创建日志文件 %s", logPath)
	}
}
