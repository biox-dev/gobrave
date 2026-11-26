package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// UpdateFileToolName 是「更新当前项目下文件元数据」工具名。
const UpdateFileToolName = "update_file"

// UpdateFileInput 是 update_file 工具的入参。所有字段留空表示不修改。
type UpdateFileInput struct {
	FileID      string `json:"file_id"`     // 文件 ID（必填，go_file 主键）
	FileName    string `json:"file_name"`   // 新展示文件名
	Path        string `json:"path"`        // 新物理路径
	Format      string `json:"format"`      // 新格式
	FileKey     string `json:"file_key"`    // 新 file_key
	Role        string `json:"role"`        // 新角色
	Storage     string `json:"storage"`     // 新存储类型
	Description string `json:"description"` // 新描述
}

// UpdateFileOutput 是 update_file 工具返回给模型的结果。
type UpdateFileOutput struct {
	FileID      int64    `json:"file_id,string"`
	FileName    string   `json:"file_name,omitempty"`
	Path        string   `json:"path,omitempty"`
	Format      string   `json:"format,omitempty"`
	Role        string   `json:"role,omitempty"`
	FileKey     string   `json:"file_key,omitempty"`
	Description string   `json:"description,omitempty"`
	Changed     []string `json:"changed,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// UpdateFile 返回一个「更新文件元数据」工具。
//
// 行为：
//   - file_id 必填（容错剥离 @file: 前缀）；
//   - 只覆盖本次给出的字段，未给出的字段保持原值；
//   - 文件必须属于当前激活项目（按项目文件列表校验），否则报错；
//   - 回传更新后的文件字段与本次实际改动的字段列表。
//
// 该工具会修改业务数据（写库），因此声明 write 授权。
func UpdateFile(updater runtimecontext.FileUpdater) tool.Tool {
	return tool.NewFunc(
		UpdateFileToolName,
		"更新当前激活项目下文件的元数据（go_file）。file_id 必填（来自 list_file / get_file 返回的 file_id，或用户输入里的 @file:<id>）。可修改 file_name / path / format / file_key / role / storage / description，未给出的字段保持原值。role 取值为 DEFAULT / TABLE / DATABASE。返回更新后的字段与本次实际改动的字段列表（changed）。文件必须属于当前项目，跨项目会报错。仅改元数据，不移动 / 不复制物理文件。",
		tool.Schema("更新文件元数据的入参", map[string]any{
			"file_id":     tool.StringProperty("文件 ID（必填，go_file 主键，字符串以保精度）。支持 @file:<id> 形式。"),
			"file_name":   tool.StringProperty("新的展示文件名。留空表示不修改。"),
			"path":        tool.StringProperty("新的物理路径。留空表示不修改（仅改数据库记录，不移动物理文件）。"),
			"format":      tool.StringProperty("新的文件格式（如 bam / vcf / csv）。留空表示不修改。"),
			"file_key":    tool.StringProperty("新的 file_key（文件在其 assay 内的键，如 FASTQ_R1 / BAM）。留空表示不修改。"),
			"role":        tool.StringProperty("新的角色（go_file.role），可选值 DEFAULT / TABLE / DATABASE。留空表示不修改。"),
			"storage":     tool.StringProperty("新的存储类型（如 LOCAL）。留空表示不修改。"),
			"description": tool.StringProperty("新的文件描述。留空表示不修改。"),
		}, "file_id"),
		func(ctx context.Context, in UpdateFileInput) (UpdateFileOutput, error) {
			projectID, err := requireProjectID(ctx, UpdateFileToolName)
			if err != nil {
				return UpdateFileOutput{}, err
			}
			fileID, err := parseFileID(in.FileID)
			if err != nil {
				return UpdateFileOutput{}, fmt.Errorf("%s: %w", UpdateFileToolName, err)
			}
			if updater == nil {
				return UpdateFileOutput{}, fmt.Errorf("%s: file updater is not available in this runtime", UpdateFileToolName)
			}
			if strings.TrimSpace(in.FileName) == "" && strings.TrimSpace(in.Path) == "" &&
				strings.TrimSpace(in.Format) == "" && strings.TrimSpace(in.FileKey) == "" &&
				strings.TrimSpace(in.Role) == "" && strings.TrimSpace(in.Storage) == "" &&
				in.Description == "" {
				return UpdateFileOutput{}, fmt.Errorf("%s: at least one field to update must be provided", UpdateFileToolName)
			}

			result, updateErr := updater.UpdateFile(ctx, runtimecontext.FileUpdateInput{
				ProjectID:   projectID,
				FileID:      fileID,
				FileName:    in.FileName,
				Path:        in.Path,
				Format:      in.Format,
				FileKey:     in.FileKey,
				Role:        in.Role,
				Storage:     in.Storage,
				Description: in.Description,
			})
			if updateErr != nil {
				return UpdateFileOutput{FileID: fileID, Error: updateErr.Error()}, nil
			}

			return UpdateFileOutput{
				FileID:      result.FileID,
				FileName:    result.FileName,
				Path:        result.Path,
				Format:      result.Format,
				Role:        string(result.Role),
				FileKey:     result.FileKey,
				Description: result.Description,
				Changed:     result.Changed,
			}, nil
		},
	).WithPermission("write", "", "更新当前项目下文件元数据")
}
