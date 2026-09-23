package handler

import (
	"context"
	"encoding/json"
	stderrs "errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

func buildParseAnalysisResult(ctx context.Context, dataService interfaces.DataService, requestParam map[string]interface{}, formJSONWrap []interface{}) (map[string]interface{}, error) {
	queryNameDict := getQueryDBField(formJSONWrap)
	queryNames := make([]string, 0, len(queryNameDict))
	for _, item := range formJSONWrap {
		formItem, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := formItem["name"].(string)
		if name == "" {
			continue
		}
		if _, ok := queryNameDict[name]; ok {
			queryNames = append(queryNames, name)
		}
	}

	analysisDict, err := buildAnalysisDictFromDB(ctx, dataService, requestParam, queryNames, queryNameDict)
	if err != nil {
		return nil, err
	}

	groupsName := make(map[string]interface{})
	reGroupsName := make(map[string]interface{})
	colors := make(map[string]interface{})
	for _, key := range queryNames {
		value, exists := requestParam[key]
		if !exists {
			continue
		}
		groupsName[key] = getGroupName(value)
		reGroupsName[key] = getReGroupName(value)
		colors[key] = getColor(value)
	}

	formNames := make(map[string]struct{})
	for _, item := range formJSONWrap {
		formItem, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if itemType, _ := formItem["type"].(string); itemType == "Divider" {
			continue
		}
		name, _ := formItem["name"].(string)
		if name == "" {
			continue
		}
		formNames[name] = struct{}{}
	}

	extraDict := make(map[string]interface{})
	for key, value := range requestParam {
		if _, ok := formNames[key]; ok {
			extraDict[key] = value
			continue
		}
		if strings.HasPrefix(key, "__") {
			extraDict[key] = value
		}
	}

	result := make(map[string]interface{}, len(extraDict)+len(analysisDict)+6)
	for k, v := range extraDict {
		result[k] = v
	}
	for k, v := range analysisDict {
		result[k] = v
	}
	result["analysis_name"] = requestParam["analysis_name"]
	result["colors"] = colors
	result["groups_name"] = groupsName
	result["re_groups_name"] = reGroupsName
	result["groups"] = queryNames
	result["pipeline_dir"] = strings.TrimSpace(os.Getenv("PIPELINE_DIR"))

	return result, nil
}

func buildAnalysisDictFromDB(
	ctx context.Context,
	dataService interfaces.DataService,
	requestParam map[string]interface{},
	queryNames []string,
	queryNameDict map[string]map[string]interface{},
) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	assaysByName := loadAssaysByName(ctx, dataService, requestParam)

	for _, key := range queryNames {
		formItem := queryNameDict[key]
		inputType := strings.TrimSpace(anyToString(formItem["input_type"]))
		if inputType == "assay" {
			rawValue, exists := requestParam[key]
			if !exists {
				continue
			}

			resolved, err := resolveAssayInputValue(ctx, dataService, formItem, rawValue)
			if err != nil {
				return nil, fmt.Errorf("resolve assay db fields for %s failed: %w", key, err)
			}
			result[key] = resolved
			continue
		}

		if inputType != "file" {
			continue
		}

		rawValue, exists := requestParam[key]
		if !exists {
			continue
		}

		formType := anyToString(formItem["type"])
		if formType == "NestSelectAssayV2" {
			resolved, err := resolveNestSelectAssayV2Value(ctx, dataService, formItem, rawValue)
			if err != nil {
				return nil, fmt.Errorf("resolve nest select db fields for %s failed: %w", key, err)
			}
			result[key] = resolved
			continue
		}

		ids, isSingle := extractIDs(rawValue)
		if len(ids) == 0 {
			continue
		}

		items, err := findCompatAnalysisResultByIDs(ctx, dataService, ids)
		if err != nil {
			return nil, fmt.Errorf("resolve %s ids failed: %w", key, err)
		}

		selectedGroupName := getGroupName(rawValue)
		reGroupName := getReGroupName(rawValue)
		extraByID := extractRequestExtrasByID(rawValue)

		if formType == "CollectedGroupSelectAssayButton" {
			analysisResult := copyAnyMap(items[0])
			columns := toInterfaceSlice(formItem["columns"])
			analysisResult["form_type"] = formType
			analysisResult["groups"] = columns

			requestMap, _ := rawValue.(map[string]interface{})
			selectedGroupMap, _ := selectedGroupName.(map[string]string)
			reGroupMap, _ := reGroupName.(map[string]interface{})

			for _, groupAny := range columns {
				groupName := anyToString(groupAny)
				if groupName == "" {
					continue
				}

				groupValue := requestMap[groupName]
				switch v := groupValue.(type) {
				case []interface{}:
					groupItems := make([]interface{}, 0, len(v))
					for _, col := range v {
						built := buildCollectedAnalysisResult(col, analysisResult, assaysByName)
						built["selcted_group_name"] = selectedGroupMap[groupName]
						built["re_groups_name"] = reGroupMap[groupName]
						groupItems = append(groupItems, built)
					}
					analysisResult[groupName] = groupItems
				default:
					analysisResult[groupName] = buildCollectedAnalysisResult(v, analysisResult, assaysByName)
				}
			}

			result[key] = analysisResult
			continue
		}

		if formType == "CollectedAssaySelect" {
			analysisResult := copyAnyMap(items[0])
			if requestMap, ok := rawValue.(map[string]interface{}); ok {
				for rk, rv := range requestMap {
					analysisResult[rk] = rv
				}
			}
			analysisResult["form_type"] = formType
			analysisResult["groups"] = toInterfaceSlice(formItem["columns"])
			result[key] = analysisResult
			continue
		}

		if formType == "NestCollectedAssaySelect" {
			itemByID := make(map[string]map[string]interface{}, len(items))
			for _, item := range items {
				itemByID[anyToString(item["id"])] = item
			}

			requestList, _ := rawValue.([]interface{})
			mergedList := make([]interface{}, 0, len(requestList))
			for _, one := range requestList {
				requestOne, ok := one.(map[string]interface{})
				if !ok {
					continue
				}
				merged := copyAnyMap(requestOne)
				fileID := anyToString(requestOne["file"])
				if dbItem, ok := itemByID[fileID]; ok {
					for dk, dv := range dbItem {
						merged[dk] = dv
					}
				}
				mergedList = append(mergedList, merged)
			}
			result[key] = mergedList
			continue
		}

		if isSingle {
			one := copyAnyMap(items[0])
			mergeMissingMapFields(one, extraByID[anyToString(one["id"])])
			one["form_type"] = formType
			one["selcted_group_name"] = selectedGroupName
			one["re_groups_name"] = reGroupName
			result[key] = one
			continue
		}

		list := make([]interface{}, 0, len(items))
		itemByID := make(map[string]map[string]interface{}, len(items))
		for _, item := range items {
			itemByID[anyToString(item["id"])] = item
		}
		if rawList, ok := rawValue.([]interface{}); ok {
			list = make([]interface{}, 0, len(rawList))
			for _, one := range rawList {
				id, extras := extractOneIDAndExtras(one)
				if id == "" {
					continue
				}
				item, ok := itemByID[id]
				if !ok {
					continue
				}
				e := copyAnyMap(item)
				mergeMissingMapFields(e, extras)
				e["form_type"] = formType
				e["selcted_group_name"] = selectedGroupName
				e["re_groups_name"] = reGroupName
				list = append(list, e)
			}
		} else {
			list = make([]interface{}, 0, len(ids))
			for _, id := range ids {
				item, ok := itemByID[anyToString(id)]
				if !ok {
					continue
				}
				e := copyAnyMap(item)
				mergeMissingMapFields(e, extraByID[anyToString(id)])
				e["form_type"] = formType
				e["selcted_group_name"] = selectedGroupName
				e["re_groups_name"] = reGroupName
				list = append(list, e)
			}
		}
		result[key] = list
	}

	return result, nil
}

