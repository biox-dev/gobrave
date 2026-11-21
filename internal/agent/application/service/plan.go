package service

import (
	"context"
	"errors"
	"strings"
	"time"

	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/utils"
)

// ReportPlanService 提供报告大纲的增删改查实现。
//
// 大纲是项目级实体：写入前由调用方（工具）用执行事实里的 ProjectID 赋值，
// 因此这里不再额外做项目归属校验（越权由工具侧的 project 事实保证）。
type reportPlanService struct {
	store ReportPlanStore
}

// NewReportPlanService 创建报告大纲服务。
func NewReportPlanService(store ReportPlanStore) ReportPlanService {
	return &reportPlanService{store: store}
}

func (s *reportPlanService) Save(ctx context.Context, plan *ReportPlan) (*ReportPlan, error) {
	if plan == nil {
		return nil, errors.New("report plan is nil")
	}
	if plan.ProjectID <= 0 {
		return nil, errors.New("project_id is required")
	}
	plan.Title = strings.TrimSpace(plan.Title)
	if plan.Title == "" {
		return nil, errors.New("title is required")
	}
	plan.Sections = NormalizeReportPlanSections(plan.Sections)

	now := time.Now()
	plan.UpdatedAt = now
	if plan.ID == 0 {
		plan.CreatedAt = now
	} else if plan.CreatedAt.IsZero() {
		if stored, err := s.store.Get(ctx, plan.ID); err == nil {
			plan.CreatedAt = stored.CreatedAt
		}
	}
	if err := s.store.Save(ctx, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func (s *reportPlanService) Get(ctx context.Context, id int64) (*ReportPlan, error) {
	if id <= 0 {
		return nil, ErrReportPlanNotFound
	}
	return s.store.Get(ctx, id)
}

func (s *reportPlanService) ListByProject(ctx context.Context, projectID int64) ([]*ReportPlan, error) {
	if projectID <= 0 {
		return nil, nil
	}
	return s.store.ListByProject(ctx, projectID)
}

func (s *reportPlanService) Delete(ctx context.Context, id int64) error {
	return s.store.Delete(ctx, id)
}

// NormalizeReportPlanSections 清洗章节树：补齐 ID、去空白、丢弃空标题、校验来源类型、
// 断开悬空 / 自引用的父节点（提升为根）。同级顺序由调用方按 SortOrder 维护。
func NormalizeReportPlanSections(sections []ReportPlanSection) []ReportPlanSection {
	if len(sections) == 0 {
		return nil
	}

	existing := make(map[int64]struct{}, len(sections))
	for i := range sections {
		if sections[i].ID <= 0 {
			sections[i].ID = utils.GenerateID()
		}
		existing[sections[i].ID] = struct{}{}
	}

	out := make([]ReportPlanSection, 0, len(sections))
	for _, section := range sections {
		section.Title = strings.TrimSpace(section.Title)
		if section.Title == "" {
			continue
		}
		section.Summary = strings.TrimSpace(section.Summary)
		if !section.Source.Valid() {
			section.Source = ReportPlanSourceCustom
		}
		if section.Source == ReportPlanSourceCustom {
			section.SourceID = 0
		}
		if _, ok := existing[section.ParentID]; !ok || section.ParentID == section.ID {
			section.ParentID = 0
		}
		if section.SortOrder < 0 {
			section.SortOrder = 0
		}
		out = append(out, section)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
