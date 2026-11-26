package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// CreateDatasetToolName 是「在当前项目下创建数据集」工具名。
const CreateDatasetToolName = "create_dataset"

// CreateDatasetInput 是 create_dataset 工具的入参。
//
// ProjectID 不由模型提供：它取自执行事实（tool.ProjectIDFrom，当前用户激活项目），
// 避免模型把数据集创建到别的项目里。
type CreateDatasetInput struct {
	DatasetName string `json:"dataset_name"` // 数据集名称（必填）
	DatasetKey  string `json:"dataset_key"`  // 稳定业务 key（可选，普通数据集留空）
	Description string `json:"description"`  // 数据集描述（可选）
	Metadata    string `json:"metadata"`     // 元数据 JSON 文本（可选）
}

// CreateDatasetOutput 是 create_dataset 工具返回给模型的结果。
type CreateDatasetOutput struct {
	DatasetID   int64  `json:"dataset_id,string"`
	DatasetName string `json:"dataset_name,omitempty"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path,omitempty"`
	Error       string `json:"error,omitempty"`
}

// CreateDataset 返回一个「创建数据集」工具。
//
// 行为：
//   - 从执行事实取当前项目 ID（缺失时直接报错）；
//   - dataset_name 必填；dataset_key / description / metadata 可选；
//   - 通过注入的 DatasetListWriter 落库并绑定到当前项目，同时初始化数据集目录；
//   - 回传新数据集 ID 与工作目录绝对路径，模型可直接在该目录下读写文件。
//
// 该工具会创建业务数据（写库 + 初始化目录），因此声明 write 授权。
func CreateDataset(writer runtimecontext.DatasetListWriter) tool.Tool {
	return tool.NewFunc(
		CreateDatasetToolName,
		"在当前激活项目下创建一个新数据集，并初始化其工作目录。dataset_name 必填；dataset_key / description / metadata 可选（普通用户数据集通常留空 dataset_key）。返回 dataset_id（字符串以保精度）与 path（数据集工作目录绝对路径，可直接在该目录下读写文件）。创建完成后可用 add_file_to_dataset 把文件登记进数据集。所属项目取自当前会话上下文，无需传入。",
		tool.Schema("创建数据集的入参", map[string]any{
			"dataset_name": tool.StringProperty("数据集名称（必填）。"),
			"dataset_key":  tool.StringProperty("稳定业务 key（可选）。普通用户数据集留空；系统已知 key（如 DATABASES）由系统按需准备，通常无需手动创建。"),
			"description":  tool.StringProperty("数据集描述（可选）。"),
			"metadata":     tool.StringProperty("元数据 JSON 文本（可选）。"),
		}, "dataset_name"),
		func(ctx context.Context, in CreateDatasetInput) (CreateDatasetOutput, error) {
			projectID, err := requireProjectID(ctx, CreateDatasetToolName)
			if err != nil {
				return CreateDatasetOutput{}, err
			}
			name := strings.TrimSpace(in.DatasetName)
			if name == "" {
				return CreateDatasetOutput{}, fmt.Errorf("%s: dataset_name is required", CreateDatasetToolName)
			}
			if writer == nil {
				return CreateDatasetOutput{}, fmt.Errorf("%s: dataset writer is not available in this runtime", CreateDatasetToolName)
			}

			result, createErr := writer.CreateDataset(ctx, runtimecontext.DatasetCreateInput{
				ProjectID:   projectID,
				DatasetName: name,
				DatasetKey:  in.DatasetKey,
				Description: in.Description,
				Metadata:    in.Metadata,
			})
			if createErr != nil {
				return CreateDatasetOutput{Error: createErr.Error()}, nil
			}

			return CreateDatasetOutput{
				DatasetID:   result.DatasetID,
				DatasetName: result.DatasetName,
				Description: result.Description,
				Path:        result.Path,
			}, nil
		},
	).WithPermission("write", "", "在当前项目下创建数据集")
}
