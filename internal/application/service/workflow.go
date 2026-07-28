package service

import (
	"context"
	"encoding/json"
	stderrs "errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/gobravedev/gobrave/internal/config"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"github.com/gobravedev/gobrave/internal/utils"
	"gorm.io/gorm"
)

type workflowService struct {
	workflowRepo  interfaces.WorkflowRepository
	containerRepo interfaces.ContainerRepository
	projectRepo   interfaces.ProjectRepository
	cfg           *config.Config
}

func NewWorkflowService(workflowRepo interfaces.WorkflowRepository, containerRepo interfaces.ContainerRepository, projectRepo interfaces.ProjectRepository, cfg *config.Config) interfaces.WorkflowService {
	return &workflowService{workflowRepo: workflowRepo, containerRepo: containerRepo, projectRepo: projectRepo, cfg: cfg}
}

func (s *workflowService) GetWorkflowByID(ctx context.Context, id int64) (*types.Workflow, error) {
	return s.workflowRepo.GetWorkflowByID(ctx, id)
}

func (s *workflowService) GetWorkflowByWorkflowID(ctx context.Context, workflowID string) (*types.Workflow, error) {
	return s.workflowRepo.GetWorkflowByWorkflowID(ctx, workflowID)
}

func (s *workflowService) PageWorkflow(ctx context.Context, pagination *types.Pagination, query *types.WorkflowPageQuery) ([]*types.Workflow, int64, error) {
	return s.workflowRepo.PageWorkflow(ctx, pagination, query)
}

func (s *workflowService) ExistsWorkflowInProjectByWorkflowID(ctx context.Context, projectID int64, workflowID string) (*types.Workflow, error) {
	return s.workflowRepo.ExistsWorkflowInProjectByWorkflowID(ctx, projectID, workflowID)
}

func (s *workflowService) PageScript(ctx context.Context, pagination *types.Pagination, query *types.ScriptPageQuery) ([]*types.Script, int64, error) {
	return s.workflowRepo.PageScript(ctx, pagination, query)
}

func (s *workflowService) GetScriptByID(ctx context.Context, id int64) (*types.Script, error) {
	return s.workflowRepo.GetScriptByID(ctx, id)
}

func (s *workflowService) ExistsScriptInProjectByScriptID(ctx context.Context, projectID int64, scriptID string) (*types.Script, error) {
	return s.workflowRepo.ExistsScriptInProjectByScriptID(ctx, projectID, scriptID)
}

func (s *workflowService) GetWorkflowVisByWorkflowID(ctx context.Context, workflowID string) (map[string]any, error) {
	findWorkflow, err := s.workflowRepo.GetWorkflowByWorkflowID(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	dagDefinition := make(map[string]any)
	if findWorkflow.DagDefinition == "" {
		return dagDefinition, nil
	}
	if err := json.Unmarshal([]byte(findWorkflow.DagDefinition), &dagDefinition); err != nil {
		return dagDefinition, nil
	}

	nodesRaw, ok := dagDefinition["nodes"].([]any)
	if !ok || len(nodesRaw) == 0 {
		return dagDefinition, nil
	}

	scriptIDs := make([]string, 0, len(nodesRaw))
	seen := make(map[string]struct{})
	for _, nodeAny := range nodesRaw {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			continue
		}
		scriptID, _ := node["script_id"].(string)
		if scriptID == "" {
			continue
		}
		if _, exists := seen[scriptID]; exists {
			continue
		}
		seen[scriptID] = struct{}{}
		scriptIDs = append(scriptIDs, scriptID)
	}

	scriptNodeMap := make(map[string]map[string]any)
	if len(scriptIDs) > 0 {
		scripts, err := s.workflowRepo.FindScriptsByScriptIDs(ctx, scriptIDs)
		if err != nil {
			return nil, err
		}
		for _, script := range scripts {
			scriptNodeMap[script.ScriptID] = buildScriptVisItem(script)
		}
	}

	nodesRes := make([]any, 0, len(nodesRaw))
	for _, nodeAny := range nodesRaw {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			continue
		}

		scriptID, _ := node["script_id"].(string)
		scriptNode, exists := scriptNodeMap[scriptID]
		if exists {
			merged := cloneAnyMap(scriptNode)
			for k, v := range node {
				merged[k] = v
			}
			nodesRes = append(nodesRes, merged)
			continue
		}

		merged := cloneAnyMap(node)
		merged["name"] = "unknown"
		nodesRes = append(nodesRes, merged)
	}

	dagDefinition["nodes"] = nodesRes
	return dagDefinition, nil
}