func findCompatAnalysisResultByIDs(ctx context.Context, dataService interfaces.DataService, ids []string) ([]map[string]interface{}, error) {
	result := make([]map[string]interface{}, 0, len(ids))
	seen := make(map[string]struct{})

	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}

		file, err := findFileByIDOrFileID(ctx, dataService, id)
		if err != nil {
			return nil, err
		}
		if file == nil {
			return nil, fmt.Errorf("data inconsistency: file id %s not found", id)
		}

		result = append(result, buildCompatAnalysisResultItem(file))
	}

	if len(result) != len(seen) {
		return nil, fmt.Errorf("data inconsistency: expected %d files, got %d", len(seen), len(result))
	}

	return result, nil
}

func findFileByIDOrFileID(ctx context.Context, dataService interfaces.DataService, rawID string) (*types.File, error) {
	if n, err := strconv.ParseInt(rawID, 10, 64); err == nil {
		file, err := dataService.GetFileByID(ctx, n)
		if err == nil {
			return file, nil
		}
		if !stderrs.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}

	file, err := dataService.GetFileByFileID(ctx, rawID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return file, nil
}

func buildCompatAnalysisResultItem(file *types.File) map[string]interface{} {
	id := strconv.FormatInt(file.ID, 10)
	return map[string]interface{}{
		"id":                 id,
		"analysis_result_id": id,
		"assay_id":           "",
		"file_name":          file.FileName,
		"component_id":       "",
		"file_type":          file.Format,
		"path":               file.Path,
		"format":             file.Format,
		"size":               file.Size,
		"md5":                file.MD5,
		"storage":            file.Storage,
		"description":        file.Description,
	}
}

// assayDisplayName 返回 Assay 的展示名：library_id 优先，其次 assay_type，最后回退主键。
func assayDisplayName(item map[string]interface{}) string {
	for _, key := range []string{"library_id", "assay_type"} {
		if s := strings.TrimSpace(anyToString(item[key])); s != "" {
			return s
		}
	}
	return strings.TrimSpace(anyToString(item["id"]))
}

// assayColumnName 返回用于列匹配的 Assay 名称，与 buildCompatAssayItem 的 assay_name 保持一致。
func assayColumnName(assay *types.AssayWithDatasetInfo) string {
	if name := strings.TrimSpace(assay.LibraryID); name != "" {
		return name
	}
	if name := strings.TrimSpace(assay.AssayType); name != "" {
		return name
	}
	return strconv.FormatInt(assay.ID, 10)
}

func loadAssaysByName(ctx context.Context, dataService interfaces.DataService, requestParam map[string]interface{}) map[string]map[string]interface{} {
	projectID := strings.TrimSpace(anyToString(requestParam["project"]))
	if projectID == "" {
		return map[string]map[string]interface{}{}
	}

	assays, err := dataService.ListAssayByProjectID(ctx, projectID)
	if err != nil {
		return map[string]map[string]interface{}{}
	}

	result := make(map[string]map[string]interface{}, len(assays))
	for _, assay := range assays {
		metadata := parseMetadataJSON(assay.Metadata)
		name := assayColumnName(assay)
		item := map[string]interface{}{
			"assay_id":   strconv.FormatInt(assay.ID, 10),
			"assay_name": name,
			"assay_type": assay.AssayType,
			"platform":   assay.Platform,
			"library_id": assay.LibraryID,
			"sample_id":  strconv.FormatInt(assay.SampleID, 10),
		}
		for k, v := range metadata {
			item[k] = v
		}
		result[name] = item
	}

	return result
}

func parseMetadataJSON(raw string) map[string]interface{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]interface{}{}
	}
	meta := make(map[string]interface{})
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return map[string]interface{}{}
	}
	result := make(map[string]interface{}, len(meta))
	for k, v := range meta {
		if v == nil {
			continue
		}
		result[k] = fmt.Sprint(v)
	}
	return result
}

