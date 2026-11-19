package handler

import (
	"context"

	apperrors "github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// ensureProjectAccessible 校验当前用户是否可以访问某个项目下的资源。
//
// 项目资源（会话、脚本、分析等）按项目共享：只要当前用户是该项目成员
// （user_project 中登记过），就可以访问；否则返回明确的 403 错误。
// 资源没有归属项目（projectID == 0）时无法判定成员身份，同样拒绝访问。
//
// 项目成员列表由 ProjectService.ListProjectUserIDs 统一提供，调用方只需传入
// 资源所属的 projectID，不必关心具体的资源类型。
func ensureProjectAccessible(ctx context.Context, projects interfaces.ProjectService, projectID, userID int64) error {
	if projectID <= 0 {
		return apperrors.NewForbiddenError("resource is not bound to a project; access denied")
	}
	if projects == nil {
		return apperrors.NewInternalServerError("project service is not configured")
	}
	projectUserIDs, err := projects.ListProjectUserIDs(ctx, projectID)
	if err != nil {
		return apperrors.NewInternalServerError("failed to list project users").WithDetails(err.Error())
	}
	for _, id := range projectUserIDs {
		if id == userID {
			return nil
		}
	}
	return apperrors.NewForbiddenError("current user is not a member of the project; resource is not accessible")
}