func (s *workflowService) GetScriptByScriptID(ctx context.Context, projectID int64, scriptID string) (*types.Script, error) {
	return s.workflowRepo.GetScriptByScriptID(ctx, projectID, scriptID)
}

func (s *workflowService) GetScriptFileByScriptID(ctx context.Context, scriptID int64) (string, string, error) {
	script, err := s.workflowRepo.GetScriptByID(ctx, scriptID)
	if err != nil {
		return "", "", err
	}
	if script == nil {
		return "", "", nil
	}
	project, err := s.projectRepo.GetProjectByID(ctx, script.ProjectID)
	if err != nil {
		return "", "", err
	}
	if project == nil {
		return "", "", nil
	}
	return utils.GetScriptFile(s.cfg.Storage.BaseDir, project.ProjectID, script.ScriptType, script.ScriptID)
}

// func (s *workflowService) GetScriptMainFileByScriptID(ctx context.Context, scriptID string) (string, string, error) {
// 	script, err := s.workflowRepo.GetScriptByScriptID(ctx, scriptID)
// 	if err != nil {
// 		return "", "", err
// 	}
// 	if script == nil {
// 		return "", "", nil
// 	}

// 	// return utils.GetScriptFile(script.ScriptType, scriptID)
// 	project, err := s.projectRepo.GetProjectByID(ctx, script.ProjectID)
// 	if err != nil {
// 		return "", "", err
// 	}
// 	if project == nil {
// 		return "", "", nil
// 	}
// 	return utils.GetScriptFile(s.cfg.Storage.BaseDir, project.ProjectID, script.ScriptType, script.ScriptID)
// }

func (s *workflowService) GetScriptContainerSnapshotByScriptID(ctx context.Context, scriptID int64) (*types.ScriptContainerSnapshot, error) {
	return s.workflowRepo.GetScriptContainerSnapshotByScriptID(ctx, scriptID)
}

func (s *workflowService) GenerateWorkflowJSONByWorkflowID(ctx context.Context, workflowID int64, storageBaseDir string) (*types.WorkflowJSONExportResponse, error) {
	if strings.TrimSpace(storageBaseDir) == "" {
		return nil, os.ErrInvalid
	}

	workflow, err := s.workflowRepo.GetWorkflowByID(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	workflowMap, err := structToMap(workflow)
	if err != nil {
		return nil, err
	}

	var dagDefinition map[string]any
	if workflow.DagDefinition != "" {
		if !json.Valid([]byte(workflow.DagDefinition)) {
			return nil, interfaces.ErrInvalidDagDefinitionJSON
		}
		if err := json.Unmarshal([]byte(workflow.DagDefinition), &dagDefinition); err != nil {
			return nil, err
		}
		workflowMap["dag_definition"] = dagDefinition
	}

	nodeItems, _ := dagDefinition["nodes"].([]any)
	scriptIDs := make([]string, 0, len(nodeItems))
	seenScriptIDs := make(map[string]struct{})
	for _, nodeAny := range nodeItems {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			continue
		}
		scriptID, _ := node["script_id"].(string)
		if scriptID == "" {
			continue
		}
		if _, exists := seenScriptIDs[scriptID]; exists {
			continue
		}
		seenScriptIDs[scriptID] = struct{}{}
		scriptIDs = append(scriptIDs, scriptID)
	}

	scripts := make([]map[string]any, 0, len(scriptIDs))
	containerTemplates := make([]map[string]any, 0)
	seenTemplateIDs := make(map[int64]struct{})
	for _, scriptID := range scriptIDs {
		script, scriptErr := s.workflowRepo.GetScriptByScriptID(ctx, workflow.ProjectID, scriptID)
		if scriptErr != nil {
			if stderrs.Is(scriptErr, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, scriptErr
		}

		scriptMap, scriptMapErr := structToMap(script)
		if scriptMapErr != nil {
			return nil, scriptMapErr
		}

		if script.ContainerTemplateID != 0 {
			template, templateErr := s.containerRepo.GetContainerTemplateByID(ctx, script.ContainerTemplateID)
			if templateErr == nil && template != nil {
				templateMap, templateMapErr := structToMap(template)
				if templateMapErr != nil {
					return nil, templateMapErr
				}
				scriptMap["container_template"] = templateMap
				if _, exists := seenTemplateIDs[template.ID]; !exists {
					seenTemplateIDs[template.ID] = struct{}{}
					containerTemplates = append(containerTemplates, templateMap)
				}
			}
		}

		scripts = append(scripts, scriptMap)
	}

	exportPayload := &types.WorkflowJSONExportResponse{
		WorkflowID:         workflow.WorkflowID,
		Workflow:           workflowMap,
		Scripts:            scripts,
		ContainerTemplates: containerTemplates,
	}

	exportDir := filepath.Join(storageBaseDir, "pipeline", "tools", workflow.WorkflowID)
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		return nil, err
	}

	// exportPath := filepath.Join(exportDir, "workflow.json")
	// exportBytes, err := json.MarshalIndent(exportPayload, "", "  ")
	// if err != nil {
	// 	return nil, err
	// }
	// if err := os.WriteFile(exportPath, exportBytes, 0o644); err != nil {
	// 	return nil, err
	// }

	// exportPayload.Path = exportPath
	return exportPayload, nil
}