func buildCollectedAnalysisResult(column interface{}, analysisResult map[string]interface{}, assaysByName map[string]map[string]interface{}) map[string]interface{} {
	columnName := anyToString(column)
	if assay, ok := assaysByName[columnName]; ok {
		result := copyAnyMap(assay)
		result["id"] = analysisResult["id"]
		result["analysis_result_id"] = analysisResult["analysis_result_id"]
		result["columns_name"] = columnName
		return result
	}

	return map[string]interface{}{
		"id":                 analysisResult["id"],
		"assay_name":         columnName,
		"analysis_result_id": analysisResult["analysis_result_id"],
		"columns_name":       columnName,
	}
}

func toInterfaceSlice(v interface{}) []interface{} {
	if items, ok := v.([]interface{}); ok {
		return items
	}
	return []interface{}{}
}

func getQueryDBField(formJSONWrap []interface{}) map[string]map[string]interface{} {
	merged := mergeColumnsByName(formJSONWrap)
	result := make(map[string]map[string]interface{})
	for _, item := range merged {
		name, _ := item["name"].(string)
		if name == "" {
			continue
		}
		dbRaw, exists := item["db"]
		if !exists {
			continue
		}
		dbFlag, ok := dbRaw.(bool)
		if !ok || !dbFlag {
			continue
		}
		result[name] = item
	}
	return result
}

