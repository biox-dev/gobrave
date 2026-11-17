package builtin

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// GetFileToolName 是「按文件 ID 读取文件内容」工具名。
const GetFileToolName = "get_file"

const (
	// readFileDefaultLimit 未显式传 limit 时最多读取的行数。
	readFileDefaultLimit = 2000
	// readFileMaxLineLength 是单行最大字符数，超出部分截断，避免超宽表格行占满上下文。
	readFileMaxLineLength = 2000
	// readFileMaxBytes 是单次回传 content 的字节预算，超出后提前结束并要求用 offset 续读。
	readFileMaxBytes = 200 * 1024
	// readFileSniffSize 是判定二进制文件时探测的字节数。
	readFileSniffSize = 8192
)

// GetFileInput 是 get_file 工具的入参。
//
// FileID 对应用户输入里的 @file:<id> 引用（如 "@file:2109711590457610240"），
// 它是 go_file 主键 ID，等价于一条文件路径。按内置工具的统一口径，模型应传冒号后的
// 裸 ID（不传 @file: 前缀）；前缀只作容错兜底。
type GetFileInput struct {
	FileID string `json:"file_id"` // 文件 ID（go_file 主键，字符串以保精度）
	Offset int    `json:"offset"`  // 起始行号（0-based）
	Limit  int    `json:"limit"`   // 最多读取的行数
}

