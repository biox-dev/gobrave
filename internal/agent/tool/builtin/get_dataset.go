package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// GetDatasetToolName 是「按 @dataset:<id> 查看数据集」的工具名。
const GetDatasetToolName = "get_dataset"

// GetDatasetInput 是 get_dataset 工具的入参。
//
// DatasetID 对应用户消息里的 @dataset:<id> 引用（如 "@dataset:2109711590457610240"），
// 它是 go_dataset 主键；所属项目取自 RuntimeContext.ProjectID。
type GetDatasetInput struct {
	DatasetID string `json:"dataset_id"` // 数据集 ID（支持 "@dataset:<id>" 形式）
}

// GetDatasetOutput 是 get_dataset 工具返回给模型的结果。
//
// Path 是数据集工作目录的绝对路径（utils.GetDatasetDir），模型可直接在该目录下
// 读取 / 写入数据集文件。
type GetDatasetOutput struct {
	DatasetID   int64  `json:"dataset_id,string"`
	DatasetName string `json:"dataset_name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
	Error       string `json:"error,omitempty"`
}

// GetDataset 返回一个「按 ID 查看数据集」工具。
//
// 行为：
//   - 解析入参 dataset_id（容错剥离 @dataset: 前缀）；
//   - 项目归属取自 RuntimeContext.ProjectID，数据集必须绑定到当前激活项目；
//   - 回传数据集名称、描述与工作目录绝对路径。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func GetDataset() tool.Tool {
	return tool.NewFunc(
		GetDatasetToolName,
		"按 dataset_id 查看单个数据集。用于回答用户对 @dataset:<id> 引用的提问，或在分析前确认数据集位置。返回 dataset_name（数据集名称）、description（描述）与 path（数据集工作目录的绝对路径，可直接在该目录下读写文件）。数据集按当前激活项目隔离，不属于当前项目时会报错。",
		tool.Schema("按 ID 查看数据集的入参", map[string]any{
			"dataset_id": tool.StringProperty("数据集 ID（go_dataset 主键）。来自用户输入里的 @dataset:<id> 引用；字符串以保精度。"),
		}, "dataset_id"),
		func(ctx context.Context, in GetDatasetInput) (GetDatasetOutput, error) {
			projectID, err := requireProjectID(ctx, GetDatasetToolName)
			if err != nil {
				return GetDatasetOutput{}, err
			}

			datasetID, err := parseDatasetID(in.DatasetID)
			if err != nil {
				return GetDatasetOutput{}, fmt.Errorf("%s: %w", GetDatasetToolName, err)
			}

			browser := tool.ProjectResourceBrowserFrom(ctx)
			if browser == nil {
				return GetDatasetOutput{}, fmt.Errorf("%s: project resource browser is not available in this runtime", GetDatasetToolName)
			}

			dataset, err := browser.GetDatasetByID(ctx, projectID, datasetID)
			if err != nil {
				return GetDatasetOutput{
					DatasetID: datasetID,
					Error:     err.Error(),
				}, nil
			}

			return GetDatasetOutput{
				DatasetID:   dataset.DatasetID,
				DatasetName: dataset.DatasetName,
				Description: dataset.Description,
				Path:        dataset.Path,
			}, nil
		},
	).WithPermission("read", "", "按 ID 查看数据集名称 / 描述 / 工作目录")
}

// parseDatasetID 解析数据集 ID，容错剥离用户 / 模型可能带上的 "@dataset:" 前缀。
func parseDatasetID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idx := strings.Index(idStr, "@dataset:"); idx >= 0 {
		idStr = strings.TrimSpace(idStr[idx+len("@dataset:"):])
	}
	if idStr == "" {
		return 0, fmt.Errorf("dataset_id is required")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid dataset_id %q: %w", idStr, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("dataset_id must be a positive integer")
	}
	return id, nil
}