func mergeColumnsByName(formJSONWrap []interface{}) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(formJSONWrap))
	assayMap := make(map[string]map[string]interface{})
	columnsMap := make(map[string][][]interface{})

	for _, item := range formJSONWrap {
		formItem, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		copied := copyAnyMap(formItem)
		result = append(result, copied)
		name, _ := copied["name"].(string)
		itemType, _ := copied["type"].(string)

		switch itemType {
		case "CollectedAssaySelect":
			if name != "" {
				assayMap[name] = copied
			}
		case "CollectedColumnsSelect":
			if name == "" {
				continue
			}
			if cols, ok := copied["columns"].([]interface{}); ok {
				columnsMap[name] = append(columnsMap[name], cols)
			}
		}
	}

	for name, assay := range assayMap {
		current, _ := assay["columns"].([]interface{})
		merged := make([]interface{}, 0, len(current)+8)
		merged = append(merged, current...)
		for _, cols := range columnsMap[name] {
			merged = append(merged, cols...)
		}
		assay["columns"] = merged
	}

	return result
}

func getGroupName(value interface{}) interface{} {
	m, ok := value.(map[string]interface{})
	if !ok {
		return "-"
	}

	group, exists := m["group"]
	if !exists {
		return "-"
	}

	if groupDict, ok := group.(map[string]interface{}); ok {
		result := make(map[string]string)
		for k, v := range groupDict {
			items := toStringSlice(v)
			if len(items) == 0 {
				continue
			}
			result[k] = strings.Join(items, "-")
		}
		return result
	}

	items := toStringSlice(group)
	if len(items) == 0 {
		return "-"
	}
	if len(items) == 1 {
		return items[0]
	}
	return strings.Join(items, "-")
}

func getReGroupName(value interface{}) interface{} {
	m, ok := value.(map[string]interface{})
	if !ok {
		return "-"
	}
	groupName, exists := m["group_name"]
	if !exists {
		return "-"
	}
	if groupMap, ok := groupName.(map[string]interface{}); ok {
		result := make(map[string]interface{})
		for k, v := range groupMap {
			s, ok := v.(string)
			if !ok || strings.TrimSpace(s) == "" {
				continue
			}
			result[k] = s
		}
		return result
	}
	return groupName
}

