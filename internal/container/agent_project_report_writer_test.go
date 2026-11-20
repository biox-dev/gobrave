package container

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// stubReportProjectService 只实现报告写能力用到的方法，其余方法由内嵌接口兜底。
type stubReportProjectService struct {
	interfaces.ProjectService
	report  *types.ProjectReport
	content string

	created *types.ProjectReport
	updated *types.ProjectReport
	written string
}

func (s *stubReportProjectService) GetProjectReportDetailByID(_ context.Context, _ int64, reportID int64) (*types.ProjectReport, error) {
	if s.report == nil || s.report.ID != reportID {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *s.report
	return &cp, nil
}

func (s *stubReportProjectService) GetProjectReportFile(_ context.Context, _ int64, _ int64) (string, error) {
	return s.content, nil
}

func (s *stubReportProjectService) AddProjectReport(_ context.Context, _ int64, report *types.ProjectReport) error {
	s.created = report
	return nil
}

func (s *stubReportProjectService) UpdateProjectReport(_ context.Context, _ int64, report *types.ProjectReport) error {
	s.updated = report
	return nil
}

func (s *stubReportProjectService) UpdateProjectReportFile(_ context.Context, _ int64, _ int64, content string) error {
	s.written = content
	return nil
}

func newTestReportWriter(t *testing.T, svc interfaces.ProjectService) (*projectReportWriter, string) {
	t.Helper()
	baseDir := t.TempDir()
	return newProjectReportWriter(svc, nil, &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}}), baseDir
}

func TestProjectReportWriterCreateWritesOutputFile(t *testing.T) {
	if err := utils.InitSnowflake(1); err != nil {
		t.Fatalf("init snowflake: %v", err)
	}
	stub := &stubReportProjectService{}
	w, baseDir := newTestReportWriter(t, stub)

	plan, err := w.PlanCreate(context.Background(), runtimecontext.ProjectReportCreateInput{
		UserID: 1, ProjectID: 7, Title: "QC report",
	})
	if err != nil {
		t.Fatalf("plan create: %v", err)
	}
	wantFile := filepath.Join(baseDir, "data", strconv.FormatInt(7, 10), "report", strconv.FormatInt(plan.ReportID, 10), utils.DefaultProjectReportFilename)
	if plan.FilePath != wantFile {
		t.Fatalf("file path = %q, want %q", plan.FilePath, wantFile)
	}
	if plan.After != "# QC report\n" {
		t.Fatalf("after = %q", plan.After)
	}

	info, err := w.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if stub.created == nil || stub.created.ID != plan.ReportID || stub.created.Title != "QC report" {
		t.Fatalf("created report = %+v", stub.created)
	}
	if stub.written != plan.After {
		t.Fatalf("written content = %q", stub.written)
	}
	if info.ReportID != plan.ReportID || info.FilePath != plan.FilePath {
		t.Fatalf("info = %+v", info)
	}
}

func TestProjectReportWriterPlanEditRequiresUniqueMatch(t *testing.T) {
	stub := &stubReportProjectService{
		report:  &types.ProjectReport{ID: 42, ProjectID: 7, Title: "QC"},
		content: "alpha\nbeta\n",
	}
	w, _ := newTestReportWriter(t, stub)
	ctx := context.Background()

	plan, err := w.PlanEdit(ctx, runtimecontext.ProjectReportEditInput{
		UserID: 1, ReportID: 42, OldString: "beta", NewString: "gamma",
	})
	if err != nil {
		t.Fatalf("plan edit: %v", err)
	}
	if plan.After != "alpha\ngamma\n" || plan.Before != "alpha\nbeta\n" {
		t.Fatalf("before/after = %q/%q", plan.Before, plan.After)
	}

	if _, err := w.PlanEdit(ctx, runtimecontext.ProjectReportEditInput{
		UserID: 1, ReportID: 42, OldString: "alpha", NewString: "alpha",
	}); err == nil {
		t.Fatalf("expected error when replacement does not change content")
	}
}

func TestProjectReportWriterPlanUpdateChangesTitleOnly(t *testing.T) {
	stub := &stubReportProjectService{
		report:  &types.ProjectReport{ID: 42, ProjectID: 7, Title: "Old"},
		content: "# body\n",
	}
	w, _ := newTestReportWriter(t, stub)

	plan, err := w.PlanUpdate(context.Background(), runtimecontext.ProjectReportUpdateInput{
		UserID: 1, ReportID: 42, Title: "New",
	})
	if err != nil {
		t.Fatalf("plan update: %v", err)
	}
	if plan.Title != "New" || plan.After != "# body\n" {
		t.Fatalf("plan = %+v", plan)
	}

	if _, err := w.Apply(context.Background(), plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if stub.updated == nil || stub.updated.Title != "New" {
		t.Fatalf("updated report = %+v", stub.updated)
	}
}

func TestProjectReportWriterApplyRejectsChangedFile(t *testing.T) {
	stub := &stubReportProjectService{
		report:  &types.ProjectReport{ID: 42, ProjectID: 7, Title: "QC"},
		content: "changed by someone else\n",
	}
	w, _ := newTestReportWriter(t, stub)

	_, err := w.Apply(context.Background(), runtimecontext.ProjectReportWritePlan{
		UserID: 1, ReportID: 42, ProjectID: 7, Title: "QC",
		Before: "alpha\n", After: "beta\n",
	})
	if err == nil {
		t.Fatalf("expected error when file changed since the diff was prepared")
	}
}

func TestProjectResourceBrowserGetReportByID(t *testing.T) {
	stub := &stubReportProjectService{
		report:  &types.ProjectReport{ID: 42, ProjectID: 7, Title: "QC report"},
		content: "# QC report\n",
	}
	baseDir := t.TempDir()
	b := &projectResourceBrowser{projectSvc: stub, cfg: &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}}}

	detail, err := b.GetReportByID(context.Background(), 1, 7, 42)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	wantFile := stub.report.OutputFile(baseDir)
	if detail.ReportID != 42 || detail.ProjectID != 7 || detail.Title != "QC report" {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.FilePath != wantFile || detail.Content != "# QC report\n" {
		t.Fatalf("file/content = %q/%q", detail.FilePath, detail.Content)
	}

	if _, err := b.GetReportByID(context.Background(), 1, 8, 42); err == nil {
		t.Fatalf("expected error for report outside the active project")
	}
	if _, err := b.GetReportByID(context.Background(), 1, 7, 99); err == nil {
		t.Fatalf("expected error for missing report")
	}
}
