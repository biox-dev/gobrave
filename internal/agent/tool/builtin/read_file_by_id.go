package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// ReadFileByIDToolName 是「根据文件 ID 读取文件」工具名。
const ReadFileByIDToolName = "read_file_by_id"

// ReadFileByIDInput 是 read_file_by_id 工具的入参。
//
// FileID 对应用户输入里的 @file:<id> 引用（如 "@file:2109711590457610240"），它是
// go_file 主键 ID，等价于一条文件路径。
type ReadFileByIDInput struct {
	FileID string `json:"file_id"` // 文件 ID（go_file 主键，字符串以保精度）
}

// ReadFileByIDOutput 是 read_file_by_id 工具返回给模型的结果。
//
// 无论格式如何都会返回 FilePath；对 tsv / csv / xlsx（全部 sheet）额外返回每个 sheet
// 的列名（Columns）与前若干行预览数据（Rows）。
type ReadFileByIDOutput struct {
	FileID   int64              `json:"file_id,string"`
	FileName string             `json:"file_name,omitempty"`
	FilePath string             `json:"file_path"`
	Format   string             `json:"format,omitempty"`
	Sheets   []ReadFileSheetTab `json:"sheets,omitempty"`
	Message  string             `json:"message,omitempty"`
	Error    string             `json:"error,omitempty"`
}

// ReadFileSheetTab 是单个 sheet / 数据表的内容预览。
type ReadFileSheetTab struct {
	Name    string           `json:"name"`
	Columns []string         `json:"columns,omitempty"`
	Rows    []map[string]any `json:"rows,omitempty"`
}

// ReadFileByID 返回一个「根据文件 ID 读取文件」工具。
//
// 行为：
//   - 解析入参 file_id（对应用户输入里的 @file:<id> 引用）；
//   - 通过注入的 FileReader 按 ID 读取文件元信息与内容预览；
//   - 对 tsv / csv / xlsx（全部 sheet）返回列名与前若干行数据，其它格式仅返回文件路径。
//
// 该工具是只读的，因此声明 read 授权：路径取自入参 file_id 解析后的文件，上层在执行前
// 会先走权限门禁。
func ReadFileByID() tool.Tool {
	return tool.NewFunc(
		ReadFileByIDToolName,
		"根据文件 ID 读取文件内容。用于回答用户对 @file:<id> 引用文件的提问（如“这个文件有什么列/内容是什么”）。对 tsv/csv/xlsx 返回所有列名与最多前 5 行数据，其它格式只返回文件路径。",
		tool.Schema("根据文件 ID 读取文件的入参", map[string]any{
			"file_id": tool.StringProperty("文件 ID（go_file 主键，字符串以保精度）。来自用户输入里的 @file:<id> 引用。"),
		}, "file_id"),
		func(ctx context.Context, in ReadFileByIDInput) (ReadFileByIDOutput, error) {
			fileID, err := parseFileID(in.FileID)
			if err != nil {
				return ReadFileByIDOutput{}, err
			}

			reader := tool.FileReaderFrom(ctx)
			if reader == nil {
				return ReadFileByIDOutput{}, fmt.Errorf("read_file_by_id: file reader is not available in this runtime")
			}

			result, err := reader.ReadFileByID(ctx, fileID)
			if err != nil {
				return ReadFileByIDOutput{
					FileID: fileID,
					Error:  err.Error(),
				}, nil
			}

			out := ReadFileByIDOutput{
				FileID:   result.FileID,
				FileName: result.FileName,
				FilePath: result.FilePath,
				Format:   result.Format,
				Message:  result.Message,
			}
			if len(result.Sheets) > 0 {
				out.Sheets = make([]ReadFileSheetTab, 0, len(result.Sheets))
				for _, sheet := range result.Sheets {
					out.Sheets = append(out.Sheets, ReadFileSheetTab{
						Name:    sheet.Name,
						Columns: sheet.Columns,
						Rows:    sheet.Rows,
					})
				}
			}
			return out, nil
		},
	).WithPermission("read", "", "根据文件 ID 读取文件内容")
}

// parseFileID 解析文件 ID 字符串。
//
// 容错处理用户 / 模型可能带上 "@file:" 前缀（如 "@file:2109711590457610240"）或首尾空白。
func parseFileID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	// 容错：剥离可能的 @file: 前缀。
	if idx := strings.Index(idStr, "@file:"); idx >= 0 {
		idStr = strings.TrimSpace(idStr[idx+len("@file:"):])
	}
	if idStr == "" {
		return 0, fmt.Errorf("read_file_by_id: file_id is required")
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("read_file_by_id: invalid file_id %q: %w", idStr, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("read_file_by_id: file_id must be a positive integer")
	}
	return id, nil
}
