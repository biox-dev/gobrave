package manager

import (
	"context"

	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// ResolveProjectID 校验项目数值主键（t_project.id）存在并返回它。
//
// 磁盘上的项目目录统一按项目主键组织（见 utils.GetProjectDir、utils.GetAISummaryFile 等），
// 而 Analysis / AnalysisNode / AISummary 记录里保存的也是这个数值主键，因此读写项目下的
// 文件前只需确认该项目存在即可。项目不存在时返回 0。
func ResolveProjectID(ctx context.Context, projectRepo interfaces.ProjectRepository, projectID int64) int64 {
	if projectRepo == nil || projectID == 0 {
		return 0
	}
	project, err := projectRepo.GetProjectByID(ctx, projectID)
	if err != nil || project == nil {
		logger.Warnf(ctx, "[Project] resolve project failed, project_id=%d err=%v", projectID, err)
		return 0
	}
	return project.ID
}
