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

// fakeAppSessionCreator 记录工具调用并回放固定结果。
type fakeAppSessionCreator struct {
	webInput      *runtimecontext.WebAppSessionCreateInput
	terminalInput *runtimecontext.TerminalAppSessionCreateInput
	err           error
}

func (f *fakeAppSessionCreator) CreateWebAppSession(_ context.Context, in runtimecontext.WebAppSessionCreateInput) (runtimecontext.AppSessionCreateResult, error) {
	f.webInput = &in
	if f.err != nil {
		return runtimecontext.AppSessionCreateResult{}, f.err
	}
	return runtimecontext.AppSessionCreateResult{
		SessionID:           101,
		ContainerTemplateID: in.ContainerTemplateID,
		SessionKind:         "web",
		Name:                in.Name,
		Status:              "CREATE_PENDING",
	}, nil
}

func (f *fakeAppSessionCreator) CreateTerminalAppSession(_ context.Context, in runtimecontext.TerminalAppSessionCreateInput) (runtimecontext.AppSessionCreateResult, error) {
	f.terminalInput = &in
	if f.err != nil {
		return runtimecontext.AppSessionCreateResult{}, f.err
	}
	return runtimecontext.AppSessionCreateResult{
		SessionID:           202,
		ContainerTemplateID: in.ContainerTemplateID,
		SessionKind:         "terminal",
		Status:              "RUNNING",
		Persistent:          true,
	}, nil
}

func TestCreateWebAppSessionPassesContextFacts(t *testing.T) {
	creator := &fakeAppSessionCreator{}
	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{UserID: 7, ProjectID: 9})

	out, err := CreateWebAppSession(creator).Execute(ctx, json.RawMessage(`{
		"container_template_id": "@container:123",
		"name": "  my app  "
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(CreateAppSessionOutput)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if creator.webInput == nil {
		t.Fatal("creator was not called")
	}
	if creator.webInput.UserID != 7 || creator.webInput.ProjectID != 9 || creator.webInput.ContainerTemplateID != 123 {
		t.Fatalf("unexpected input: %+v", creator.webInput)
	}
	if creator.webInput.Name != "my app" {
		t.Fatalf("name = %q", creator.webInput.Name)
	}
	if res.SessionID != 101 || res.SessionKind != "web" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestCreateWebAppSessionRequiresProject(t *testing.T) {
	creator := &fakeAppSessionCreator{}
	if _, err := CreateWebAppSession(creator).Execute(context.Background(), json.RawMessage(`{"container_template_id":"1"}`)); err == nil {
		t.Fatal("expected error for missing project")
	}
	if creator.webInput != nil {
		t.Fatal("creator should not be called")
	}
}

func TestCreateWebAppSessionReturnsCreatorError(t *testing.T) {
	creator := &fakeAppSessionCreator{err: errors.New("boom")}
	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{UserID: 7, ProjectID: 9})

	out, err := CreateWebAppSession(creator).Execute(ctx, json.RawMessage(`{"container_template_id":"1"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(CreateAppSessionOutput)
	if res.Error != "boom" {
		t.Fatalf("error = %q", res.Error)
	}
	if res.ContainerTemplateID != 1 || res.SessionKind != "web" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestCreateTerminalAppSessionPassesDimensions(t *testing.T) {
	creator := &fakeAppSessionCreator{}
	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{UserID: 7, ProjectID: 9})

	out, err := CreateTerminalAppSession(creator).Execute(ctx, json.RawMessage(`{
		"container_template_id": "456",
		"rows": 30,
		"cols": 120
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(CreateAppSessionOutput)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if creator.terminalInput == nil {
		t.Fatal("creator was not called")
	}
	if creator.terminalInput.ContainerTemplateID != 456 || creator.terminalInput.Rows != 30 || creator.terminalInput.Cols != 120 {
		t.Fatalf("unexpected input: %+v", creator.terminalInput)
	}
	if res.SessionID != 202 || res.SessionKind != "terminal" || !res.Persistent {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestCreateTerminalAppSessionRejectsNegativeRows(t *testing.T) {
	creator := &fakeAppSessionCreator{}
	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{UserID: 7, ProjectID: 9})

	if _, err := CreateTerminalAppSession(creator).Execute(ctx, json.RawMessage(`{
		"container_template_id": "456",
		"rows": -1
	}`)); err == nil {
		t.Fatal("expected error for negative rows")
	}
	if creator.terminalInput != nil {
		t.Fatal("creator should not be called")
	}
}