func getColor(value interface{}) interface{} {
	m, ok := value.(map[string]interface{})
	if !ok {
		return "-"
	}
	if color, ok := m["color"]; ok {
		return color
	}
	return "-"
}

func toStringSlice(value interface{}) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []interface{}:
		result := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok || strings.TrimSpace(s) == "" {
				continue
			}
			result = append(result, s)
		}
		return result
	default:
		return nil
	}
}

func copyAnyMap(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func extractIDs(value interface{}) ([]string, bool) {
	switch v := value.(type) {
	case map[string]interface{}:
		if fileID, ok := v["file"]; ok {
			id := strings.TrimSpace(anyToString(fileID))
			if id == "" {
				return nil, true
			}
			return []string{id}, true
		}
		if assay, ok := v["assay"]; ok {
			ids := extractIDList(assay)
			if len(ids) == 1 {
				return ids, true
			}
			return ids, false
		}
		if val, ok := v["value"]; ok {
			id := strings.TrimSpace(anyToString(val))
			if id == "" {
				return nil, true
			}
			return []string{id}, true
		}
		return nil, false
	case []interface{}:
		ids := make([]string, 0, len(v))
		for _, item := range v {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if fileID, ok := m["file"]; ok {
				id := strings.TrimSpace(anyToString(fileID))
				if id != "" {
					ids = append(ids, id)
				}
				continue
			}
			if assay, ok := m["assay"]; ok {
				ids = append(ids, extractIDList(assay)...)
				continue
			}
			if val, ok := m["value"]; ok {
				id := strings.TrimSpace(anyToString(val))
				if id != "" {
					ids = append(ids, id)
				}
			}
		}
		return ids, false
	default:
		id := strings.TrimSpace(anyToString(value))
		if id == "" {
			return nil, true
		}
		return []string{id}, true
	}
}

func extractRequestExtrasByID(value interface{}) map[string]map[string]interface{} {
	result := make(map[string]map[string]interface{})

	appendOne := func(raw map[string]interface{}) {
		if raw == nil {
			return
		}
		id := extractPrimaryID(raw)
		if id == "" {
			return
		}
		extras := copyAnyMap(raw)
		delete(extras, "file")
		delete(extras, "assay")
		delete(extras, "value")
		result[id] = extras
	}

	switch v := value.(type) {
	case map[string]interface{}:
		appendOne(v)
	case []interface{}:
		for _, one := range v {
			raw, ok := one.(map[string]interface{})
			if !ok {
				continue
			}
			appendOne(raw)
		}
	}

	return result
}

func extractPrimaryID(raw map[string]interface{}) string {
	id := strings.TrimSpace(anyToString(raw["file"]))
	if id != "" {
		return id
	}
	id = strings.TrimSpace(anyToString(raw["assay"]))
	if id != "" {
		return id
	}
	id = strings.TrimSpace(anyToString(raw["value"]))
	if id != "" {
		return id
	}
	return ""
}

func extractOneIDAndExtras(value interface{}) (string, map[string]interface{}) {
	raw, ok := value.(map[string]interface{})
	if !ok {
		id := strings.TrimSpace(anyToString(value))
		if id == "" {
			return "", nil
		}
		return id, nil
	}

	id := strings.TrimSpace(anyToString(raw["file"]))
	if id == "" {
		ids := extractIDList(raw["assay"])
		if len(ids) > 0 {
			id = strings.TrimSpace(ids[0])
		}
	}
	if id == "" {
		id = strings.TrimSpace(anyToString(raw["value"]))
	}
	if id == "" {
		return "", nil
	}

	extras := copyAnyMap(raw)
	delete(extras, "file")
	delete(extras, "assay")
	delete(extras, "value")
	return id, extras
}

func mergeMissingMapFields(dst map[string]interface{}, src map[string]interface{}) {
	if dst == nil || src == nil {
		return
	}
	for k, v := range src {
		if _, exists := dst[k]; exists {
			continue
		}
		dst[k] = v
	}
}

func extractIDList(value interface{}) []string {
	switch v := value.(type) {
	case []interface{}:
		result := make([]string, 0, len(v))
		for _, item := range v {
			id := strings.TrimSpace(anyToString(item))
			if id == "" {
				continue
			}
			result = append(result, id)
		}
		return result
	default:
		id := strings.TrimSpace(anyToString(v))
		if id == "" {
			return nil
		}
		return []string{id}
	}
}

func anyToString(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatInt(int64(x), 10)
	default:
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
}

func resolveNestSelectAssayV2Value(
	ctx context.Context,
	dataService interfaces.DataService,
	formItem map[string]interface{},
	rawValue interface{},
) (interface{}, error) {
	appendDBFields := getNestSelectAssayV2AppendDBFileFields(formItem)
	if len(appendDBFields) == 0 {
		return rawValue, nil
	}

	fileCache := make(map[string]map[string]interface{})
	convert := func(item interface{}) (interface{}, error) {
		row, ok := item.(map[string]interface{})
		if !ok {
			return item, nil
		}

		copied := copyAnyMap(row)
		for _, fieldName := range appendDBFields {
			resolvedField, err := resolveNestedDBFileValue(ctx, dataService, copied[fieldName], fileCache)
			if err != nil {
				return nil, err
			}
			copied[fieldName] = resolvedField
		}
		return copied, nil
	}

	switch value := rawValue.(type) {
	case []interface{}:
		result := make([]interface{}, 0, len(value))
		for _, one := range value {
			converted, err := convert(one)
			if err != nil {
				return nil, err
			}
			result = append(result, converted)
		}
		return result, nil
	default:
		return convert(value)
	}
}

func getNestSelectAssayV2AppendDBFileFields(formItem map[string]interface{}) []string {
	appendItems := toInterfaceSlice(formItem["append"])
	result := make([]string, 0, len(appendItems))

	for _, one := range appendItems {
		appendItem, ok := one.(map[string]interface{})
		if !ok {
			continue
		}
		if strings.TrimSpace(anyToString(appendItem["input_type"])) != "file" {
			continue
		}
		dbFlag, _ := appendItem["db"].(bool)
		if !dbFlag {
			continue
		}
		name := strings.TrimSpace(anyToString(appendItem["name"]))
		if name == "" {
			continue
		}
		result = append(result, name)
	}

	return result
}

func resolveNestedDBFileValue(
	ctx context.Context,
	dataService interfaces.DataService,
	value interface{},
	fileCache map[string]map[string]interface{},
) (interface{}, error) {
	switch v := value.(type) {
	case map[string]interface{}:
		copied := copyAnyMap(v)
		fileID := strings.TrimSpace(anyToString(copied["file"]))
		if fileID == "" {
			return copied, nil
		}
		fileObj, err := loadCompatFileObjectByID(ctx, dataService, fileID, fileCache)
		if err != nil {
			return nil, err
		}
		merged := copyAnyMap(fileObj)
		mergeMissingMapFields(merged, copied)
		return merged, nil
	default:
		fileID := strings.TrimSpace(anyToString(value))
		if fileID == "" {
			return value, nil
		}
		fileObj, err := loadCompatFileObjectByID(ctx, dataService, fileID, fileCache)
		if err != nil {
			return nil, err
		}
		merged := copyAnyMap(fileObj)
		merged["file"] = fileID
		return merged, nil
	}
}

func loadCompatFileObjectByID(
	ctx context.Context,
	dataService interfaces.DataService,
	fileID string,
	fileCache map[string]map[string]interface{},
) (map[string]interface{}, error) {
	if cached, ok := fileCache[fileID]; ok {
		return copyAnyMap(cached), nil
	}

	file, err := findFileByIDOrFileID(ctx, dataService, fileID)
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, fmt.Errorf("data inconsistency: file id %s not found", fileID)
	}

	item := buildCompatAnalysisResultItem(file)
	fileCache[fileID] = item
	return copyAnyMap(item), nil
}

