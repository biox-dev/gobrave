package logger

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	loggerMu      sync.Mutex
	activeLogFile io.WriteCloser
)

var appLogger = logrus.New()

// 添加调用者字段
func addCaller(entry *logrus.Entry, skip int) *logrus.Entry {
	pc, file, line, ok := runtime.Caller(skip)
	if !ok {
		return entry
	}
	shortFile := path.Base(file)
	funcName := "unknown"
	if fn := runtime.FuncForPC(pc); fn != nil {
		// 只保留函数名，不带包路径（如 doSomething）
		fullName := path.Base(fn.Name())
		parts := strings.Split(fullName, ".")
		funcName = parts[len(parts)-1]
	}
	return entry.WithField("caller", fmt.Sprintf("%s:%d[%s]", shortFile, line, funcName))
}

// GetLogger 获取日志实例
func GetLogger(c context.Context) *logrus.Entry {
	// if logger := c.Value(types.LoggerContextKey); logger != nil {
	// 	return logger.(*logrus.Entry)
	// }
	return logrus.NewEntry(appLogger)
}

// Fatalf 使用格式化字符串输出致命级别的日志并退出程序
func Fatalf(c context.Context, format string, args ...interface{}) {
	addCaller(GetLogger(c), 2).Fatalf(format, args...)
}

// Debugf 使用格式化字符串输出调试级别的日志
func Debugf(c context.Context, format string, args ...interface{}) {
	addCaller(GetLogger(c), 2).Debugf(format, args...)
}

// Warnf 使用格式化字符串输出警告级别的日志
func Warnf(c context.Context, format string, args ...interface{}) {
	addCaller(GetLogger(c), 2).Warnf(format, args...)
}

// Info 输出信息级别的日志
func Info(c context.Context, args ...interface{}) {
	addCaller(GetLogger(c), 2).Info(args...)
}

// Infof 使用格式化字符串输出信息级别的日志
func Infof(c context.Context, format string, args ...interface{}) {
	addCaller(GetLogger(c), 2).Infof(format, args...)
}

// Warn 输出警告级别的日志
func Warn(c context.Context, args ...interface{}) {
	addCaller(GetLogger(c), 2).Warn(args...)
}

// Error 输出错误级别的日志
func Error(c context.Context, args ...interface{}) {
	addCaller(GetLogger(c), 2).Error(args...)
}

// Errorf 使用格式化字符串输出错误级别的日志
func Errorf(c context.Context, format string, args ...interface{}) {
	addCaller(GetLogger(c), 2).Errorf(format, args...)
}

// ErrorWithFields 输出带有额外字段的错误级别日志
func ErrorWithFields(c context.Context, err error, fields logrus.Fields) {
	if fields == nil {
		fields = logrus.Fields{}
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	addCaller(GetLogger(c), 2).WithFields(fields).Error("发生错误")
}

// ANSI颜色代码
const (
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorPurple = "\033[35m"
	colorCyan   = "\033[36m"
	colorWhite  = "\033[37m"
	colorGray   = "\033[90m"
	colorBold   = "\033[1m"
	colorReset  = "\033[0m"
)

type CustomFormatter struct {
	ForceColor bool // 是否强制使用颜色，即使在非终端环境下
}

func (f *CustomFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	timestamp := entry.Time.Format("2006-01-02 15:04:05.000")
	level := strings.ToUpper(entry.Level.String())

	// 根据日志级别设置颜色
	var levelColor, resetColor string
	if f.ForceColor {
		switch entry.Level {
		case logrus.DebugLevel:
			levelColor = colorCyan
		case logrus.InfoLevel:
			levelColor = colorGreen
		case logrus.WarnLevel:
			levelColor = colorYellow
		case logrus.ErrorLevel:
			levelColor = colorRed
		case logrus.FatalLevel:
			levelColor = colorPurple
		default:
			levelColor = colorReset
		}
		resetColor = colorReset
	}

	// 取出 caller 字段
	caller := ""
	if val, ok := entry.Data["caller"]; ok {
		caller = fmt.Sprintf("%v", val)
	}

	// 拼接字段部分：request_id 优先，其他排序后输出
	fields := ""

	// request_id 优先输出
	if v, ok := entry.Data["request_id"]; ok {
		if f.ForceColor {
			fields += fmt.Sprintf("%s%v%s ",
				colorBlue, v, colorReset)
		} else {
			fields += fmt.Sprintf("%v ", v)
		}
	}

	// 其余字段排序后输出
	keys := make([]string, 0, len(entry.Data))
	for k := range entry.Data {
		if k != "caller" && k != "request_id" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if f.ForceColor {
			val := fmt.Sprintf("%v", entry.Data[k])
			coloredVal := fmt.Sprintf("%s%s%s", colorWhite, val, colorReset)
			if k == "error" {
				coloredVal = fmt.Sprintf("%s%s%s", colorRed, val, colorReset)
			}
			fields += fmt.Sprintf("%s%s%s=%s ",
				colorCyan, k, colorReset, coloredVal)
		} else {
			fields += fmt.Sprintf("%s=%v ", k, entry.Data[k])
		}
	}

	fields = strings.TrimSpace(fields)

	// 拼接最终输出内容，添加颜色
	if f.ForceColor {
		coloredTimestamp := fmt.Sprintf("%s%s%s", colorGray, timestamp, resetColor)
		coloredCaller := caller
		if caller != "" {
			coloredCaller = fmt.Sprintf("%s%s%s", colorPurple, caller, resetColor)
		}
		return []byte(fmt.Sprintf("%s%-5s%s[%s] [%s] %-20s | %s\n",
			levelColor, level, resetColor, coloredTimestamp, fields, coloredCaller, entry.Message)), nil
	}

	return []byte(fmt.Sprintf("%-5s[%s] [%s] %-20s | %s\n",
		level, timestamp, fields, caller, entry.Message)), nil
}

// 初始化全局日志设置
func init() {
	ConfigureFromEnv()
}

// 默认日志级别：未配置 log.level / LOG_LEVEL 时生效。
// 默认 info，因此 debug 日志需要显式开启（log.level: debug）。
const defaultLogLevel = logrus.InfoLevel

// 默认日志颜色模式：仅在 stdout 为终端时着色。
const defaultColorMode = "auto"

// LevelFromString 把字符串解析为 logrus.Level。
// 支持 trace/debug/info/warn(warning)/error/fatal/panic；
// 空值或未识别的取值返回默认级别。
func LevelFromString(s string) logrus.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return logrus.TraceLevel
	case "debug":
		return logrus.DebugLevel
	case "info":
		return logrus.InfoLevel
	case "warn", "warning":
		return logrus.WarnLevel
	case "error":
		return logrus.ErrorLevel
	case "fatal":
		return logrus.FatalLevel
	case "panic":
		return logrus.PanicLevel
	default:
		return defaultLogLevel
	}
}

