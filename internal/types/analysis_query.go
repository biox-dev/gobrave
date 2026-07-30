package types

import "strings"

// AnalysisQuey defines optional filters and sorting for analysis pagination.
// Keep the type name for compatibility with existing callers.
type AnalysisQuey struct {
	IDs []int64 `json:"ids,omitempty"`

	ID *int64 `json:"id,string,omitempty"`

	ProjectID int64 `json:"project_id,string,omitempty"`

	AnalysisID string `json:"analysis_id,omitempty"`

	AnalysisName string `json:"analysis_name,omitempty"`

	WorkflowID string `json:"relation_id,omitempty"`

	JobStatus string `json:"job_status,omitempty"`

	ServerStatus string `json:"server_status,omitempty"`

	IsReport *bool `json:"is_report,omitempty"`

	CacheType *int `json:"cache_type,omitempty"`

	SortBy string `json:"sort_by,omitempty"`

	SortOrder string `json:"sort_order,omitempty"`
}

// AnalysisQuery is an alias of AnalysisQuey for clearer naming in new code.
type AnalysisQuery = AnalysisQuey

func (q *AnalysisQuey) GetAnalysisID() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.AnalysisID)
}

func (q *AnalysisQuey) GetAnalysisName() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.AnalysisName)
}

func (q *AnalysisQuey) GetWorkflowID() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.WorkflowID)
}

func (q *AnalysisQuey) GetJobStatus() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.JobStatus)
}

func (q *AnalysisQuey) GetServerStatus() string {
	if q == nil {
		return ""
	}
	return strings.TrimSpace(q.ServerStatus)
}

func (q *AnalysisQuey) GetSortColumn() string {
	if q == nil {
		return "created_at"
	}

	switch strings.ToLower(strings.TrimSpace(q.SortBy)) {
	case "id":
		return "id"
	case "analysis_id":
		return "analysis_id"
	case "analysis_name":
		return "analysis_name"
	case "relation_id", "workflow_id":
		return "relation_id"
	case "job_status":
		return "job_status"
	case "server_status":
		return "server_status"
	case "cache_type":
		return "cache_type"
	case "created_at":
		return "created_at"
	case "updated_at":
		return "updated_at"
	default:
		return "created_at"
	}
}

func (q *AnalysisQuey) GetSortOrder() string {
	if q == nil {
		return "DESC"
	}

	sortOrder := strings.ToUpper(strings.TrimSpace(q.SortOrder))
	if sortOrder == "ASC" {
		return "ASC"
	}
	return "DESC"
}
