package wsl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/biox-dev/gobrave/internal/logger"
)

// wslExePath 返回 wsl.exe 的绝对路径。
//
// 必须用绝对路径：PATH 里可能存在同名程序（例如某些 shell 包装脚本），
// 用相对名会被劫持，导致行为不可预测。
func wslExePath() (string, error) {
	root := strings.TrimSpace(os.Getenv("SystemRoot"))
	if root == "" {
		root = `C:\Windows`
	}
	p := filepath.Join(root, "System32", "wsl.exe")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("wsl.exe not found at %s: %w", p, err)
	}
	return p, nil
}

// runWSL 执行 wsl.exe 的参数列表，返回解码后的标准输出。
//
// 关键点：wsl.exe 的输出是 UTF-16LE（尤其是 -l -q），直接 string(out) 会
// 得到大量 NUL 字节，必须解码后使用。部分子命令（如 -- bash -lc）输出的是
// UTF-8，解码函数会自动识别。
func runWSL(ctx context.Context, args ...string) (string, error) {
	exe, err := wslExePath()
	if err != nil {
		logger.Errorf(ctx, "[wsl] resolve wsl.exe failed: %v", err)
		return "", err
	}

	cmdline := formatArgv(exe, args)
	logger.Infof(ctx, "[wsl] exec: %s", cmdline)
	start := time.Now()

	cmd := exec.CommandContext(ctx, exe, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(decodeOutput(stderr.Bytes()))
		if msg == "" {
			msg = strings.TrimSpace(decodeOutput(stdout.Bytes()))
		}
		logger.Errorf(ctx, "[wsl] exec failed after %s: %s: err=%v stderr=%q stdout=%q",
			time.Since(start).Round(time.Millisecond), cmdline, err, msg, strings.TrimSpace(decodeOutput(stdout.Bytes())))
		return decodeOutput(stdout.Bytes()), fmt.Errorf("wsl %s: %w: %s", strings.Join(args, " "), err, msg)
	}

	out := decodeOutput(stdout.Bytes())
	logger.Debugf(ctx, "[wsl] exec ok in %s: %s: stdout=%q stderr=%q",
		time.Since(start).Round(time.Millisecond), cmdline, strings.TrimSpace(out), strings.TrimSpace(decodeOutput(stderr.Bytes())))
	return out, nil
}

// formatArgv 把可执行文件与参数拼成一行可复制的命令行，便于排查。
//
// 含空白或引号的参数用双引号包裹，避免日志里的命令行被误读。
func formatArgv(exe string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteArg(exe))
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

// quoteArg 在必要时给参数加双引号。
func quoteArg(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"'") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

// compactScript 把多行脚本压成单行，便于写进一行日志。
//
// 保留换行分隔的命令边界（用 "; " 连接），去掉空行，避免日志被拉长。
func compactScript(script string) string {
	lines := strings.Split(script, "\n")
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "; ")
}

// runInDistro 以 root 身份在指定发行版里执行一条 shell 命令。
//
// 统一用 sh -lc：交互式登录 shell 会加载 profile，可能改写 PATH / 启动额外进程，
// 而这里需要的是可预测的最小环境。
func runInDistro(ctx context.Context, distro, script string) (string, error) {
	logger.Debugf(ctx, "[wsl] run in distro %s: %s", distro, compactScript(script))
	return runWSL(ctx, "-d", distro, "-u", "root", "--", "sh", "-lc", script)
}

// decodeOutput 把 wsl.exe 的输出解码成 UTF-8。
//
// 判据：若字节流中含有大量 0x00（UTF-16LE 的 ASCII 高位），按 UTF-16LE 解码；
// 否则按 UTF-8 原样返回。这样同一次调用既能处理 -l -q（UTF-16）也能处理
// 发行版内命令输出（UTF-8）。
func decodeOutput(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if isUTF16LE(b) {
		return decodeUTF16LE(b)
	}
	if utf8.Valid(b) {
		return string(b)
	}
	// 非法 UTF-8 且不像 UTF-16：尽量保留可读部分。
	return strings.ToValidUTF8(string(b), "")
}

// isUTF16LE 判断字节流是否更像 UTF-16LE。
//
// 判据：UTF-16LE 编码的 ASCII 形如 'a',0,'b',0，因此奇数位（下标 1,3,5...）
// 的 0x00 比例很高。这里要求「超过半数奇数位为 0」，避免把恰含少量 NUL 的
// UTF-8 输出（如二进制片段）误判。
func isUTF16LE(b []byte) bool {
	if len(b) < 2 {
		return false
	}
	odd := b[1:]
	if len(odd) == 0 {
		return false
	}
	return countNUL(odd)*2 > len(odd) && len(b)%2 == 0
}

func countNUL(b []byte) int {
	n := 0
	for _, c := range b {
		if c == 0 {
			n++
		}
	}
	return n
}

// decodeUTF16LE 解码 UTF-16LE 字节流并去掉 BOM。
func decodeUTF16LE(b []byte) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	if len(u) > 0 && u[0] == 0xFEFF {
		u = u[1:]
	}
	return string(utf16.Decode(u))
}

// listDistros 返回已安装的发行版名列表（已 trim、已过滤空行）。
func listDistros(ctx context.Context) ([]string, error) {
	out, err := runWSL(ctx, "-l", "-q")
	if err != nil {
		return nil, err
	}
	names := splitDistroList(out)
	logger.Debugf(ctx, "[wsl] installed distros: %v", names)
	return names, nil
}

// runningDistros 返回正在运行的发行版名列表。
func runningDistros(ctx context.Context) ([]string, error) {
	out, err := runWSL(ctx, "-l", "--running", "-q")
	if err != nil {
		// 旧版 WSL 不支持 --running：降级为空列表，由上层用「能否执行命令」判断。
		return nil, err
	}
	return splitDistroList(out), nil
}

// splitDistroList 把 wsl -l -q 的多行输出拆成名字列表。
//
// 需要同时 strip 行尾的 \r 与 NUL：UTF-16 解码后可能残留 \x00，
// 未去除会让发行版名比较（containsDistro）永远不相等。
func splitDistroList(out string) []string {
	lines := strings.Split(out, "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		name := strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(line), "\x00", ""))
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names
}

// containsDistro 判断列表里是否存在目标发行版（大小写不敏感）。
func containsDistro(names []string, target string) bool {
	for _, n := range names {
		if strings.EqualFold(strings.TrimSpace(n), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

// isWSLUnavailable 判断错误是否表示 WSL 本身不可用（未启用 / 未安装）。
func isWSLUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, key := range []string{
		"wsl.exe not found",
		"not installed",
		"windows subsystem for linux",
		"wsl_register_distribution",
	} {
		if strings.Contains(msg, key) {
			return true
		}
	}
	return false
}
