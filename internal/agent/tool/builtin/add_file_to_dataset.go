package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// AddFileToDatasetToolName 是「把文件添加到数据集」的工具名。
const AddFileToDatasetToolName = "add_file_to_dataset"

// AddFileToDatasetInput 是 add_file_to_dataset 工具的入参。
//
// DatasetID 对应用户消息里的 @dataset:<id> 引用；Path 必须是文件路径（IsPrefix
// 为 true 时可以是前缀）。其余字段与 types.AddFileToDatasetRequest 语义一致。
type AddFileToDatasetInput struct {
	DatasetID string `json:"dataset_id"`          // 数据集 ID（支持 "@dataset:<id>" 形式）
	Path      string `json:"path"`                // 文件路径：绝对路径或相对数据集目录
	Role      string `json:"role,omitempty"`      // 文件角色，缺省 DEFAULT
	FileName  string `json:"file_name,omitempty"` // 展示文件名，缺省取 basename
	IsPrefix  bool   `json:"is_prefix,omitempty"`
	IsCopy    bool   `json:"is_copy,omitempty"`
}

// AddFileToDatasetOutput 是 add_file_to_dataset 工具返回给模型的结果。
type AddFileToDatasetOutput struct {
	DatasetID int64  `json:"dataset_id,string"`
	FileID    int64  `json:"file_id,string"`
	FilePath  string `json:"file_path"`
	FileName  string `json:"file_name"`
	Role      string `json:"role,omitempty"`
	Format    string `json:"format,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
}

// AddFileToDataset 返回一个「把文件添加到数据集」工具。
//
// 行为：
//   - 解析 dataset_id（容错剥离 @dataset: 前缀）；
//   - 项目取自执行事实（tool.ProjectIDFrom），数据集必须属于当前激活项目；
//   - path 为绝对路径时要求位于存储根目录内，相对路径相对数据集工作目录解析；
//   - 复用 AddFileToDataset 业务链路：登记（或复用）go_file 并建立
//     go_dataset_file 绑定，按 role 归档文件。
//
// 该工具会写库 / 可能复制文件，因此声明 write 授权，授权路径取入参 path。
func AddFileToDataset(writer runtimecontext.DatasetWriter) tool.Tool {
	return tool.NewFunc(
		AddFileToDatasetToolName,
		"把一个文件添加到数据集。传入 dataset_id（数据集 ID，支持 @dataset:<id>）与 path（文件路径）：绝对路径必须位于项目存储根目录内，相对路径相对数据集工作目录解析。可选 role（文件角色，如 BAM / VCF / DEFAULT，缺省 DEFAULT）、file_name（展示文件名，缺省取文件名）、is_prefix（把 path 当作前缀批量场景，不校验文件存在）、is_copy（先把文件复制到数据集目录再登记）。完成后文件会出现在该数据集的文件列表中。",
		tool.Schema("把文件添加到数据集的入参", map[string]any{
			"dataset_id": tool.StringProperty("数据集 ID（go_dataset 主键，字符串以保精度）。来自用户输入里的 @dataset:<id> 引用。"),
			"path":       tool.StringProperty("要添加的文件路径。绝对路径必须在项目存储根目录内；相对路径相对数据集工作目录（get_dataset 返回的 path）解析。"),
			"role":       tool.StringProperty("文件角色（go_file.role），如 BAM / VCF / DEFAULT；缺省 DEFAULT。"),
			"file_name":  tool.StringProperty("展示文件名；缺省取路径的 basename。"),
			"is_prefix":  tool.BooleanProperty("为 true 时把 path 当作前缀（批量目录导入场景），不校验文件是否存在。"),
			"is_copy":    tool.BooleanProperty("为 true 时先把文件复制到数据集目录（文件名加时间戳前缀）再登记。"),
		}, "dataset_id", "path"),
		func(ctx context.Context, in AddFileToDatasetInput) (AddFileToDatasetOutput, error) {
			projectID, err := requireProjectID(ctx, AddFileToDatasetToolName)
			if err != nil {
				return AddFileToDatasetOutput{}, err
			}

			datasetID, err := parseDatasetID(in.DatasetID)
			if err != nil {
				return AddFileToDatasetOutput{}, fmt.Errorf("%s: %w", AddFileToDatasetToolName, err)
			}

			if writer == nil {
				return AddFileToDatasetOutput{}, fmt.Errorf("%s: dataset writer is not available in this runtime", AddFileToDatasetToolName)
			}

			path := strings.TrimSpace(in.Path)
			if path == "" {
				return AddFileToDatasetOutput{}, fmt.Errorf("%s: path is required", AddFileToDatasetToolName)
			}

			result, err := writer.AddFileToDataset(ctx, runtimecontext.DatasetFileAddInput{
				ProjectID: projectID,
				DatasetID: datasetID,
				Path:      path,
				Role:      in.Role,
				FileName:  in.FileName,
				IsPrefix:  in.IsPrefix,
				IsCopy:    in.IsCopy,
			})
			if err != nil {
				return AddFileToDatasetOutput{
					DatasetID: datasetID,
					FilePath:  path,
					Error:     err.Error(),
				}, nil
			}

			return AddFileToDatasetOutput{
				DatasetID: result.DatasetID,
				FileID:    result.FileID,
				FilePath:  result.FilePath,
				FileName:  result.FileName,
				Role:      result.Role,
				Format:    result.Format,
				Size:      result.Size,
				Message:   "文件已添加到数据集。",
			}, nil
		},
	).WithPermission("write", "path", "把文件添加（登记）到数据集")
}