func resolveAssayInputValue(
	ctx context.Context,
	dataService interfaces.DataService,
	formItem map[string]interface{},
	rawValue interface{},
) (interface{}, error) {
	assayIDs := extractAssayIDsFromValue(rawValue)
	if len(assayIDs) == 0 {
		return []interface{}{}, nil
	}

	acceptFormats := getAssayAcceptFormats(formItem)
	acceptFormatByLower := make(map[string]string, len(acceptFormats))
	for _, format := range acceptFormats {
		acceptFormatByLower[strings.ToLower(strings.TrimSpace(format))] = format
	}

	selected := make(map[int64]struct{}, len(assayIDs))
	for _, id := range assayIDs {
		if idNum, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64); err == nil {
			selected[idNum] = struct{}{}
		}
	}
	if len(selected) == 0 {
		return []interface{}{}, nil
	}

	assayRoleToPath := make(map[int64]map[string]string)

	// Files are assay-private (go_file.assay_id), so every selected assay is
	// resolved on its own; an assay without files contributes an empty map.
	for assayID := range selected {
		files, err := dataService.ListFileByAssayID(ctx, assayID)
		if err != nil {
			return nil, err
		}

		roleToPath := make(map[string]string, len(files))
		for _, file := range files {
			if file == nil {
				continue
			}

			role := strings.TrimSpace(file.Role)
			if len(acceptFormatByLower) > 0 {
				mapped, ok := acceptFormatByLower[strings.ToLower(role)]
				if !ok {
					continue
				}
				role = mapped
			}
			if role == "" {
				continue
			}
			if existing := strings.TrimSpace(roleToPath[role]); existing != "" {
				continue
			}

			path := strings.TrimSpace(file.Path)
			if path == "" {
				path = strings.TrimSpace(file.FileID)
			}
			roleToPath[role] = path
		}

		assayRoleToPath[assayID] = roleToPath
	}

	result := make([]interface{}, 0, len(assayIDs))
	for _, assayID := range assayIDs {
		assayIDNum, err := strconv.ParseInt(strings.TrimSpace(assayID), 10, 64)
		if err != nil {
			continue
		}

		row := map[string]interface{}{
			"ID": assayID,
		}

		if len(acceptFormats) > 0 {
			for _, format := range acceptFormats {
				row[format] = ""
			}
		}

		roleMap := assayRoleToPath[assayIDNum]
		if len(roleMap) > 0 {
			if len(acceptFormats) == 0 {
				roleKeys := make([]string, 0, len(roleMap))
				for role := range roleMap {
					roleKeys = append(roleKeys, role)
				}
				sort.Strings(roleKeys)
				for _, role := range roleKeys {
					row[role] = roleMap[role]
				}
			} else {
				for _, format := range acceptFormats {
					if path, ok := roleMap[format]; ok {
						row[format] = path
					}
				}
			}
		}

		result = append(result, row)
	}

	return result, nil
}

func extractAssayIDsFromValue(value interface{}) []string {
	switch v := value.(type) {
	case map[string]interface{}:
		if assay, ok := v["assay"]; ok {
			return extractIDList(assay)
		}
		return nil
	default:
		return extractIDList(v)
	}
}

func getAssayAcceptFormats(formItem map[string]interface{}) []string {
	resolver, _ := formItem["resolver"].(map[string]interface{})
	if resolver == nil {
		return nil
	}

	raw := resolver["accept_formats"]
	formats := make([]string, 0)
	for _, one := range toInterfaceSlice(raw) {
		format := strings.TrimSpace(anyToString(one))
		if format == "" {
			continue
		}
		formats = append(formats, format)
	}

	if len(formats) == 0 {
		return nil
	}
	return formats
}
