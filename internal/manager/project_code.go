package manager

import (
	"context"
	"strings"

	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// ResolveProjectCode 将项目的数值主键（t_project.id）解析为磁盘目录使用的项目标识
// （t_project.project_id，见 types.Project.ProjectID）。
//
// 磁盘上的项目目录统一按 t_project.project_id 组织（见 utils.GetProjectDir、
// utils.GetAISummaryFile 等），而 Analysis / AnalysisNode / AISummary 记录里保存的是
// 数值主键，因此读写项目下的文件前需要用本方法把数值 ID 换成字符串标识。
// 项目不存在或标识为空时返回空串。
func ResolveProjectCode(ctx context.Context, projectRepo interfaces.ProjectRepository, projectID int64) string {
	if projectRepo == nil || projectID == 0 {
		return ""
	}
	project, err := projectRepo.GetProjectByID(ctx, projectID)
	if err != nil || project == nil {
		logger.Warnf(ctx, "[Project] resolve project code failed, project_id=%d err=%v", projectID, err)
		return ""
	}
	return strings.TrimSpace(project.ProjectID)
}
