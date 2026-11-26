package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// UpdateDatasetToolName 是「更新当前项目下数据集」工具名。
const UpdateDatasetToolName = "update_dataset"

// UpdateDatasetInput 是 update_dataset 工具的入参。
//
// DatasetID 必填；其余字段留空表示不修改该字段。
type UpdateDatasetInput struct {
	DatasetID   string `json:"dataset_id"`   // 数据集 ID（必填，支持 "@dataset:<id>" 形式）
	DatasetName string `json:"dataset_name"` // 新名称，留空表示不修改
	DatasetKey  string `json:"dataset_key"`  // 新 key，留空表示不修改
	Description string `json:"description"`  // 新描述，留空表示不修改
	Metadata    string `json:"metadata"`     // 新元数据 JSON 文本，留空表示不修改
}

// UpdateDatasetOutput 是 update_dataset 工具返回给模型的结果。
type UpdateDatasetOutput struct {
	DatasetID   int64  `json:"dataset_id,string"`
	DatasetName string `json:"dataset_name,omitempty"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path,omitempty"`
	Error       string `json:"error,omitempty"`
}

// UpdateDataset 返回一个「更新数据集名称 / 描述 / key」工具。
//
// 行为：
//   - dataset_id 必填（容错剥离 @dataset: 前缀）；
//   - 只覆盖本次给出的字段，未给出的字段保持原值；
//   - 数据集必须属于当前激活项目（按 go_project_dataset 绑定校验），否则报错；
//   - 回传更新后的数据集信息与工作目录绝对路径。
//
// 该工具会修改业务数据（写库），因此声明 write 授权。
func UpdateDataset(writer runtimecontext.DatasetListWriter) tool.Tool {
	return tool.NewFunc(
		UpdateDatasetToolName,
		"更新当前激活项目下已有数据集的元数据。dataset_id 必填（来自 list_dataset 返回的 dataset_id，或用户输入里的 @dataset:<id>）。可同时修改 dataset_name / dataset_key / description / metadata，未给出的字段保持原值。返回更新后的数据集信息与 path（工作目录绝对路径）。数据集必须属于当前项目，跨项目会报错。",
		tool.Schema("更新数据集的入参", map[string]any{
			"dataset_id":   tool.StringProperty("数据集 ID（必填，字符串以保精度）。来自 list_dataset 返回的 dataset_id 或用户输入里的 @dataset:<id>。"),
			"dataset_name": tool.StringProperty("新的数据集名称。留空表示不修改。"),
			"dataset_key":  tool.StringProperty("新的稳定业务 key。留空表示不修改。"),
			"description":  tool.StringProperty("新的数据集描述。留空表示不修改。"),
			"metadata":     tool.StringProperty("新的元数据 JSON 文本。留空表示不修改。"),
		}, "dataset_id"),
		func(ctx context.Context, in UpdateDatasetInput) (UpdateDatasetOutput, error) {
			projectID, err := requireProjectID(ctx, UpdateDatasetToolName)
			if err != nil {
				return UpdateDatasetOutput{}, err
			}
			datasetID, err := parseDatasetID(in.DatasetID)
			if err != nil {
				return UpdateDatasetOutput{}, fmt.Errorf("%s: %w", UpdateDatasetToolName, err)
			}
			if writer == nil {
				return UpdateDatasetOutput{}, fmt.Errorf("%s: dataset writer is not available in this runtime", UpdateDatasetToolName)
			}
			if strings.TrimSpace(in.DatasetName) == "" && strings.TrimSpace(in.DatasetKey) == "" &&
				in.Description == "" && in.Metadata == "" {
				return UpdateDatasetOutput{}, fmt.Errorf("%s: at least one of dataset_name / dataset_key / description / metadata must be provided", UpdateDatasetToolName)
			}

			result, updateErr := writer.UpdateDataset(ctx, runtimecontext.DatasetUpdateInput{
				ProjectID:   projectID,
				DatasetID:   datasetID,
				DatasetName: in.DatasetName,
				DatasetKey:  in.DatasetKey,
				Description: in.Description,
				Metadata:    in.Metadata,
			})
			if updateErr != nil {
				return UpdateDatasetOutput{DatasetID: datasetID, Error: updateErr.Error()}, nil
			}

			return UpdateDatasetOutput{
				DatasetID:   result.DatasetID,
				DatasetName: result.DatasetName,
				Description: result.Description,
				Path:        result.Path,
			}, nil
		},
	).WithPermission("write", "", "更新当前项目下数据集")
}
