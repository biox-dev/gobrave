package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/types"
)

// GetProjectDatabasePathToolName 是「获取项目默认数据库目录」的工具名。
const GetProjectDatabasePathToolName = "get_project_database_path"

// GetProjectDatabasePathInput 是 get_project_database_path 工具的入参。
//
// 固定使用当前激活项目，不接受模型传入 ID，避免读到别的项目的数据库目录。
type GetProjectDatabasePathInput struct{}

// GetProjectDatabasePathOutput 是 get_project_database_path 工具返回给模型的结果。
//
// Path 是数据集工作目录的绝对路径（utils.GetDatasetDir），模型可直接在该目录下
// 读取 / 写入参考数据库文件（如 reference genome、索引、注释库）。
type GetProjectDatabasePathOutput struct {
	ProjectID   int64  `json:"project_id,string"`
	DatasetID   int64  `json:"dataset_id,string"`
	DatasetKey  string `json:"dataset_key"`
	DatasetName string `json:"dataset_name"`
	Path        string `json:"path"`
	// Created 表示本次调用是否新建了 DATABASES 数据集。
	Created bool   `json:"created"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// GetProjectDatabasePath 返回一个「获取项目默认数据库目录」工具。
//
// 行为：
//   - 项目取自执行事实（tool.ProjectIDFrom）；
//   - 查找 dataset_key == types.DatasetKeyDatabases（"DATABASES"）的数据集，
//     不存在时自动创建并绑定到当前项目；
//   - 回传该数据集工作目录的绝对路径，模型可直接把参考数据库放在这里复用。
//
// 该工具会创建数据集（写库），因此声明 write 授权。
func GetProjectDatabasePath(writer runtimecontext.DatasetWriter) tool.Tool {
	return tool.NewFunc(
		GetProjectDatabasePathToolName,
		"获取当前项目的默认数据库目录。用于需要参考数据库（reference genome、索引、注释库等）的分析场景：返回的 path 是项目数据库数据集（dataset_key=DATABASES）工作目录的绝对路径，可直接在该目录下读取 / 写入数据库文件。若当前项目还没有该数据集，会自动创建并绑定到项目。所属项目取自当前会话上下文，无需传入。",
		tool.Schema("获取项目默认数据库目录的入参（无需参数）", map[string]any{}),
		func(ctx context.Context, _ GetProjectDatabasePathInput) (GetProjectDatabasePathOutput, error) {
			projectID, err := requireProjectID(ctx, GetProjectDatabasePathToolName)
			if err != nil {
				return GetProjectDatabasePathOutput{}, err
			}

			if writer == nil {
				return GetProjectDatabasePathOutput{}, fmt.Errorf("%s: dataset writer is not available in this runtime", GetProjectDatabasePathToolName)
			}

			dataset, created, err := writer.EnsureDatasetByKey(ctx, projectID, types.DatasetKeyDatabases)
			if err != nil {
				return GetProjectDatabasePathOutput{
					ProjectID:  projectID,
					DatasetKey: types.DatasetKeyDatabases,
					Error:      err.Error(),
				}, nil
			}

			out := GetProjectDatabasePathOutput{
				ProjectID:   projectID,
				DatasetID:   dataset.DatasetID,
				DatasetKey:  types.DatasetKeyDatabases,
				DatasetName: dataset.DatasetName,
				Path:        dataset.Path,
				Created:     created,
			}
			if created {
				out.Message = "当前项目还没有数据库数据集，已自动创建。"
			}
			return out, nil
		},
	).WithPermission("write", "", "获取（必要时创建）项目默认数据库目录 DATABASES")
}
