package types

import "strings"

// ScriptPageQuery defines optional filters and sorting for script pagination.
// New query fields should be added here and wired in repository applyFilters.
type ScriptPageQuery struct {
	IDs []int64 `json:"ids,omitempty"`

	ID *int64 `json:"id,string,omitempty"`

	ProjectID int64 `json:"project_id,string,omitempty"`

	ScriptID string `json:"script_id,omitempty"`

	Name string `json:"name,omitempty"`

	ScriptType string `json:"script_type,omitempty"`

	Category string `json:"category,omitempty"`

	InstallKey string `json:"install_key,omitempty"`

	Tags string `json:"tags,omitempty"`

	Keywords string `json:"keywords,omitempty"`

	ContainerTemplateID *int64 `json:"container_template_id,string,omitempty"`

	SortBy string `json:"sort_by,omitempty"`

	SortOrder string `json:"sort_order,omitempty"`
}

func (q *ScriptPageQuery) GetScriptID() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.ScriptID)
}

func (q *ScriptPageQuery) GetName() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.Name)
}

func (q *ScriptPageQuery) GetScriptType() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.ScriptType)
}

func (q *ScriptPageQuery) GetCategory() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.Category)
}

func (q *ScriptPageQuery) GetInstallKey() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.InstallKey)
}

func (q *ScriptPageQuery) GetTags() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.Tags)
}

func (q *ScriptPageQuery) GetKeywords() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.Keywords)
}

func (q *ScriptPageQuery) GetSortColumn() string {
	if q == nil {
		return "id"
	}

	switch strings.ToLower(strings.TrimSpace(q.SortBy)) {
	case "id":
		return "id"
	case "script_id", "component_id":
		return "component_id"
	case "name", "component_name":
		// 排序仍落到数据库列 component_name。
		return "component_name"
	case "script_type":
		return "script_type"
	case "category":
		return "category"
	case "created_at":
		return "created_at"
	case "updated_at":
		return "updated_at"
	default:
		return "id"
	}
}

func (q *ScriptPageQuery) GetSortOrder() string {
	if q == nil {
		return "DESC"
	}

	sortOrder := strings.ToUpper(strings.TrimSpace(q.SortOrder))
	if sortOrder == "ASC" {
		return "ASC"
	}
	return "DESC"
}