func (s *workflowService) GenerateScriptJSONByScriptID(ctx context.Context, scriptID int64) (*types.ScriptJSONExportResponse, error) {
	script, err := s.workflowRepo.GetScriptByID(ctx, scriptID)
	if err != nil {
		return nil, err
	}

	scriptMap, err := structToMap(script)
	if err != nil {
		return nil, err
	}

	containerTemplates := make([]map[string]any, 0)
	if script.ContainerTemplateID != 0 {
		template, templateErr := s.containerRepo.GetContainerTemplateByID(ctx, script.ContainerTemplateID)
		if templateErr == nil && template != nil {
			templateMap, templateMapErr := structToMap(template)
			if templateMapErr != nil {
				return nil, templateMapErr
			}
			scriptMap["container_template"] = templateMap
			containerTemplates = append(containerTemplates, templateMap)
		}
	}

	return &types.ScriptJSONExportResponse{
		ScriptID:           script.ScriptID,
		Script:             scriptMap,
		ContainerTemplates: containerTemplates,
	}, nil
}

func (s *workflowService) CreateWorkflow(ctx context.Context, workflow *types.Workflow) error {
	return s.workflowRepo.CreateWorkflow(ctx, workflow)
}

func (s *workflowService) UpdateWorkflow(ctx context.Context, workflow *types.Workflow) error {
	return s.workflowRepo.UpdateWorkflow(ctx, workflow)
}

func (s *workflowService) CreateScript(ctx context.Context, script *types.Script) error {
	return s.workflowRepo.CreateScript(ctx, script)
}

func (s *workflowService) UpdateScript(ctx context.Context, script *types.Script) error {
	return s.workflowRepo.UpdateScript(ctx, script)
}
func (s *workflowService) GetScriptFormJSONByID(ctx context.Context, scriptID int64) ([]any, error) {
	script, err := s.workflowRepo.GetScriptByID(ctx, scriptID)
	if err != nil {
		return nil, err
	}

	formJSONWrap := make([]interface{}, 0)

	if script.IOSchema != "" {
		ioSchema := make(map[string]interface{})
		if err := json.Unmarshal([]byte(script.IOSchema), &ioSchema); err != nil {
			return nil, err
		}
		if inputs, ok := ioSchema["inputs"].([]interface{}); ok {
			formJSONWrap = append(formJSONWrap, inputs...)
		}
		if params, ok := ioSchema["params"].([]interface{}); ok {
			formJSONWrap = append(formJSONWrap, params...)
		}

	}

	if script.Content != "" {
		content := make(map[string]interface{})
		if err := json.Unmarshal([]byte(script.Content), &content); err != nil {
			return nil, err
		}
		if contentFormJSON, ok := content["formJson"].([]interface{}); ok {
			formJSONWrap = append(formJSONWrap, contentFormJSON...)
		}
	}
	return formJSONWrap, err
}

