package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
)

// fakeContainerTemplateSpecWriter 是最小的 ContainerTemplateSpecWriter 实现。
type fakeContainerTemplateSpecWriter struct {
	created *runtimecontext.ContainerTemplateSpecCreateInput
	updated *runtimecontext.ContainerTemplateSpecUpdateInput
	record  runtimecontext.ContainerTemplateSpecRecord
	err     error
}

func (f *fakeContainerTemplateSpecWriter) CreateContainerTemplateSpec(_ context.Context, in runtimecontext.ContainerTemplateSpecCreateInput) (runtimecontext.ContainerTemplateSpecRecord, error) {
	f.created = &in
	if f.err != nil {
		return runtimecontext.ContainerTemplateSpecRecord{}, f.err
	}
	record := f.record
	record.SpecID = 1001
	record.Name = in.Name
	return record, nil
}

func (f *fakeContainerTemplateSpecWriter) UpdateContainerTemplateSpec(_ context.Context, in runtimecontext.ContainerTemplateSpecUpdateInput) (runtimecontext.ContainerTemplateSpecRecord, error) {
	f.updated = &in
	if f.err != nil {
		return runtimecontext.ContainerTemplateSpecRecord{}, f.err
	}
	record := f.record
	record.SpecID = in.SpecID
	return record, nil
}

// fakeContainerTemplateDefinitionWriter 是最小的 ContainerTemplateDefinitionWriter 实现。
type fakeContainerTemplateDefinitionWriter struct {
	created *runtimecontext.ContainerTemplateDefinitionCreateInput
	updated *runtimecontext.ContainerTemplateDefinitionUpdateInput
	record  runtimecontext.ContainerTemplateDefinitionRecord
	err     error
}

func (f *fakeContainerTemplateDefinitionWriter) CreateContainerTemplateDefinition(_ context.Context, in runtimecontext.ContainerTemplateDefinitionCreateInput) (runtimecontext.ContainerTemplateDefinitionRecord, error) {
	f.created = &in
	if f.err != nil {
		return runtimecontext.ContainerTemplateDefinitionRecord{}, f.err
	}
	record := f.record
	record.DefinitionID = 2001
	record.SpecID = in.SpecID
	record.ImageName = in.ImageName
	record.Name = in.Name
	return record, nil
}

func (f *fakeContainerTemplateDefinitionWriter) UpdateContainerTemplateDefinition(_ context.Context, in runtimecontext.ContainerTemplateDefinitionUpdateInput) (runtimecontext.ContainerTemplateDefinitionRecord, error) {
	f.updated = &in
	if f.err != nil {
		return runtimecontext.ContainerTemplateDefinitionRecord{}, f.err
	}
	record := f.record
	record.DefinitionID = in.DefinitionID
	return record, nil
}

