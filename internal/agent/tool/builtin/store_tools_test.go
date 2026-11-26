package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// storeTestFacts 返回带用户 / 激活项目的执行事实 ctx。
func storeTestFacts(userID, projectID int64) context.Context {
	return tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{UserID: userID, ProjectID: projectID})
}

// fakeStoreBrowser 是最小的 StoreBrowser 实现。
type fakeStoreBrowser struct {
	gotUserID int64
	gotIn     runtimecontext.StorePageInput
	result    runtimecontext.StorePageResult
	err       error
}

func (f *fakeStoreBrowser) PageStore(_ context.Context, userID int64, in runtimecontext.StorePageInput) (runtimecontext.StorePageResult, error) {
	f.gotUserID = userID
	f.gotIn = in
	if f.err != nil {
		return runtimecontext.StorePageResult{}, f.err
	}
	return f.result, nil
}

// fakeStorePublisher 是最小的 StoreComponentPublisher 实现。
type fakeStorePublisher struct {
	workflowIn *runtimecontext.StorePublishWorkflowInput
	scriptIn   *runtimecontext.StorePublishScriptInput
	result     runtimecontext.StorePublishResult
	err        error
}

func (f *fakeStorePublisher) PublishWorkflow(_ context.Context, in runtimecontext.StorePublishWorkflowInput) (runtimecontext.StorePublishResult, error) {
	f.workflowIn = &in
	if f.err != nil {
		return runtimecontext.StorePublishResult{}, f.err
	}
	return f.result, nil
}

func (f *fakeStorePublisher) PublishScript(_ context.Context, in runtimecontext.StorePublishScriptInput) (runtimecontext.StorePublishResult, error) {
	f.scriptIn = &in
	if f.err != nil {
		return runtimecontext.StorePublishResult{}, f.err
	}
	return f.result, nil
}

// fakeStoreInstaller 是最小的 StoreComponentInstaller 实现。
type fakeStoreInstaller struct {
	workflowIn *runtimecontext.StoreInstallWorkflowInput
	scriptIn   *runtimecontext.StoreInstallScriptInput
	result     runtimecontext.StoreInstallResult
	err        error
}

func (f *fakeStoreInstaller) InstallWorkflow(_ context.Context, in runtimecontext.StoreInstallWorkflowInput) (runtimecontext.StoreInstallResult, error) {
	f.workflowIn = &in
	if f.err != nil {
		return runtimecontext.StoreInstallResult{}, f.err
	}
	return f.result, nil
}

func (f *fakeStoreInstaller) InstallScript(_ context.Context, in runtimecontext.StoreInstallScriptInput) (runtimecontext.StoreInstallResult, error) {
	f.scriptIn = &in
	if f.err != nil {
		return runtimecontext.StoreInstallResult{}, f.err
	}
	return f.result, nil
}