// 后续废除
// func (s *workflowService) GetFormJSONByScriptID(ctx context.Context, scriptID int64) ([]any, error) {
// 	script, err := s.workflowRepo.GetScriptByID(ctx, scriptID)
// 	if err != nil {
// 		return nil, err
// 	}

// 	formJSONWrap := make([]interface{}, 0)

// 	if script.IOSchema != "" {
// 		ioSchema := make(map[string]interface{})
// 		if err := json.Unmarshal([]byte(script.IOSchema), &ioSchema); err != nil {
// 			return nil, err
// 		}
// 		if inputs, ok := ioSchema["inputs"].([]interface{}); ok {
// 			formJSONWrap = append(formJSONWrap, inputs...)
// 		}
// 		if params, ok := ioSchema["params"].([]interface{}); ok {
// 			formJSONWrap = append(formJSONWrap, params...)
// 		}

// 	}

// 	if script.Content != "" {
// 		content := make(map[string]interface{})
// 		if err := json.Unmarshal([]byte(script.Content), &content); err != nil {
// 			return nil, err
// 		}
// 		if contentFormJSON, ok := content["formJson"].([]interface{}); ok {
// 			formJSONWrap = append(formJSONWrap, contentFormJSON...)
// 		}
// 	}
// 	return formJSONWrap, err
// }

func (s *workflowService) GetFormJSONByWorkflowID(ctx context.Context, workflowID string) ([]any, error) {
	findWorkflow, err := s.workflowRepo.GetWorkflowByWorkflowID(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	formJSONWrap := make([]any, 0)
	if findWorkflow.DagDefinition == "" {
		return formJSONWrap, nil
	}

	var dagDefinition map[string]any
	if err := json.Unmarshal([]byte(findWorkflow.DagDefinition), &dagDefinition); err != nil {
		return formJSONWrap, nil
	}

	nodesRaw, ok := dagDefinition["nodes"].([]any)
	if !ok {
		return formJSONWrap, nil
	}

	nodesMap := make(map[string]map[string]any)
	inputScriptIDs := make(map[string]struct{})
	nodeIncomingHandles := make(map[string]map[string]struct{})
	nodeIDsByModuleID := make(map[string][]string)

	edgesRaw, _ := dagDefinition["edges"].([]any)
	for _, edgeAny := range edgesRaw {
		edge, ok := edgeAny.(map[string]any)
		if !ok {
			continue
		}
		targetNodeID, _ := edge["target"].(string)
		targetHandle, _ := edge["targetHandle"].(string)
		if targetNodeID == "" || targetHandle == "" {
			continue
		}
		if _, exists := nodeIncomingHandles[targetNodeID]; !exists {
			nodeIncomingHandles[targetNodeID] = make(map[string]struct{})
		}
		nodeIncomingHandles[targetNodeID][targetHandle] = struct{}{}
	}

	moduleIDs := make([]string, 0)
	for _, nodeAny := range nodesRaw {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			continue
		}
		moduleID, _ := node["script_id"].(string)
		if moduleID == "" {
			continue
		}
		nodeID, _ := node["node_id"].(string)

		nodesMap[moduleID] = node
		moduleIDs = append(moduleIDs, moduleID)
		if nodeID != "" {
			nodeIDsByModuleID[moduleID] = append(nodeIDsByModuleID[moduleID], nodeID)
		}
	}

	if len(moduleIDs) == 0 {
		return formJSONWrap, nil
	}

	scripts, err := s.workflowRepo.FindScriptsByScriptIDs(ctx, moduleIDs)
	if err != nil {
		return nil, err
	}

	for _, script := range scripts {
		scriptID := script.ScriptID
		ioSchema := make(map[string]any)
		if script.IOSchema != "" {
			_ = json.Unmarshal([]byte(script.IOSchema), &ioSchema)
		}

		inputNames := getInputNames(ioSchema)
		nodeIDs := nodeIDsByModuleID[scriptID]
		missingInputNames := make(map[string]struct{})
		for _, nodeID := range nodeIDs {
			incomingHandles := nodeIncomingHandles[nodeID]
			for inputName := range inputNames {
				if _, ok := incomingHandles[inputName]; !ok {
					missingInputNames[inputName] = struct{}{}
				}
			}
		}
		if len(missingInputNames) > 0 {
			inputScriptIDs[scriptID] = struct{}{}
		}

		if _, isInputScript := inputScriptIDs[scriptID]; isInputScript && script.IOSchema != "" {
			merged := make(map[string]any, len(ioSchema)+4)
			for k, v := range ioSchema {
				merged[k] = v
			}
			if node, exists := nodesMap[scriptID]; exists {
				for k, v := range node {
					merged[k] = v
				}
			}
			buildInputScriptFormJSON(merged, &formJSONWrap, missingInputNames)
		}

		if params, ok := ioSchema["params"].([]any); ok {
			formJSONWrap = append(formJSONWrap, params...)
		}

		if script.Content != "" {
			var content map[string]any
			if err := json.Unmarshal([]byte(script.Content), &content); err == nil {
				if contentFormJSON, ok := content["formJson"].([]any); ok {
					formJSONWrap = append(formJSONWrap, contentFormJSON...)
				}
			}
		}
	}

	return formJSONWrap, nil
}

