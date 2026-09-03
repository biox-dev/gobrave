package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

type ContainerService interface {
	CreateContainerImage(ctx context.Context, item *types.ContainerImage) error
	GetContainerImageByID(ctx context.Context, id int64) (*types.ContainerImage, error)
	UpdateContainerImage(ctx context.Context, item *types.ContainerImage) error
	DeleteContainerImage(ctx context.Context, id int64) error
	ListContainerImage(ctx context.Context) ([]*types.ContainerImage, error)
	PageContainerImage(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error)
	// TransitionContainerAndEnqueueOutbox(ctx context.Context, instance *types.ContainerInstance, to types.ContainerStatus, eventType string) error
	// GetMaxConcurrency() int
	RecreateAppSessionContainer(ctx context.Context, userID string, appSessionID int64) error

	CreateContainerTemplate(ctx context.Context, item *types.ContainerTemplate) error
	GetContainerTemplateByID(ctx context.Context, id int64) (*types.ContainerTemplate, error)
	UpdateContainerTemplate(ctx context.Context, item *types.ContainerTemplate) error
	DeleteContainerTemplate(ctx context.Context, id int64) error
	ListContainerTemplate(ctx context.Context) ([]*types.ContainerTemplate, error)
	PageContainerTemplate(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error)
	ImportContainerTemplate(ctx context.Context, item *types.ContainerTemplateExport) (*types.ContainerTemplateExport, error)

	CreateAppSessionByTemplate(ctx context.Context, userID string, projectID int64, containerTemplateID int64, name string) (*types.AppSession, error)
	CreateAppSessionByTemplateForAnalysisNode(ctx context.Context, userID string, projectID int64, containerTemplateID int64, name string, analysisNodeID int64, workspacePath string) (*types.AppSession, error)
	StartAppSession(ctx context.Context, userID string, appSessionID int64) error
	StopAppSession(ctx context.Context, userID string, appSessionID int64) error
	DeleteAppSession(ctx context.Context, userID string, appSessionID int64) error
	GetAppSessionByID(ctx context.Context, userID string, appSessionID int64) (*types.AppSession, error)
	ListAppSessionByUserID(ctx context.Context, userID string) ([]*types.AppSession, error)
	PageAppSessionByUserID(ctx context.Context, userID string, pagination *types.Pagination, query *types.AppSessionPageQuery) (*types.PageResult, error)
	ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx context.Context, ownerType types.ContainerOwnerType, ownerIDs []int64) ([]*types.ContainerInstance, error)
	DeleteContainerInstancesByOwnerTypeAndOwnerIDs(ctx context.Context, ownerType types.ContainerOwnerType, ownerIDs []int64) error

	PageContainerInstance(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error)
	// PageContainerEvent(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error)
	PageOutboxEvent(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error)
}

type ContainerRepository interface {
	WithTransaction(ctx context.Context, fn func(ContainerRepository) error) error

	CreateContainerImage(ctx context.Context, item *types.ContainerImage) error
	GetContainerImageByID(ctx context.Context, id int64) (*types.ContainerImage, error)
	UpdateContainerImage(ctx context.Context, item *types.ContainerImage) error
	DeleteContainerImage(ctx context.Context, id int64) error
	ListContainerImage(ctx context.Context) ([]*types.ContainerImage, error)
	PageContainerImage(ctx context.Context, pagination *types.Pagination) ([]*types.ContainerImage, int64, error)

	CreateContainerTemplate(ctx context.Context, item *types.ContainerTemplate) error
	GetContainerTemplateByID(ctx context.Context, id int64) (*types.ContainerTemplate, error)
	UpdateContainerTemplate(ctx context.Context, item *types.ContainerTemplate) error
	DeleteContainerTemplate(ctx context.Context, id int64) error
	ListContainerTemplate(ctx context.Context) ([]*types.ContainerTemplate, error)
	PageContainerTemplate(ctx context.Context, pagination *types.Pagination) ([]*types.ContainerTemplate, int64, error)

	CreateAppSession(ctx context.Context, item *types.AppSession) error
	GetAppSessionByID(ctx context.Context, id int64) (*types.AppSession, error)
	GetProjectByProjectID(ctx context.Context, projectID string) (*types.Project, error)
	UpdateAppSession(ctx context.Context, item *types.AppSession) error
	DeleteAppSession(ctx context.Context, id int64) error
	ListAppSession(ctx context.Context) ([]*types.AppSession, error)
	PageAppSessionByUserID(ctx context.Context, userID string, pagination *types.Pagination, query *types.AppSessionPageQuery) ([]*types.AppSession, int64, error)

	CreateContainerInstance(ctx context.Context, item *types.ContainerInstance) error
	GetContainerInstanceByID(ctx context.Context, id int64) (*types.ContainerInstance, error)
	GetContainerInstanceByRuntimeID(ctx context.Context, runtimeID string) (*types.ContainerInstance, error)
	GetContainerInstanceByOwner(ctx context.Context, ownerType types.ContainerOwnerType, ownerID int64) (*types.ContainerInstance, error)
	UpdateContainerInstance(ctx context.Context, item *types.ContainerInstance) error
	DeleteContainerInstance(ctx context.Context, id int64) error
	ListContainerInstance(ctx context.Context) ([]*types.ContainerInstance, error)
	CountContainerInstanceByStatuses(ctx context.Context, statuses []types.ContainerStatus) (int64, error)
	ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx context.Context, ownerType types.ContainerOwnerType, ownerIDs []int64) ([]*types.ContainerInstance, error)
	PageContainerInstance(ctx context.Context, pagination *types.Pagination) ([]*types.ContainerInstance, int64, error)

	// CreateContainerEvent(ctx context.Context, item *types.ContainerEvent) error
	// GetContainerEventByID(ctx context.Context, id int64) (*types.ContainerEvent, error)
	// UpdateContainerEvent(ctx context.Context, item *types.ContainerEvent) error
	// DeleteContainerEvent(ctx context.Context, id int64) error
	// ListContainerEvent(ctx context.Context) ([]*types.ContainerEvent, error)
	// PageContainerEvent(ctx context.Context, pagination *types.Pagination) ([]*types.ContainerEvent, int64, error)

	CreateOutboxEvent(ctx context.Context, item *types.OutboxEvent) error
	ListPendingOutboxEvent(ctx context.Context, limit int) ([]*types.OutboxEvent, error)
	ListPendingOutboxEventsByType(ctx context.Context, eventType string, limit int) ([]*types.OutboxEvent, error)
	CountPendingOutboxEvents(ctx context.Context, eventTypes ...string) (int64, error)
	CountPendingOutboxEventsByType(ctx context.Context, eventType string) (int64, error)
	MarkOutboxEventProcessing(ctx context.Context, id int64) error
	MarkOutboxEventPending(ctx context.Context, id int64) error
	MarkOutboxEventSent(ctx context.Context, id int64) error
	MarkOutboxEventFailed(ctx context.Context, id int64) error
	PageOutboxEvent(ctx context.Context, pagination *types.Pagination) ([]*types.OutboxEvent, int64, error)
}
