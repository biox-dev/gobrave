package service

// 本文件集中实现「应用会话」（types.AppSession）相关的服务层逻辑：
// 创建（Web 会话 / 分析节点会话 / 终端会话）、启停、删除、查询与重建容器。
//
// 应用会话是终端会话与 Web 应用会话的统一实体：
//   - SessionKind=web：容器跑模板命令，前端通过网关访问应用；
//   - SessionKind=terminal：容器常驻，交互式 shell 由终端会话注册表
//     （internal/manager.TerminalSessionRegistry）在拿到会话主键后打开。
//
// 会话层（manager）只负责「进程内 PTY + 会话记录持久化」，
// 面向用户的归属校验、分页查询等仍留在服务层，避免下层反向依赖上层的读模型。

import (
	"context"
	stderrs "errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/types"
	"gorm.io/gorm"
)

func (s *containerService) CreateAppSessionByTemplate(ctx context.Context, userID int64, projectID int64, containerTemplateID int64, name string) (*types.AppSession, error) {
	return s.createAppSessionByTemplate(ctx, userID, projectID, containerTemplateID, name, 0, "")
}

func (s *containerService) CreateAppSessionByTemplateForAnalysisNode(ctx context.Context, userID int64, projectID int64, containerTemplateID int64, name string, analysisNodeID int64, workspacePath string) (*types.AppSession, error) {
	return s.createAppSessionByTemplate(ctx, userID, projectID, containerTemplateID, name, analysisNodeID, workspacePath)
}