func getInputNames(ioSchema map[string]any) map[string]struct{} {
	result := make(map[string]struct{})
	inputs, ok := ioSchema["inputs"].([]any)
	if !ok {
		return result
	}

	for _, itemAny := range inputs {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		name, _ := item["name"].(string)
		if name == "" {
			continue
		}
		result[name] = struct{}{}
	}

	return result
}

func structToMap(value any) (map[string]any, error) {
	content, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	result := make(map[string]any)
	if err := json.Unmarshal(content, &result); err != nil {
		return nil, err
	}

	return result, nil
}

func filterInputs(items []any, inputNames map[string]struct{}) []any {
	if inputNames == nil {
		return items
	}

	filtered := make([]any, 0, len(items))
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		name, _ := item["name"].(string)
		if name == "" {
			continue
		}
		if _, exists := inputNames[name]; exists {
			filtered = append(filtered, itemAny)
		}
	}

	return filtered
}

func buildInputScriptFormJSON(ioSchema map[string]any, formJSONWrap *[]any, inputNames map[string]struct{}) {
	if scatterAny, ok := ioSchema["scatter"]; ok {
		scatter, ok := scatterAny.(map[string]any)
		if !ok {
			return
		}
		if mode, _ := scatter["mode"].(string); mode == "each" {
			if workflow, ok := ioSchema["workflow"].([]any); ok {
				*formJSONWrap = append(*formJSONWrap, workflow...)
			}
			return
		}
		if inputs, ok := ioSchema["inputs"].([]any); ok {
			*formJSONWrap = append(*formJSONWrap, filterInputs(inputs, inputNames)...)
		}
		return
	}

	if inputs, ok := ioSchema["inputs"].([]any); ok {
		*formJSONWrap = append(*formJSONWrap, filterInputs(inputs, inputNames)...)
	}
}

func buildScriptVisItem(script *types.Script) map[string]any {
	node := map[string]any{
		"name":      script.ComponentName,
		"id":        script.ScriptID,
		"script_id": script.ScriptID,
		"node_id":   script.ScriptID + "_1",
		"inputs":    map[string]any{},
		"outputs":   map[string]any{},
	}

	if script.IOSchema == "" {
		return node
	}

	ioSchema := make(map[string]any)
	if err := json.Unmarshal([]byte(script.IOSchema), &ioSchema); err != nil {
		return node
	}

	node["inputs"] = formatIOSchemaItems(ioSchema["inputs"])
	node["outputs"] = formatIOSchemaItems(ioSchema["outputs"])

	if scatter, ok := ioSchema["scatter"]; ok {
		node["scatter"] = scatter
	}
	if gather, ok := ioSchema["gather"]; ok {
		node["gather"] = gather
	}
	if ui, ok := ioSchema["ui"].(map[string]any); ok {
		if color, exists := ui["color"]; exists {
			node["color"] = color
		}
		if icon, exists := ui["icon"]; exists {
			node["icon"] = icon
		}
	}

	return node
}

func formatIOSchemaItems(raw any) map[string]any {
	items, ok := raw.([]any)
	if !ok {
		return map[string]any{}
	}

	result := make(map[string]any)
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		name, _ := item["name"].(string)
		if name == "" {
			continue
		}

		formatted := make(map[string]any)
		for k, v := range item {
			if k == "name" {
				continue
			}
			formatted[k] = v
		}
		result[name] = formatted
	}

	return result
}

func cloneAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
