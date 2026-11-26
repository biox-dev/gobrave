package container

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// fakeTemplateContainerService 是 projectResourceBrowser 容器模板写能力测试用的最小实现。
// 内嵌 interfaces.ContainerService 后只需实现被用到的方法。
type fakeTemplateContainerService struct {
	interfaces.ContainerService
	specs  map[int64]*types.ContainerTemplateSpec
	defs   map[int64]*types.ContainerTemplateDefinition
	nextID int64
}

func newFakeTemplateContainerService() *fakeTemplateContainerService {
	return &fakeTemplateContainerService{
		specs:  map[int64]*types.ContainerTemplateSpec{},
		defs:   map[int64]*types.ContainerTemplateDefinition{},
		nextID: 100,
	}
}

func (f *fakeTemplateContainerService) CreateContainerTemplateSpec(_ context.Context, item *types.ContainerTemplateSpec) error {
	f.nextID++
	item.ID = f.nextID
	cloned := *item
	f.specs[item.ID] = &cloned
	return nil
}

func (f *fakeTemplateContainerService) GetContainerTemplateSpecByID(_ context.Context, id int64) (*types.ContainerTemplateSpec, error) {
	item, ok := f.specs[id]
	if !ok {
		return nil, errFakeNotFound
	}
	cloned := *item
	return &cloned, nil
}

func (f *fakeTemplateContainerService) UpdateContainerTemplateSpec(_ context.Context, item *types.ContainerTemplateSpec) error {
	cloned := *item
	f.specs[item.ID] = &cloned
	return nil
}

func (f *fakeTemplateContainerService) CreateContainerTemplateDefinition(_ context.Context, item *types.ContainerTemplateDefinition) error {
	f.nextID++
	item.ID = f.nextID
	cloned := *item
	f.defs[item.ID] = &cloned
	return nil
}

func (f *fakeTemplateContainerService) GetContainerTemplateDefinitionByID(_ context.Context, id int64) (*types.ContainerTemplateDefinition, error) {
	item, ok := f.defs[id]
	if !ok {
		return nil, errFakeNotFound
	}
	cloned := *item
	return &cloned, nil
}

func (f *fakeTemplateContainerService) UpdateContainerTemplateDefinition(_ context.Context, item *types.ContainerTemplateDefinition) error {
	cloned := *item
	f.defs[item.ID] = &cloned
	return nil
}

var errFakeNotFound = &fakeNotFoundError{}

type fakeNotFoundError struct{}

func (e *fakeNotFoundError) Error() string { return "not found" }

func TestProjectResourceBrowserCreateContainerTemplateSpec(t *testing.T) {
	svc := newFakeTemplateContainerService()
	b := &projectResourceBrowser{containerSvc: svc}

	record, err := b.CreateContainerTemplateSpec(context.Background(), runtimecontext.ContainerTemplateSpecCreateInput{
		Name:    "rstudio-base",
		Command: "/init",
		Port:    8787,
		Env:     map[string]any{"PUID": "1000"},
	})
	if err != nil {
		t.Fatalf("create spec: %v", err)
	}
	if record.SpecID != 101 {
		t.Fatalf("spec_id = %d", record.SpecID)
	}
	stored := svc.specs[101]
	if stored == nil {
		t.Fatal("spec was not stored")
	}
	if stored.Name != "rstudio-base" || stored.Command != "/init" || stored.Port != 8787 {
		t.Fatalf("stored = %+v", stored)
	}
	var env map[string]any
	if err := json.Unmarshal(stored.Env, &env); err != nil {
		t.Fatalf("env is not valid json: %v", err)
	}
	if env["PUID"] != "1000" {
		t.Fatalf("env = %v", env)
	}
}

func TestProjectResourceBrowserUpdateContainerTemplateSpecPreservesUnsetFields(t *testing.T) {
	svc := newFakeTemplateContainerService()
	svc.specs[7] = &types.ContainerTemplateSpec{
		ID:      7,
		Name:    "old",
		Command: "run",
		Port:    8787,
		CPU:     1,
	}
	b := &projectResourceBrowser{containerSvc: svc}

	cpu := 4.0
	record, err := b.UpdateContainerTemplateSpec(context.Background(), runtimecontext.ContainerTemplateSpecUpdateInput{
		SpecID: 7,
		CPU:    &cpu,
	})
	if err != nil {
		t.Fatalf("update spec: %v", err)
	}
	if record.SpecID != 7 || record.CPU != 4 {
		t.Fatalf("record = %+v", record)
	}
	stored := svc.specs[7]
	if stored.Name != "old" || stored.Command != "run" || stored.Port != 8787 {
		t.Fatalf("unset fields were overwritten: %+v", stored)
	}
	if stored.CPU != 4 {
		t.Fatalf("cpu = %v", stored.CPU)
	}
}

func TestProjectResourceBrowserCreateContainerTemplateDefinition(t *testing.T) {
	svc := newFakeTemplateContainerService()
	svc.specs[9] = &types.ContainerTemplateSpec{ID: 9, Name: "base"}
	b := &projectResourceBrowser{containerSvc: svc}

	record, err := b.CreateContainerTemplateDefinition(context.Background(), runtimecontext.ContainerTemplateDefinitionCreateInput{
		SpecID:      9,
		ImageName:   "docker.io/rocker/rstudio:4.4",
		PullPolicy:  "Always",
		Name:        "RStudio",
		Description: "RStudio IDE",
	})
	if err != nil {
		t.Fatalf("create definition: %v", err)
	}
	if record.DefinitionID != 101 || record.SpecID != 9 {
		t.Fatalf("record = %+v", record)
	}
	stored := svc.defs[101]
	if stored.ImageName != "docker.io/rocker/rstudio:4.4" || stored.PullPolicy != "Always" || stored.Name != "RStudio" {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestProjectResourceBrowserUpdateContainerTemplateDefinitionPreservesUnsetFields(t *testing.T) {
	svc := newFakeTemplateContainerService()
	svc.defs[5] = &types.ContainerTemplateDefinition{
		ID:          5,
		SpecID:      9,
		ImageName:   "busybox",
		PullPolicy:  "IfNotPresent",
		Name:        "keep",
		Description: "keep me",
	}
	b := &projectResourceBrowser{containerSvc: svc}

	newSpec := int64(10)
	record, err := b.UpdateContainerTemplateDefinition(context.Background(), runtimecontext.ContainerTemplateDefinitionUpdateInput{
		DefinitionID: 5,
		SpecID:       &newSpec,
	})
	if err != nil {
		t.Fatalf("update definition: %v", err)
	}
	if record.DefinitionID != 5 || record.SpecID != 10 {
		t.Fatalf("record = %+v", record)
	}
	stored := svc.defs[5]
	if stored.ImageName != "busybox" || stored.PullPolicy != "IfNotPresent" || stored.Name != "keep" || stored.Description != "keep me" {
		t.Fatalf("unset fields were overwritten: %+v", stored)
	}
}
