package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// ListDatasetToolName 是「列出当前项目下数据集」工具名。
const ListDatasetToolName = "list_dataset"

// ListDatasetInput 是 list_dataset 工具的入参。
//
// ProjectID 不由模型提供：它取自执行事实（tool.ProjectIDFrom，当前用户激活项目）。
type ListDatasetInput struct{}

// ProjectDatasetItem 是单个数据集的摘要（工具回传给模型的字段）。
type ProjectDatasetItem = runtimecontext.DatasetBrief

// ListDatasetOutput 是 list_dataset 工具返回给模型的结果。
type ListDatasetOutput struct {
	ProjectID int64                `json:"project_id,string"`
	Total     int                  `json:"total"`
	Datasets  []ProjectDatasetItem `json:"datasets"`
	Message   string               `json:"message,omitempty"`
	Error     string               `json:"error,omitempty"`
}

// ListDataset 返回一个「列出当前项目下数据集」工具。
//
// 行为：
//   - 从执行事实取当前项目 ID（缺失时直接报错）；
//   - 通过构造注入的 DatasetBrowser 列出该项目下全部数据集；
//   - 每个数据集回传 dataset_id / dataset_name / description 与工作目录绝对路径。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func ListDataset(browser runtimecontext.DatasetBrowser) tool.Tool {
	return tool.NewFunc(
		ListDatasetToolName,
		"列出当前激活项目下的全部数据集。返回每个数据集的 dataset_id（字符串以保精度，可直接传给 get_dataset / update_dataset）、dataset_name（名称）、description（描述）与 path（数据集工作目录绝对路径，可直接在该目录下读写文件）。需要查看单个数据集详情时用 get_dataset；需要新建 / 修改时用 create_dataset / update_dataset。所属项目取自当前会话上下文，无需传入。",
		tool.Schema("列出当前项目数据集的入参（无需参数）", map[string]any{}),
		func(ctx context.Context, _ ListDatasetInput) (ListDatasetOutput, error) {
			projectID, err := requireProjectID(ctx, ListDatasetToolName)
			if err != nil {
				return ListDatasetOutput{}, err
			}
			if browser == nil {
				return ListDatasetOutput{}, fmt.Errorf("%s: dataset browser is not available in this runtime", ListDatasetToolName)
			}

			datasets, listErr := browser.ListDatasetsByProject(ctx, projectID)
			if listErr != nil {
				return ListDatasetOutput{
					ProjectID: projectID,
					Datasets:  []ProjectDatasetItem{},
					Error:     listErr.Error(),
				}, nil
			}

			out := ListDatasetOutput{
				ProjectID: projectID,
				Total:     len(datasets),
				Datasets:  datasets,
			}
			if out.Total == 0 {
				out.Message = "当前项目下还没有数据集。需要时可用 create_dataset 新建。"
			}
			return out, nil
		},
	).WithPermission("read", "", "列出当前项目下的全部数据集")
}
