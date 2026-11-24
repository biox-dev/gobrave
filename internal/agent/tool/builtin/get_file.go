package builtin

import (
	"context"
	"fmt"
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
	readFileDefaultLimit = 5
	// readFileMaxBytes 是单次回传 content 的字节预算，超出后提前结束并要求用 offset 续读。
	readFileMaxBytes = 200 * 1024
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
// view / edit / bash）；文本文件额外返回 Content，xlsx / xlsm 返回首个工作表的 TSV
// 文本，图片与其它二进制文件只返回 file_path 与 format 并在 Message 里说明原因。
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
- **content**：文本文件的内容（csv / tsv 首行即列名）；xlsx / xlsm 返回首个工作表的 TSV 文本。图片与其它二进制文件不含 content，会在 message 里说明，此时用 file_path 交给容器命令读取。
- **offset / line_count / next_offset / has_more / truncated**：大文件分批读取信息；has_more 为 true 时用 next_offset 继续读。

## 限制

- 单次最多返回约 200KB 文本、默认 5 行；单行超过 2000 字符会被截断（truncated=true）。
- 无法按 ID 逐行定位大文件时，先读结构（列名 / 前若干行），再用 file_path 走 bash / 脚本处理。`

// GetFile 返回一个「按文件 ID 读取文件内容」工具。
//
// 行为：
//   - 解析入参 file_id（容错剥离 @file: 前缀，模型应传裸 ID）；
//   - 通过注入的 FileReader 按 ID 查出文件元信息（含真实路径 / format）；
//   - 交给 FileReader.ReadFileContent 读取内容（可按 offset / limit 分批，并以
//     文件元信息里的 format 判定解析方式），文本 / 表格回传内容，图片 / 二进制只回传路径与格式；
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
			"limit":   tool.IntegerProperty("最多读取的行数（默认 5）"),
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

// fillFileContent 读取文件内容并填充 Content / Message 与分批读取字段。
//
// 解析细节（表格转分隔文本、二进制 / 图片识别、按 offset / limit 分页）由
// FileReader.ReadFileContent 统一处理，工具只透传分页与字节预算。
func fillFileContent(ctx context.Context, reader runtimecontext.FileReader, out *GetFileOutput, offset, limit int) error {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = readFileDefaultLimit
	}

	result, err := reader.ReadFileContent(ctx, runtimecontext.FileContentRequest{
		// Format 取 go_file.format（normalizeFileFormat 已带路径后缀兜底），
		// 让表格 / Excel / 二进制的判定以文件元信息为准，而不是只看路径后缀。
		Path:     out.FilePath,
		Format:   out.Format,
		Offset:   offset,
		Limit:    limit,
		MaxBytes: readFileMaxBytes,
	})
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", out.FilePath, err)
	}

	out.Offset = result.Offset
	out.LineCount = result.LineCount
	out.NextOffset = result.NextOffset
	out.HasMore = result.HasMore
	out.Truncated = result.Truncated
	out.Content = result.Content
	out.Message = result.Message
	return nil
}

// normalizeFileFormat 归一化文件格式：优先取元信息里的 format，为空时回退到路径后缀。
func normalizeFileFormat(format, path string) string {
	normalized := strings.TrimSpace(strings.ToLower(strings.TrimPrefix(format, ".")))
	if normalized == "" {
		normalized = strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}
	return normalized
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