func TestPageStoreUsesUserFactsAndInput(t *testing.T) {
	b := &fakeStoreBrowser{result: runtimecontext.StorePageResult{
		Total:    1,
		Page:     2,
		PageSize: 5,
		Stores: []runtimecontext.StoreItem{{
			StoreID:   11,
			StoreType: "workflow",
			Name:      "qc",
			StorePath: "/data/store/abc",
			Installed: true,
		}},
	}}

	out, err := PageStore(b).Execute(storeTestFacts(9, 7), json.RawMessage(`{"store_type":"workflow","keywords":"qc","page":2,"page_size":5,"sort_by":"name","sort_order":"ASC"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(PageStoreOutput)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if b.gotUserID != 9 {
		t.Fatalf("user id = %d, want 9", b.gotUserID)
	}
	if b.gotIn.StoreType != "workflow" || b.gotIn.Keywords != "qc" || b.gotIn.Page != 2 || b.gotIn.PageSize != 5 ||
		b.gotIn.SortBy != "name" || b.gotIn.SortOrder != "ASC" {
		t.Fatalf("input = %+v", b.gotIn)
	}
	if res.Total != 1 || len(res.Stores) != 1 || res.Stores[0].StoreID != 11 || !res.Stores[0].Installed {
		t.Fatalf("output = %+v", res)
	}
}

func TestPageStoreRequiresUser(t *testing.T) {
	b := &fakeStoreBrowser{}
	if _, err := PageStore(b).Execute(context.Background(), json.RawMessage(`{"store_type":"script"}`)); err == nil {
		t.Fatalf("expected error when user facts are missing")
	}
}

func TestPageStoreReturnsQueryErrorInOutput(t *testing.T) {
	b := &fakeStoreBrowser{err: errors.New("store_type must be workflow or script")}
	out, err := PageStore(b).Execute(storeTestFacts(9, 7), json.RawMessage(`{"store_type":"bogus"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res := out.(PageStoreOutput); res.Error == "" {
		t.Fatalf("expected error in output")
	}
}

func TestPublishWorkflowPassesProjectAndParsesReference(t *testing.T) {
	p := &fakeStorePublisher{result: runtimecontext.StorePublishResult{
		StoreID:     5,
		StoreType:   "workflow",
		StorePath:   "/data/store/abc",
		Pushed:      true,
		ComponentID: "wf-uuid",
	}}

	out, err := PublishWorkflowToStore(p).Execute(storeTestFacts(9, 7), json.RawMessage(`{"workflow_id":"@workflow:42","message":"  first release  "}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(PublishWorkflowOutput)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if p.workflowIn == nil {
		t.Fatalf("publisher was not called")
	}
	if p.workflowIn.ProjectID != 7 || p.workflowIn.WorkflowID != 42 || p.workflowIn.Message != "first release" {
		t.Fatalf("publish input = %+v", *p.workflowIn)
	}
	if res.StoreID != 5 || !res.Pushed || res.ComponentID != "wf-uuid" {
		t.Fatalf("output = %+v", res)
	}
}

func TestPublishWorkflowRejectsInvalidID(t *testing.T) {
	p := &fakeStorePublisher{}
	if _, err := PublishWorkflowToStore(p).Execute(storeTestFacts(9, 7), json.RawMessage(`{"workflow_id":"abc"}`)); err == nil {
		t.Fatalf("expected invalid workflow_id error")
	}
	if p.workflowIn != nil {
		t.Fatalf("publisher should not be called for invalid input")
	}
}

func TestPublishScriptParsesScriptPrefix(t *testing.T) {
	p := &fakeStorePublisher{result: runtimecontext.StorePublishResult{StoreID: 6, StoreType: "script", UpToDate: true}}

	out, err := PublishScriptToStore(p).Execute(storeTestFacts(9, 7), json.RawMessage(`{"script_id":"@script:77"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res := out.(PublishScriptOutput); res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if p.scriptIn == nil || p.scriptIn.ProjectID != 7 || p.scriptIn.ScriptID != 77 {
		t.Fatalf("publish input = %+v", p.scriptIn)
	}
}

func TestPublishScriptReturnsPublisherErrorInOutput(t *testing.T) {
	p := &fakeStorePublisher{err: errors.New("script 77 not found")}
	out, err := PublishScriptToStore(p).Execute(storeTestFacts(9, 7), json.RawMessage(`{"script_id":"77"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res := out.(PublishScriptOutput); res.Error == "" {
		t.Fatalf("expected error in output")
	}
}

func TestInstallWorkflowParsesStoreReference(t *testing.T) {
	i := &fakeStoreInstaller{result: runtimecontext.StoreInstallResult{
		StoreID:     5,
		StorePath:   "/data/store/abc",
		TargetDir:   "/data/7/pipeline/workflow/wf-uuid",
		ComponentID: "wf-uuid",
		InstalledID: 99,
	}}

	out, err := InstallWorkflowFromStore(i).Execute(storeTestFacts(9, 7), json.RawMessage(`{"store_id":"@store:5"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(InstallWorkflowOutput)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if i.workflowIn == nil || i.workflowIn.ProjectID != 7 || i.workflowIn.StoreID != 5 {
		t.Fatalf("install input = %+v", i.workflowIn)
	}
	if res.InstalledID != 99 || res.ComponentID != "wf-uuid" {
		t.Fatalf("output = %+v", res)
	}
}

func TestInstallScriptCreateModeFlag(t *testing.T) {
	i := &fakeStoreInstaller{result: runtimecontext.StoreInstallResult{StoreID: 6, InstalledID: 100}}

	out, err := InstallScriptFromStore(i).Execute(storeTestFacts(9, 7), json.RawMessage(`{"store_id":"6","create":true}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res := out.(InstallScriptOutput); res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if i.scriptIn == nil || i.scriptIn.ProjectID != 7 || i.scriptIn.StoreID != 6 || !i.scriptIn.CreateMode {
		t.Fatalf("install input = %+v", i.scriptIn)
	}
}

func TestInstallWorkflowRequiresProject(t *testing.T) {
	i := &fakeStoreInstaller{}
	if _, err := InstallWorkflowFromStore(i).Execute(context.Background(), json.RawMessage(`{"store_id":"5"}`)); err == nil {
		t.Fatalf("expected error when active project is missing")
	}
}