// CreateTerminalAppSession 按容器模板创建「终端会话」（AppSession + 常驻容器）。
//
// 与 CreateAppSessionByTemplate 的区别：
//   - AgentSessionID 记录发起方（0 = UI 手动创建；>0 = Agent 会话）：
//     同一 (templateID, agentSessionID) 只会有一条会话；
//   - Persistent 标记会话是否持久（UI / WS 接入后不会再退回一次性）；
//   - 容器以 terminalKeepAliveCommand 常驻，真正的交互式 shell 由运行时 exec 打开。
//
// 容器创建由终端会话注册表在拿到会话主键后同步执行（CreateByTemplateWithOptions），
// 因此这里只负责落库会话记录。
func (s *containerService) CreateTerminalAppSession(ctx context.Context, in types.TerminalAppSessionInput) (*types.AppSession, error) {
	if in.UserID == 0 {
		return nil, fmt.Errorf("user id is required")
	}
	if in.ProjectID == 0 {
		return nil, fmt.Errorf("project id is required")
	}
	if in.ContainerTemplateID == 0 {
		return nil, fmt.Errorf("container template id is required")
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		tpl, err := s.containerRepo.GetContainerTemplateByID(ctx, in.ContainerTemplateID)
		if err == nil && tpl != nil {
			name = strings.TrimSpace(tpl.DisplayName)
			if name == "" {
				name = strings.TrimSpace(tpl.Name)
			}
		}
		if name == "" {
			name = strconv.FormatInt(in.ContainerTemplateID, 10)
		}
		if in.AgentSessionID > 0 {
			name = fmt.Sprintf("%s (Agent %d)", name, in.AgentSessionID)
		}
	}

	now := time.Now()
	session := &types.AppSession{
		UserID:              in.UserID,
		ProjectID:           in.ProjectID,
		ContainerTemplateID: in.ContainerTemplateID,
		AgentSessionID:      in.AgentSessionID,
		Name:                name,
		Status:              "CREATING",
		WorkspacePath:       strings.TrimSpace(in.WorkspacePath),
		Persistent:          in.Persistent,
		// 终端会话：容器常驻，交互式 shell 由运行时 exec 打开。
		SessionKind:  types.AppSessionKindTerminal,
		LastAccessAt: &now,
	}
	if err := s.containerRepo.CreateAppSession(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

func (s *containerService) createAppSessionByTemplate(ctx context.Context, userID int64, projectID int64, containerTemplateID int64, name string, analysisNodeID int64, workspacePath string) (*types.AppSession, error) {
	if userID == 0 {
		return nil, fmt.Errorf("user id is required")
	}
	if projectID == 0 {
		return nil, fmt.Errorf("project id is required")
	}
	if containerTemplateID == 0 {
		return nil, fmt.Errorf("container template id is required")
	}

	tpl, err := s.containerRepo.GetContainerTemplateByID(ctx, containerTemplateID)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("app-session-%d-%d", projectID, containerTemplateID)
		if strings.TrimSpace(tpl.Name) != "" {
			name = fmt.Sprintf("%s-%d", strings.TrimSpace(tpl.Name), projectID)
		}
	}

	session := &types.AppSession{
		UserID:              userID,
		ProjectID:           projectID,
		AnalysisNodeID:      analysisNodeID,
		ContainerTemplateID: containerTemplateID,
		Name:                name,
		// AppType:             tpl.AppType,
		Status:        "CREATE_PENDING", //create_pending PENDING_CREATION
		WorkspacePath: strings.TrimSpace(workspacePath),
		// Web 应用会话：容器跑模板命令，前端通过网关路由访问应用。
		SessionKind: types.AppSessionKindWeb,
	}
	if err := s.containerRepo.CreateAppSession(ctx, session); err != nil {
		return nil, err
	}

	_, err = s.containerMgr.CreateByTemplate(ctx, containerTemplateID, types.ContainerOwnerAppSession, session.ID, name)
	if err != nil {
		session.Status = "FAILED"
		_ = s.containerRepo.UpdateAppSession(ctx, session)
		return nil, err
	}

	return session, nil
}

func (s *containerService) StartAppSession(ctx context.Context, userID int64, appSessionID int64) error {
	session, err := s.ensureOwnedAppSession(ctx, userID, appSessionID)
	if err != nil {
		return err
	}
	queueStatus, err := s.containerMgr.QueueStatus(ctx)
	if err != nil {
		return err
	}
	if queueStatus != nil && queueStatus.MaxPending > 0 && queueStatus.PendingCount >= int64(queueStatus.MaxPending) {
		session.Status = "FAILED"
		_ = s.containerRepo.UpdateAppSession(ctx, session)
		return fmt.Errorf("container start queue is full (%d/%d pending), please try again later",
			queueStatus.PendingCount, queueStatus.MaxPending)
	}

	inst, err := s.containerRepo.GetContainerInstanceByOwner(ctx, types.ContainerOwnerAppSession, session.ID)
	if err != nil {
		return err
	}

	if err := s.containerMgr.Start(ctx, inst.ID); err != nil {
		session.Status = "FAILED"
		_ = s.containerRepo.UpdateAppSession(ctx, session)
		return err
	}

	session.Status = "START_PENDING"
	session.StoppedAt = nil
	return s.containerRepo.UpdateAppSession(ctx, session)
}

func (s *containerService) StopAppSession(ctx context.Context, userID int64, appSessionID int64) error {
	session, err := s.ensureOwnedAppSession(ctx, userID, appSessionID)
	if err != nil {
		return err
	}
	inst, err := s.containerRepo.GetContainerInstanceByOwner(ctx, types.ContainerOwnerAppSession, session.ID)
	if err != nil {
		return err
	}

	if err := s.containerMgr.Stop(ctx, inst.ID); err != nil {
		if err == types.ErrContainerAlreadyStopped {
			session.Status = "STOPPED"
			return s.containerRepo.UpdateAppSession(ctx, session)
		}
		session.Status = "FAILED"
		_ = s.containerRepo.UpdateAppSession(ctx, session)
		return err
	}

	// now := time.Now()
	// session.Status = "STOPPED"
	// session.StoppedAt = &now
	// s.containerRepo.UpdateAppSession(ctx, session)
	return nil
}

func (s *containerService) DeleteAppSession(ctx context.Context, userID int64, appSessionID int64) error {
	session, err := s.ensureOwnedAppSession(ctx, userID, appSessionID)
	if err != nil {
		return err
	}

	inst, err := s.containerRepo.GetContainerInstanceByOwner(ctx, types.ContainerOwnerAppSession, session.ID)
	if err != nil && !stderrs.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil {
		if err := s.containerMgr.Delete(ctx, inst.ID); err != nil {
			return err
		}
	}

	return s.containerRepo.DeleteAppSession(ctx, session.ID)
}

func (s *containerService) GetAppSessionByID(ctx context.Context, userID int64, appSessionID int64) (*types.AppSession, error) {
	return s.ensureOwnedAppSession(ctx, userID, appSessionID)
}

func (s *containerService) ListAppSessionByUserID(ctx context.Context, userID int64) ([]*types.AppSession, error) {
	items, err := s.containerRepo.ListAppSession(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]*types.AppSession, 0)
	for _, item := range items {
		if item.UserID == userID {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func (s *containerService) PageAppSessionByUserID(ctx context.Context, userID int64, pagination *types.Pagination, query *types.AppSessionPageQuery) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.containerRepo.PageAppSessionByUserID(ctx, userID, pagination, query)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}

// ensureOwnedAppSession 按 ID 读取会话并校验归属：非本人会话统一按「不存在」处理。
func (s *containerService) ensureOwnedAppSession(ctx context.Context, userID int64, appSessionID int64) (*types.AppSession, error) {
	session, err := s.containerRepo.GetAppSessionByID(ctx, appSessionID)
	if err != nil {
		return nil, err
	}
	if session.UserID != userID {
		return nil, gorm.ErrRecordNotFound
	}
	return session, nil
}

func (s *containerService) RecreateAppSessionContainer(ctx context.Context, userID int64, appSessionID int64) error {
	session, err := s.ensureOwnedAppSession(ctx, userID, appSessionID)
	if err != nil {
		return err
	}

	// payload, err := json.Marshal(map[string]any{
	// 	"app_session_id": appSessionID,
	// 	"user_id":        strings.TrimSpace(userID),
	// })
	if err != nil {
		return err
	}

	// if err := s.containerRepo.CreateOutboxEvent(ctx, &types.OutboxEvent{
	// 	Type:    manager.OutboxEventTypeAppSessionRecreateRequest,
	// 	Payload: payload,
	// 	Status:  "pending",
	// }); err != nil {
	// 	return err
	// }

	session.Status = "RECREATE_PENDING"
	session.StoppedAt = nil
	if err := s.containerRepo.UpdateAppSession(ctx, session); err != nil {
		return err
	}
	inst, err := s.containerRepo.GetContainerInstanceByOwner(ctx, types.ContainerOwnerAppSession, session.ID)
	if err != nil {
		return err
	}
	if err := s.containerMgr.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerReCreatePending, manager.OutboxEventTypeRecreateRequest); err != nil {
		return err
	}

	return nil
}
