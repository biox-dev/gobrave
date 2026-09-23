package handler

import (
	"context"
	"sort"

	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

func buildScriptFormData(ctx context.Context,
	workflowService interfaces.WorkflowService,
	dataService interfaces.DataService,
	scriptID int64,
	projectID string) ([]interface{}, map[string]interface{}, error) {

	formJSONWrap, err := workflowService.GetScriptFormJSONByID(ctx, scriptID)
	if err != nil {
		return nil, nil, err
	}

	needAssayList := false
	roleSet := make(map[string]struct{})

	for _, item := range formJSONWrap {
		formItem, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		inputType, _ := formItem["input_type"].(string)
		switch inputType {
		case "assay":
			needAssayList = true
		case "file":
			resolver, ok := formItem["resolver"].(map[string]interface{})
			if !ok {
				continue
			}
			acceptFormats := extractStringList(resolver["accept_formats"])
			for _, role := range acceptFormats {
				if role != "" {
					roleSet[role] = struct{}{}
				}
			}
		}
	}

	analysisResult := map[string]interface{}{
		"assay": make([]map[string]interface{}, 0),
	}

	if needAssayList {
		assayList, err := dataService.ListAssayByProjectID(ctx, projectID)
		if err != nil {
			return nil, nil, err
		}

		compatAssays := make([]map[string]interface{}, 0, len(assayList))
		for _, assay := range assayList {
			compatItem, err := buildCompatAssayItem(assay)
			if err != nil {
				return nil, nil, err
			}
			compatAssays = append(compatAssays, compatItem)
		}
		analysisResult["assay"] = compatAssays
	}

	if len(roleSet) > 0 {
		roles := make([]string, 0, len(roleSet))
		for role := range roleSet {
			roles = append(roles, role)
		}
		sort.Strings(roles)

		files, err := dataService.ListFileByProjectID(ctx, projectID, roles)
		if err != nil {
			return nil, nil, err
		}

		grouped := make(map[string][]map[string]interface{}, len(roles))
		for _, role := range roles {
			grouped[role] = make([]map[string]interface{}, 0)
		}

		for _, file := range files {
			compatItem, err := buildCompatFileItem(file)
			if err != nil {
				return nil, nil, err
			}
			grouped[file.Role] = append(grouped[file.Role], compatItem)
		}

		for role, items := range grouped {
			analysisResult[role] = items
		}
	}

	return formJSONWrap, analysisResult, nil

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

	needAssayList := false
	roleSet := make(map[string]struct{})

	for _, item := range formJSONWrap {
		formItem, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		inputType, _ := formItem["input_type"].(string)
		switch inputType {
		case "assay":
			needAssayList = true
		case "file":
			resolver, ok := formItem["resolver"].(map[string]interface{})
			if !ok {
				continue
			}
			acceptFormats := extractStringList(resolver["accept_formats"])
			for _, role := range acceptFormats {
				if role != "" {
					roleSet[role] = struct{}{}
				}
			}
		}
	}

	analysisResult := map[string]interface{}{
		"assay": make([]map[string]interface{}, 0),
	}

	if needAssayList {
		assayList, err := dataService.ListAssayByProjectID(ctx, projectID)
		if err != nil {
			return nil, nil, err
		}

		compatAssays := make([]map[string]interface{}, 0, len(assayList))
		for _, assay := range assayList {
			compatItem, err := buildCompatAssayItem(assay)
			if err != nil {
				return nil, nil, err
			}
			compatAssays = append(compatAssays, compatItem)
		}
		analysisResult["assay"] = compatAssays
	}

	if len(roleSet) > 0 {
		roles := make([]string, 0, len(roleSet))
		for role := range roleSet {
			roles = append(roles, role)
		}
		sort.Strings(roles)

		files, err := dataService.ListFileByProjectID(ctx, projectID, roles)
		if err != nil {
			return nil, nil, err
		}

		grouped := make(map[string][]map[string]interface{}, len(roles))
		for _, role := range roles {
			grouped[role] = make([]map[string]interface{}, 0)
		}

		for _, file := range files {
			compatItem, err := buildCompatFileItem(file)
			if err != nil {
				return nil, nil, err
			}
			grouped[file.Role] = append(grouped[file.Role], compatItem)
		}

		for role, items := range grouped {
			analysisResult[role] = items
		}
	}

	return formJSONWrap, analysisResult, nil
}
