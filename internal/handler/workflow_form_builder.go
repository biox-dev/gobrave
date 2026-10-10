package handler

import (
	"context"
	"sort"

	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

func buildScriptFormData(ctx context.Context,
	workflowService interfaces.WorkflowService,
	dataService interfaces.DataService,
	scriptID int64,
	projectID string) ([]interface{}, map[string]interface{}, error) {

	formJSONWrap, _, err := workflowService.GetScriptFormJSONByID(ctx, scriptID)
	if err != nil {
		return nil, nil, err
	}

	analysisResult, err := resolveFormAnalysisResult(ctx, dataService, formJSONWrap, projectID)
	if err != nil {
		return nil, nil, err
	}

	return formJSONWrap, analysisResult, nil
}

// resolveFormAnalysisResult builds the `analysis_result` map for a form JSON.
// Every `input_type=assay` item contributes the project's assays: a dataset binds
// directly to the assay (go_project_dataset -> go_dataset_assay -> go_assay), and
// the assay carries its own `role`, which the item's `resolver.accept_formats` are
// matched against. The assays are then keyed by their own role, which the
// frontend reads as `dataMap[role]`. Every `input_type=file` item contributes its
// files under each of its `resolver.accept_formats` roles (matched against
// `go_dataset_file.role`). The frontend resolves an item's options as
// `dataMap[role]`, which is why these are keyed by role just like the file case
// always was.
func resolveFormAnalysisResult(
	ctx context.Context,
	dataService interfaces.DataService,
	formJSONWrap []interface{},
	projectID string,
) (map[string]interface{}, error) {
	assayRoleSet := make(map[string]struct{})
	fileRoleSet := make(map[string]struct{})

	for _, item := range formJSONWrap {
		formItem, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		inputType, _ := formItem["input_type"].(string)
		if inputType != "assay" && inputType != "file" {
			continue
		}

		target := fileRoleSet
		if inputType == "assay" {
			target = assayRoleSet
		}
		for _, role := range utils.AcceptFormats(formItem) {
			target[role] = struct{}{}
		}
	}

	analysisResult := make(map[string]interface{})

	if len(assayRoleSet) > 0 {
		roles := sortedKeys(assayRoleSet)

		// The item's resolver.accept_formats are the roles this input accepts; an
		// empty roles slice is not possible here (the set only gets filled from
		// accept_formats), and the repository adds no role condition when it is
		// empty anyway.
		assays, err := dataService.ListAssayByProjectID(ctx, projectID, roles)
		if err != nil {
			return nil, err
		}

		grouped := make(map[string][]map[string]interface{}, len(roles))
		for _, role := range roles {
			grouped[role] = make([]map[string]interface{}, 0)
		}
		for _, assay := range assays {
			compatItem, err := buildCompatAssayItem(assay)
			if err != nil {
				return nil, err
			}
			grouped[assay.Role] = append(grouped[assay.Role], compatItem)
		}
		for role, items := range grouped {
			analysisResult[role] = items
		}
	}

	if len(fileRoleSet) > 0 {
		roles := sortedKeys(fileRoleSet)

		files, err := dataService.ListFileByProjectID(ctx, projectID, roles)
		if err != nil {
			return nil, err
		}

		grouped := make(map[string][]map[string]interface{}, len(roles))
		for _, role := range roles {
			grouped[role] = make([]map[string]interface{}, 0)
		}
		for _, file := range files {
			compatItem, err := buildCompatFileItem(file)
			if err != nil {
				return nil, err
			}
			grouped[file.Role] = append(grouped[file.Role], compatItem)
		}
		for role, items := range grouped {
			analysisResult[role] = items
		}
	}

	return analysisResult, nil
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func buildWorkflowFormData(ctx context.Context,
	workflowService interfaces.WorkflowService,
	dataService interfaces.DataService,
	workflowID int64,
	projectID string) ([]interface{}, map[string]interface{}, error) {
	formJSONWrap, err := workflowService.GetFormJSONByWorkflowID(ctx, workflowID)
	if err != nil {
		return nil, nil, err
	}

	analysisResult, err := resolveFormAnalysisResult(ctx, dataService, formJSONWrap, projectID)
	if err != nil {
		return nil, nil, err
	}

	return formJSONWrap, analysisResult, nil
}
