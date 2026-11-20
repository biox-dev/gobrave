package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// fakeReportWriter 是最小的 ProjectReportWriter 实现，用于验证工具行为。
type fakeReportWriter struct {
	plan     runtimecontext.ProjectReportWritePlan
	planErr  error
	createIn *runtimecontext.ProjectReportCreateInput
	editIn   *runtimecontext.ProjectReportEditInput
	updateIn *runtimecontext.ProjectReportUpdateInput
	applied  *runtimecontext.ProjectReportWritePlan
}

func (f *fakeReportWriter) PlanCreate(_ context.Context, in runtimecontext.ProjectReportCreateInput) (runtimecontext.ProjectReportWritePlan, error) {
	f.createIn = &in
	if f.planErr != nil {
		return runtimecontext.ProjectReportWritePlan{}, f.planErr
	}
	return f.plan, nil
}

func (f *fakeReportWriter) PlanEdit(_ context.Context, in runtimecontext.ProjectReportEditInput) (runtimecontext.ProjectReportWritePlan, error) {
	f.editIn = &in
	if f.planErr != nil {
		return runtimecontext.ProjectReportWritePlan{}, f.planErr
	}
	return f.plan, nil
}

func (f *fakeReportWriter) PlanUpdate(_ context.Context, in runtimecontext.ProjectReportUpdateInput) (runtimecontext.ProjectReportWritePlan, error) {
	f.updateIn = &in
	if f.planErr != nil {
		return runtimecontext.ProjectReportWritePlan{}, f.planErr
	}
	return f.plan, nil
}

func (f *fakeReportWriter) Apply(_ context.Context, plan runtimecontext.ProjectReportWritePlan) (runtimecontext.ProjectReportWriteInfo, error) {
	f.applied = &plan
	return runtimecontext.ProjectReportWriteInfo{
		ReportID:  plan.ReportID,
		ProjectID: plan.ProjectID,
		Title:     plan.Title,
		FilePath:  plan.FilePath,
	}, nil
}

// reportTestFacts 返回带用户 / 激活项目的执行事实 ctx。
func reportTestFacts() context.Context {
	return tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{UserID: 9, ProjectID: 7})
}

func TestCreateReportUsesFactsAndApplies(t *testing.T) {
	w := &fakeReportWriter{plan: runtimecontext.ProjectReportWritePlan{
		ReportID:  42,
		ProjectID: 7,
		Title:     "QC report",
		FilePath:  "/data/7/report/42/output.md",
		After:     "# QC report\n",
		Create:    true,
	}}

	out, err := CreateReport(w).Execute(reportTestFacts(),
		json.RawMessage(`{"title":"QC report","content":"# QC report\n"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(ReportWriteOutput)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if w.createIn == nil || w.createIn.ProjectID != 7 || w.createIn.UserID != 9 {
		t.Fatalf("PlanCreate input = %+v", w.createIn)
	}
	if w.applied == nil {
		t.Fatalf("Apply was not called")
	}
	if res.ReportID != 42 || res.FilePath != "/data/7/report/42/output.md" {
		t.Fatalf("output = %+v", res)
	}
}

func TestUpdateReportTextReplacementMode(t *testing.T) {
	w := &fakeReportWriter{plan: runtimecontext.ProjectReportWritePlan{
		ReportID:  42,
		ProjectID: 7,
		Title:     "QC report",
		FilePath:  "/data/7/report/42/output.md",
		Before:    "# old\n",
		After:     "# new\n",
	}}

	_, err := UpdateReport(w).Execute(reportTestFacts(),
		json.RawMessage(`{"report_id":"@report:42","old_string":"old","new_string":"new"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if w.editIn == nil || w.editIn.ReportID != 42 || w.editIn.OldString != "old" || w.editIn.NewString != "new" {
		t.Fatalf("PlanEdit input = %+v", w.editIn)
	}
	if w.updateIn != nil {
		t.Fatalf("PlanUpdate should not be called")
	}
	if w.applied == nil {
		t.Fatalf("Apply was not called")
	}
}

func TestUpdateReportFieldOverwriteMode(t *testing.T) {
	w := &fakeReportWriter{plan: runtimecontext.ProjectReportWritePlan{
		ReportID:  42,
		ProjectID: 7,
		Title:     "New title",
		FilePath:  "/data/7/report/42/output.md",
		Before:    "# old\n",
		After:     "# new body\n",
	}}

	_, err := UpdateReport(w).Execute(reportTestFacts(),
		json.RawMessage(`{"report_id":"42","title":"New title","content":"# new body\n"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if w.updateIn == nil || w.updateIn.ReportID != 42 || w.updateIn.Title != "New title" {
		t.Fatalf("PlanUpdate input = %+v", w.updateIn)
	}
	if w.editIn != nil {
		t.Fatalf("PlanEdit should not be called")
	}
}

func TestUpdateReportRejectsMixedModes(t *testing.T) {
	w := &fakeReportWriter{}

	out, err := UpdateReport(w).Execute(reportTestFacts(),
		json.RawMessage(`{"report_id":"42","title":"T","old_string":"a","new_string":"b"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(ReportWriteOutput)
	if res.Error == "" {
		t.Fatalf("expected an error for mixed edit modes")
	}
	if w.applied != nil {
		t.Fatalf("Apply should not be called")
	}
}
