package executor

import (
	"context"
	"fmt"

	"github.com/gobravedev/gobrave/internal/manager"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
)

type KubernetesExecutor struct {
	fallback     Executor
	workflowRepo interfaces.WorkflowRepository
	containerMgr *manager.ContainerManager
}

func NewKubernetesExecutor(
	fallback Executor,
	workflowRepo interfaces.WorkflowRepository,
	containerMgr *manager.ContainerManager,
) *KubernetesExecutor {
	return &KubernetesExecutor{
		fallback:     fallback,
		workflowRepo: workflowRepo,
		containerMgr: containerMgr,
	}
}

func (e *KubernetesExecutor) Execute(ctx context.Context, node *types.AnalysisNode) (*Result, error) {
	if ActionFromContext(ctx) == ActionStop {
		if e.containerMgr == nil {
			return nil, fmt.Errorf("kubernetes executor stop requires container manager")
		}
		if node == nil || node.ID == 0 {
			return nil, fmt.Errorf("kubernetes executor stop requires persisted analysis node id")
		}
		if err := e.containerMgr.StopByOwner(ctx, types.ContainerOwnerDagNode, int64(node.ID)); err != nil {
			return nil, fmt.Errorf("stop dag node kubernetes job failed: %w", err)
		}
		return &Result{Status: "stopping", ExitCode: 0}, nil
	}

	if e.containerMgr == nil || e.workflowRepo == nil {
		return e.fallback.Execute(ctx, node)
	}

	scriptID := node.ScriptID
	if scriptID == 0 {
		return nil, fmt.Errorf("kubernetes executor requires script_id")
	}

	scriptItem, err := e.workflowRepo.GetScriptByID(ctx, scriptID)
	if err != nil {
		return nil, fmt.Errorf("load script failed: %w", err)
	}
	if scriptItem.ContainerTemplateID == 0 {
		return nil, fmt.Errorf("script container_template_id is required: script_id=%d", scriptID)
	}

	if node.ID == 0 {
		return nil, fmt.Errorf("kubernetes executor requires persisted analysis node id")
	}

	instanceName := fmt.Sprintf("dag-node-%d-%d", node.AnalysisID, node.ID)
	inst, err := e.containerMgr.CreateByTemplate(
		ctx,
		scriptItem.ContainerTemplateID,
		types.ContainerOwnerDagNode,
		int64(node.ID),
		instanceName,
	)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes job by dag node failed: %w", err)
	}

	resolvedOutputs := map[string]any{
		"container_instance_id": inst.ID,
		"container_runtime_id":  inst.RuntimeID,
		"container_ip":          inst.IPAddress,
		"container_status":      string(inst.Status),
		"container_owner_type":  string(inst.OwnerType),
		"container_owner_id":    inst.OwnerID,
	}

	return &Result{
		Status:          "running",
		ResolvedOutputs: resolvedOutputs,
		ExitCode:        0,
		Deferred:        true,
	}, nil
}