func TestCreateContainerTemplateSpecDefaultsPortAndTrims(t *testing.T) {
	writer := &fakeContainerTemplateSpecWriter{}

	out, err := CreateContainerTemplateSpec(writer).Execute(context.Background(), json.RawMessage(`{
		"name": "  rstudio-base  ",
		"command": "  /init  ",
		"env": {"PUID": "1000"}
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(ContainerTemplateSpecWriteOutput)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if writer.created == nil {
		t.Fatal("writer was not called")
	}
	if writer.created.Name != "rstudio-base" || writer.created.Command != "/init" {
		t.Fatalf("unexpected create input: %+v", writer.created)
	}
	if writer.created.Port != DefaultContainerTemplatePort {
		t.Fatalf("port = %d, want %d", writer.created.Port, DefaultContainerTemplatePort)
	}
	if res.SpecID != 1001 {
		t.Fatalf("spec_id = %d", res.SpecID)
	}
}

func TestCreateContainerTemplateSpecRequiresName(t *testing.T) {
	writer := &fakeContainerTemplateSpecWriter{}
	if _, err := CreateContainerTemplateSpec(writer).Execute(context.Background(), json.RawMessage(`{"description":"x"}`)); err == nil {
		t.Fatal("expected error for missing name")
	}
	if writer.created != nil {
		t.Fatal("writer should not be called")
	}
}

func TestUpdateContainerTemplateSpecRequiresField(t *testing.T) {
	writer := &fakeContainerTemplateSpecWriter{}
	if _, err := UpdateContainerTemplateSpec(writer).Execute(context.Background(), json.RawMessage(`{"spec_id":"11"}`)); err == nil {
		t.Fatal("expected error when no field is provided")
	}
}

func TestUpdateContainerTemplateSpecPassesPointers(t *testing.T) {
	writer := &fakeContainerTemplateSpecWriter{}

	out, err := UpdateContainerTemplateSpec(writer).Execute(context.Background(), json.RawMessage(`{
		"spec_id": "@container_spec:11",
		"cpu": 2.5,
		"name": "  renamed  ",
		"change_uid": true
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(ContainerTemplateSpecWriteOutput)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if writer.updated == nil || writer.updated.SpecID != 11 {
		t.Fatalf("unexpected update input: %+v", writer.updated)
	}
	if writer.updated.CPU == nil || *writer.updated.CPU != 2.5 {
		t.Fatalf("cpu = %v", writer.updated.CPU)
	}
	if writer.updated.Name == nil || *writer.updated.Name != "renamed" {
		t.Fatalf("name = %v", writer.updated.Name)
	}
	if writer.updated.ChangeUID == nil || !*writer.updated.ChangeUID {
		t.Fatalf("change_uid = %v", writer.updated.ChangeUID)
	}
	if writer.updated.Port != nil || writer.updated.Memory != nil || writer.updated.Env != nil {
		t.Fatalf("unset fields should stay nil: %+v", writer.updated)
	}
}

func TestUpdateContainerTemplateSpecReturnsWriterError(t *testing.T) {
	writer := &fakeContainerTemplateSpecWriter{err: errors.New("boom")}

	out, err := UpdateContainerTemplateSpec(writer).Execute(context.Background(), json.RawMessage(`{"spec_id":"11","port":9000}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(ContainerTemplateSpecWriteOutput)
	if res.Error != "boom" {
		t.Fatalf("error = %q", res.Error)
	}
	if res.SpecID != 11 {
		t.Fatalf("spec_id = %d", res.SpecID)
	}
}

func TestCreateContainerTemplateDefinitionRequiresImage(t *testing.T) {
	writer := &fakeContainerTemplateDefinitionWriter{}
	if _, err := CreateContainerTemplateDefinition(writer).Execute(context.Background(), json.RawMessage(`{"spec_id":"11"}`)); err == nil {
		t.Fatal("expected error for missing image_name")
	}
}

func TestCreateContainerTemplateDefinitionPassesFields(t *testing.T) {
	writer := &fakeContainerTemplateDefinitionWriter{}

	out, err := CreateContainerTemplateDefinition(writer).Execute(context.Background(), json.RawMessage(`{
		"spec_id": "11",
		"image_name": "  docker.io/rocker/rstudio:4.4  ",
		"pull_policy": "Always",
		"name": "RStudio",
		"description": "RStudio IDE"
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(ContainerTemplateDefinitionWriteOutput)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if writer.created == nil {
		t.Fatal("writer was not called")
	}
	if writer.created.SpecID != 11 || writer.created.ImageName != "docker.io/rocker/rstudio:4.4" {
		t.Fatalf("unexpected create input: %+v", writer.created)
	}
	if writer.created.PullPolicy != "Always" || writer.created.Name != "RStudio" || writer.created.Description != "RStudio IDE" {
		t.Fatalf("unexpected create input: %+v", writer.created)
	}
	if res.DefinitionID != 2001 {
		t.Fatalf("definition_id = %d", res.DefinitionID)
	}
}

func TestUpdateContainerTemplateDefinitionParsesReferenceAndRequiresField(t *testing.T) {
	writer := &fakeContainerTemplateDefinitionWriter{}

	if _, err := UpdateContainerTemplateDefinition(writer).Execute(context.Background(), json.RawMessage(`{"definition_id":"@container:21"}`)); err == nil {
		t.Fatal("expected error when no field is provided")
	}

	out, err := UpdateContainerTemplateDefinition(writer).Execute(context.Background(), json.RawMessage(`{
		"definition_id": "@container:21",
		"image_name": "  busybox:latest  "
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(ContainerTemplateDefinitionWriteOutput)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if writer.updated == nil || writer.updated.DefinitionID != 21 {
		t.Fatalf("unexpected update input: %+v", writer.updated)
	}
	if writer.updated.ImageName == nil || *writer.updated.ImageName != "busybox:latest" {
		t.Fatalf("image_name = %v", writer.updated.ImageName)
	}
	if writer.updated.SpecID != nil || writer.updated.PullPolicy != nil || writer.updated.Name != nil {
		t.Fatalf("unset fields should stay nil: %+v", writer.updated)
	}
}