// func defaultMacAppLogPath() string {
// 	execPath, err := os.Executable()
// 	if err != nil || !strings.Contains(execPath, ".app/Contents/MacOS") {
// 		return ""
// 	}

// 	homeDir, err := os.UserHomeDir()
// 	if err != nil {
// 		return ""
// 	}

// 	appName := "WeKnora Lite"
// 	if idx := strings.Index(execPath, ".app/Contents/MacOS"); idx >= 0 {
// 		bundleName := filepath.Base(execPath[:idx+4])
// 		if trimmed := strings.TrimSuffix(bundleName, ".app"); trimmed != "" {
// 			appName = trimmed
// 		}
// 	}

//		return filepath.Join(homeDir, "Library", "Logs", appName, appName+".log")
//	}
//
// defaultLogPath 使用统一外部路径规则定位 logs/app.log
func defaultLogPath() string {
	logPath, err := utils.ResolveExternalPath(filepath.Join("gobrave.log"))
	if err != nil {
		return ""
	}
	return logPath

}

func openLogFile(logPath string) (io.WriteCloser, error) {
	dir := filepath.Dir(logPath)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    50, // megabytes
		MaxBackups: 3,
		MaxAge:     28, // days
		Compress:   true,
	}, nil
}

// Options 描述一次日志重新配置所需的参数。
// 它由 config.yml 的 log 段映射而来，所有字段都可省略。
type Options struct {
	// Level 日志级别：trace/debug/info/warn/error/fatal/panic。
	Level string
	// Path 日志文件路径；为空时回退到默认路径（logs/app.log）。
	Path string
	// Color 颜色模式：auto（默认，仅终端着色）/always/never。
	Color string
}

// resolveLevel 计算最终生效的日志级别。
// LOG_LEVEL 环境变量优先于 log.level 配置；两者都未配置时使用默认级别。
func resolveLevel(opts Options) logrus.Level {
	if env := strings.TrimSpace(os.Getenv("LOG_LEVEL")); env != "" {
		return LevelFromString(env)
	}
	return LevelFromString(opts.Level)
}

// resolveColorMode 计算是否强制输出 ANSI 颜色。
// auto：仅在 stdout 为终端时着色（Docker 日志采集等非终端场景自动禁用）。
func resolveColorMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "always", "force", "true", "on":
		return true
	case "never", "false", "off":
		return false
	default:
		if fi, err := os.Stdout.Stat(); err == nil {
			return (fi.Mode() & os.ModeCharDevice) != 0
		}
		return false
	}
}

// Configure 使用给定参数重新配置全局日志。
//
// 优先级（低 → 高）：
//  1. Options 传入的取值（通常来自 config.yml 的 log 段）
//  2. LOG_LEVEL / LOG_PATH 环境变量
//
// 可重复调用（例如加载 config.yml 之后），每次都会关闭旧的日志文件句柄。
func Configure(opts Options) {
	loggerMu.Lock()
	defer loggerMu.Unlock()

	if activeLogFile != nil {
		_ = activeLogFile.Close()
		activeLogFile = nil
	}

	appLogger.SetLevel(resolveLevel(opts))

	writer := io.Writer(os.Stdout)
	logPath := strings.TrimSpace(opts.Path)
	if envPath := strings.TrimSpace(os.Getenv("LOG_PATH")); envPath != "" {
		logPath = filepath.Clean(envPath)
	}
	if logPath == "" {
		logPath = defaultLogPath()
	}
	if logPath != "" {
		file, err := openLogFile(logPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "logger: failed to open log file %s: %v\n", logPath, err)
		} else {
			activeLogFile = file
			writer = io.MultiWriter(os.Stdout, file)
		}
	}

	// 默认继续输出到 stdout，同时在可用时落盘到文件
	appLogger.SetOutput(writer)

	// 设置日志格式而不修改全局时区
	appLogger.SetFormatter(&CustomFormatter{ForceColor: resolveColorMode(opts.Color)})
	appLogger.SetReportCaller(false)
}

// ConfigureFromEnv 使用环境变量重新配置日志（向后兼容入口）。
// 这允许在 main() 中加载 .env 后，让 LOG_LEVEL / LOG_PATH 立即生效。
func ConfigureFromEnv() {
	Configure(Options{Color: defaultColorMode})
}
