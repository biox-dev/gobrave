package interfaces

import (
	"context"
	stderrs "errors"

	"github.com/biox-dev/gobrave/internal/types"
)

var ErrInvalidDagDefinitionJSON = stderrs.New("dag_definition is not valid JSON format")

type WorkflowService interface {
	GetFormJSONByWorkflowID(ctx context.Context, workflowID string) ([]any, error)
	GetScriptFormJSONByID(ctx context.Context, scriptID int64) ([]any, error)
	// 后续废除
	// GetFormJSONByScriptID(ctx context.Context, scriptID string) ([]any, error)
	GetWorkflowByID(ctx context.Context, id int64) (*types.Workflow, error)
	GetWorkflowVisByWorkflowID(ctx context.Context, workflowID string) (map[string]any, error)
	GetWorkflowByWorkflowID(ctx context.Context, workflowID string) (*types.Workflow, error)
	PageWorkflow(ctx context.Context, pagination *types.Pagination, query *types.WorkflowPageQuery) ([]*types.Workflow, int64, error)
	ExistsWorkflowInProjectByWorkflowID(ctx context.Context, projectID int64, workflowID string) (*types.Workflow, error)
	PageScript(ctx context.Context, pagination *types.Pagination, query *types.ScriptPageQuery) ([]*types.Script, int64, error)
	GetScriptByID(ctx context.Context, id int64) (*types.Script, error)
	GetScriptByScriptID(ctx context.Context, projectID int64, scriptID string) (*types.Script, error)
	ExistsScriptInProjectByScriptID(ctx context.Context, projectID int64, scriptID string) (*types.Script, error)
	// 后续废除
	// GetScriptMainFileByScriptID(ctx context.Context, scriptID string) (string, string, error)
	GetScriptFileByScriptID(ctx context.Context, scriptID int64) (string, string, error)
	GetScriptContainerSnapshotByScriptID(ctx context.Context, scriptID int64) (*types.ScriptContainerSnapshot, error)
	GenerateWorkflowJSONByWorkflowID(ctx context.Context, workflowID int64, storageBaseDir string) (*types.WorkflowJSONExportResponse, error)
	GenerateScriptJSONByScriptID(ctx context.Context, scriptID int64) (*types.ScriptJSONExportResponse, error)
	CreateWorkflow(ctx context.Context, workflow *types.Workflow) error
	UpdateWorkflow(ctx context.Context, workflow *types.Workflow) error
	DeleteWorkflow(ctx context.Context, id int64) error
	CreateScript(ctx context.Context, script *types.Script) error
	UpdateScript(ctx context.Context, script *types.Script) error
	DeleteScript(ctx context.Context, id int64) error
}

type WorkflowRepository interface {
	GetWorkflowByID(ctx context.Context, id int64) (*types.Workflow, error)
	GetWorkflowByWorkflowID(ctx context.Context, workflowID string) (*types.Workflow, error)
	PageWorkflow(ctx context.Context, pagination *types.Pagination, query *types.WorkflowPageQuery) ([]*types.Workflow, int64, error)
	ExistsWorkflowInProjectByWorkflowID(ctx context.Context, projectID int64, workflowID string) (*types.Workflow, error)
	PageScript(ctx context.Context, pagination *types.Pagination, query *types.ScriptPageQuery) ([]*types.Script, int64, error)
	GetScriptByID(ctx context.Context, id int64) (*types.Script, error)
	GetScriptByScriptID(ctx context.Context, projectID int64, scriptID string) (*types.Script, error)
	ExistsScriptInProjectByScriptID(ctx context.Context, projectID int64, scriptID string) (*types.Script, error)
	FindScriptsByScriptIDs(ctx context.Context, scriptIDs []string) ([]*types.Script, error)
	GetScriptContainerSnapshotByScriptID(ctx context.Context, scriptID int64) (*types.ScriptContainerSnapshot, error)
	CreateWorkflow(ctx context.Context, workflow *types.Workflow) error
	UpdateWorkflow(ctx context.Context, workflow *types.Workflow) error
	DeleteWorkflowByID(ctx context.Context, id int64) error
	CreateScript(ctx context.Context, script *types.Script) error
	UpdateScript(ctx context.Context, script *types.Script) error
	DeleteScriptByID(ctx context.Context, id int64) error
	ListWorkflowsByProjectID(ctx context.Context, projectID int64) ([]*types.Workflow, error)
}
