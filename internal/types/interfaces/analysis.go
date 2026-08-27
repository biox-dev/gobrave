package interfaces

import (
	"context"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
)

type AnalysisService interface {
	UpdateAnalysisByID(ctx context.Context, analysisID int64, values map[string]any) error
	UpdateAnalysisNodeByID(ctx context.Context, analysisNodeID int64, values map[string]any) error
	GetAnalysisByID(ctx context.Context, analysisID int64) (*types.Analysis, error)
	GetAnalysisByAnalysisID(ctx context.Context, analysisID string) (*types.Analysis, error)
	ListAnalysisByProjectID(ctx context.Context, projectID int64, query *types.AnalysisQuey) ([]*types.Analysis, error)
	PageAnalysisByProjectID(ctx context.Context, pagination *types.Pagination, projectID int64, query *types.AnalysisQuey) ([]*types.Analysis, int64, error)
	GetAnalysisNodeByID(ctx context.Context, id int64) (*types.AnalysisNode, error)
	GetAnalysisNodeByAnalysisNodeID(ctx context.Context, analysisNodeID string) (*types.AnalysisNode, error)
	ListAnalysisNodesByAnalysisID(ctx context.Context, analysisID int64) ([]*types.AnalysisNode, error)
	ListAnalysisNodesByProjectIDAndScriptID(ctx context.Context, projectID, scriptID int64) ([]*types.AnalysisNode, error)
	SaveAnalysisController(ctx context.Context, input *types.AnalysisControllerSaveInput) (*types.Analysis, error)
	DeleteAnalysisNode(ctx context.Context, id int64) error
	DeleteAnalysis(ctx context.Context, id int64) error
	ListAnalysisByWorkflowID(ctx context.Context, workflowID string) ([]*types.Analysis, error)
}

type AnalysisRepository interface {
	GetAnalysisByID(ctx context.Context, analysisID int64) (*types.Analysis, error)
	GetAnalysisByAnalysisID(ctx context.Context, analysisID string) (*types.Analysis, error)
	ListAnalysisByJobStatus(ctx context.Context, jobStatus string) ([]*types.Analysis, error)
	ListAnalysisByProjectID(ctx context.Context, projectID int64, query *types.AnalysisQuey) ([]*types.Analysis, error)
	PageAnalysisByProjectID(ctx context.Context, pagination *types.Pagination, projectID int64, query *types.AnalysisQuey) ([]*types.Analysis, int64, error)
	GetAnalysisNodeByID(ctx context.Context, id int64) (*types.AnalysisNode, error)
	GetAnalysisNodeByAnalysisNodeID(ctx context.Context, analysisNodeID string) (*types.AnalysisNode, error)
	GetAnalysisNodeByNodeID(ctx context.Context, analysisID int64, nodeID string) (*types.AnalysisNode, error)
	WithTransaction(ctx context.Context, fn func(AnalysisRepository) error) error
	CreateAnalysis(ctx context.Context, item *types.Analysis) error
	TryMarkAnalysisRunning(ctx context.Context, analysisID int64, now time.Time, staleBefore time.Time) (bool, error)
	UpdateAnalysisByAnalysisID(ctx context.Context, analysisID string, values map[string]any) error
	UpdateAnalysisByID(ctx context.Context, analysisID int64, values map[string]any) error

	ListAnalysisNodesByAnalysisID(ctx context.Context, analysisID int64) ([]*types.AnalysisNode, error)
	PageAnalysisNodesByProjectID(ctx context.Context, pagination *types.Pagination, projectID, scriptID int64) ([]*types.AnalysisNode, int64, error)
	ListAnalysisNodesByProjectIDAndScriptID(ctx context.Context, projectID, scriptID int64) ([]*types.AnalysisNode, error)
	ListAnalysisEdgesByAnalysisID(ctx context.Context, analysisID int64) ([]*types.AnalysisEdge, error)
	UpdateAnalysisNodeByID(ctx context.Context, id int64, values map[string]any) error
	UpdateAnalysisNodeByAnalysisNodeID(ctx context.Context, analysisNodeID string, values map[string]any) error
	ClaimNextReadyNode(ctx context.Context, analysisID int64, fromStatus string, toStatus string) (*types.AnalysisNode, error)
	DeleteAnalysisNodesByAnalysisID(ctx context.Context, analysisID int64) error
	DeleteAnalysisNodeByID(ctx context.Context, id int64) error
	CreateAnalysisNodes(ctx context.Context, items []*types.AnalysisNode) error
	DeleteAnalysisEdgesByAnalysisID(ctx context.Context, analysisID int64) error
	CreateAnalysisEdges(ctx context.Context, items []*types.AnalysisEdge) error
	DeleteAnalysisByID(ctx context.Context, id int64) error
	ListAnalysisByWorkflowID(ctx context.Context, workflowID string) ([]*types.Analysis, error)
}