// GetFileOutput 是 get_file 工具返回给模型的结果。
//
// 无论格式如何都会返回 FilePath（真实绝对路径，可直接作为分析节点输入参数或传给
// view / edit / bash）；文本文件额外返回 Content，xlsx / xlsm 返回各 sheet 的列名与
// 预览行，图片与其它二进制文件只返回 file_path 与 format 并在 Message 里说明原因。
type GetFileOutput struct {
	FileID   int64  `json:"file_id,string"`
	FileName string `json:"file_name,omitempty"`
	FilePath string `json:"file_path"`
	Format   string `json:"format,omitempty"`
	// Offset / LineCount / NextOffset 描述本次读取的行窗口：Content 是
	// [offset, offset+line_count) 这些行；HasMore 为 true 时用 NextOffset 续读。
	Offset     int    `json:"offset,omitempty"`
	LineCount  int    `json:"line_count,omitempty"`
	NextOffset int    `json:"next_offset,omitempty"`
	HasMore    bool   `json:"has_more,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	Content    string `json:"content,omitempty"`
	Message    string `json:"message,omitempty"`
	Error      string `json:"error,omitempty"`
}

// getFileDescription 是工具描述（模型选工具 / 正确传参的唯一依据）。
const getFileDescription = `按 file_id 读取文件内容，用于处理用户消息里的 @file:<id> 引用（如“这个文件有哪些列 / 内容是什么”）。

## 入参口径

用户输入里的 @file:<id> 是界面插入的引用 chip：**直接把冒号后的数字作为 file_id 传入**（例如 @file:2109711590457610240 → file_id 传 "2109711590457610240"），不要传前缀，也不要把整串 chip 当作文件路径。

## 返回字段

- **file_path**：文件的真实绝对路径，可直接作为分析节点输入参数，或继续用 view / edit / bash 处理。
- **format**：文件格式（如 csv / tsv / xlsx / bam）。
- **content**：文本文件的内容（csv / tsv 首行即列名）；xlsx / xlsm 返回各 sheet 的列名与前若干行预览。图片与其它二进制文件不含 content，会在 message 里说明，此时用 file_path 交给容器命令读取。
- **offset / line_count / next_offset / has_more / truncated**：大文件分批读取信息；has_more 为 true 时用 next_offset 继续读。

## 限制

- 单次最多返回约 200KB 文本、默认 2000 行；单行超过 2000 字符会被截断（truncated=true）。
- 无法按 ID 逐行定位大文件时，先读结构（列名 / 前若干行），再用 file_path 走 bash / 脚本处理。`

// GetFile 返回一个「按文件 ID 读取文件内容」工具。
//
// 行为：
//   - 解析入参 file_id（容错剥离 @file: 前缀，模型应传裸 ID）；
//   - 通过注入的 FileReader 按 ID 查出文件元信息（含真实路径）；
//   - 文本文件回传内容（可按 offset / limit 分批），xlsx 回传 sheet 预览，
//     图片 / 二进制只回传路径与格式；
//   - 文件不存在或不可读时回填 error 字段而不是直接失败，便于模型改用其它方式。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func GetFile(reader runtimecontext.FileReader) tool.Tool {
	return tool.NewFunc(
		GetFileToolName,
		getFileDescription,
		tool.Schema("按文件 ID 读取文件内容的入参", map[string]any{
			"file_id": tool.StringProperty("文件 ID（go_file 主键，字符串以保精度）。用户输入里有 @file:<id> 引用时传冒号后的裸数字 ID，不要带 @file: 前缀。"),
			"offset":  tool.IntegerProperty("起始行号（0-based，默认 0）"),
			"limit":   tool.IntegerProperty("最多读取的行数（默认 2000）"),
		}, "file_id"),
		func(ctx context.Context, in GetFileInput) (GetFileOutput, error) {
			fileID, err := parseFileID(in.FileID)
			if err != nil {
				return GetFileOutput{}, fmt.Errorf("%s: %w", GetFileToolName, err)
			}

			if reader == nil {
				return GetFileOutput{}, fmt.Errorf("%s: file reader is not available in this runtime", GetFileToolName)
			}

			result, err := reader.ReadFileByID(ctx, fileID)
			if err != nil {
				return GetFileOutput{
					FileID: fileID,
					Error:  err.Error(),
				}, nil
			}

			out := GetFileOutput{
				FileID:   fileID,
				FileName: result.FileName,
				FilePath: result.FilePath,
				Format:   normalizeFileFormat(result.Format, result.FilePath),
			}
			if out.FilePath == "" {
				out.Error = fmt.Sprintf("file %d has no readable path", fileID)
				return out, nil
			}

			if err := fillFileContent(ctx, reader, &out, in.Offset, in.Limit); err != nil {
				out.Error = err.Error()
			}
			return out, nil
		},
	).WithPermission("read", "", "按文件 ID 读取文件内容")
}

// fillFileContent 按文件格式填充 Content / Message 与分批读取字段。
//
// 三种分支：
//   - xlsx / xlsm：交给 FileReader.ReadSpreadsheet 渲染 sheet 列名与预览行；
//   - 图片 / 二进制：不回传内容，只给出 file_path 与说明；
//   - 文本：按 offset / limit 逐行读取，受字节预算约束。
func fillFileContent(ctx context.Context, reader runtimecontext.FileReader, out *GetFileOutput, offset, limit int) error {
	info, err := os.Stat(out.FilePath)
	if err != nil {
		return fmt.Errorf("cannot access %s: %w", out.FilePath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory, not a file", out.FilePath)
	}

	switch out.Format {
	case "xlsx", "xlsm":
		preview, err := reader.ReadSpreadsheet(ctx, out.FilePath)
		if err != nil {
			return fmt.Errorf("failed to preview %s: %w", out.FilePath, err)
		}
		out.Content = preview
		return nil
	}

	if isImageFormat(out.Format) {
		out.Message = "图片文件，无法以文本展示；file_path 可直接交给容器命令或其它工具处理。"
		return nil
	}

	binary, err := looksBinary(out.FilePath)
	if err != nil {
		return err
	}
	if binary {
		out.Message = "二进制文件，无法以文本展示；file_path 可直接作为分析节点输入参数或交给容器命令处理。"
		return nil
	}

	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = readFileDefaultLimit
	}

	lines, hasMore, truncated, err := readFileLines(out.FilePath, offset, limit)
	if err != nil {
		return err
	}
	out.Offset = offset
	out.LineCount = len(lines)
	out.HasMore = hasMore
	out.Truncated = truncated
	if hasMore {
		out.NextOffset = offset + len(lines)
	}
	out.Content = strings.Join(lines, "\n")

	if len(lines) == 0 && !hasMore && offset > 0 {
		out.Message = fmt.Sprintf("offset %d 超出文件行数，文件已读完。", offset)
	}
	return nil
}

// readFileLines 从 offset 行开始读取最多 limit 行。
//
// hasMore 表示文件在本次读取窗口之后还有内容（limit 或字节预算触顶）；
// truncated 表示有行因超长被截断。字节预算 readFileMaxBytes 用于控制单次回传体积。
func readFileLines(path string, offset, limit int) (lines []string, hasMore, truncated bool, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, false, fmt.Errorf("cannot open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for i := 0; i < offset && scanner.Scan(); i++ {
	}
	if err := scanner.Err(); err != nil {
		return nil, false, false, fmt.Errorf("failed reading %s: %w", path, err)
	}

	readBytes := 0
	for scanner.Scan() {
		if len(lines) >= limit {
			// 还有未消费的行，说明本次窗口没读到底。
			hasMore = true
			break
		}

		line := strings.TrimSuffix(scanner.Text(), "\r")
		if len(line) > readFileMaxLineLength {
			line = line[:readFileMaxLineLength] + "..."
			truncated = true
		}
		if readBytes+len(line)+1 > readFileMaxBytes {
			hasMore = true
			break
		}
		lines = append(lines, line)
		readBytes += len(line) + 1
	}
	if err := scanner.Err(); err != nil {
		return nil, false, false, fmt.Errorf("failed reading %s: %w", path, err)
	}
	return lines, hasMore, truncated, nil
}

// looksBinary 通过探测文件头是否含 NUL 字节判断是否为二进制文件。
func looksBinary(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("cannot open %s: %w", path, err)
	}
	defer file.Close()

	buf := make([]byte, readFileSniffSize)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("failed reading %s: %w", path, err)
	}
	return bytes.IndexByte(buf[:n], 0) >= 0, nil
}

// normalizeFileFormat 归一化文件格式：优先取元信息里的 format，为空时回退到路径后缀。
func normalizeFileFormat(format, path string) string {
	normalized := strings.TrimSpace(strings.ToLower(strings.TrimPrefix(format, ".")))
	if normalized == "" {
		normalized = strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}
	return normalized
}

// isImageFormat 判断是否为无法以文本展示的图片格式。
func isImageFormat(format string) bool {
	switch format {
	case "jpg", "jpeg", "png", "gif", "bmp", "svg", "webp":
		return true
	default:
		return false
	}
}

// parseFileID 解析必填的文件 ID：为空或非法时返回错误。
//
// 容错处理用户 / 模型可能带上的 "@file:" 引用前缀（如 "@file:2109711590457610240"）：
// 该前缀只出现在用户输入里，模型按内置工具统一口径应传裸 ID，这里统一剥掉兜底。
func parseFileID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idx := strings.Index(idStr, "@file:"); idx >= 0 {
		idStr = strings.TrimSpace(idStr[idx+len("@file:"):])
	}
	if idStr == "" {
		return 0, fmt.Errorf("file_id is required")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid file_id %q: %w", idStr, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("file_id must be a positive integer")
	}
	return id, nil
}
