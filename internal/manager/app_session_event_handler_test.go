package manager

import (
	"context"
	"testing"

	"github.com/gobravedev/gobrave/internal/types"
)

func TestAppSessionEventHandler_Handle_UpdatesToRunningOnContainerStarted(t *testing.T) {
	repo := newMockContainerRepo()
	ctx := context.TODO()
	session := &types.AppSession{ID: 1001, UserID: "u1", Status: "CREATING"}
	if err := repo.CreateAppSession(ctx, session); err != nil {
		t.Fatalf("seed app session failed: %v", err)
	}
	inst := &types.ContainerInstance{ID: 2001, OwnerType: types.ContainerOwnerAppSession, OwnerID: session.ID}
	if err := repo.CreateContainerInstance(ctx, inst); err != nil {
		t.Fatalf("seed container instance failed: %v", err)
	}

	h := NewAppSessionEventHandler(repo, nil)
	h.Handle(types.ContainerEvent{ContainerInstanceID: inst.ID, Event: "ContainerStarted"})

	stored, err := repo.GetAppSessionByID(ctx, session.ID)
	if err != nil {
		t.Fatalf("load app session failed: %v", err)
	}
	if stored.Status != "RUNNING" {
		t.Fatalf("expected RUNNING, got %s", stored.Status)
	}
	if stored.StartedAt == nil {
		t.Fatalf("expected StartedAt to be set")
	}
	if stored.StoppedAt != nil {
		t.Fatalf("expected StoppedAt to be nil")
	}
}

func TestAppSessionEventHandler_Handle_UpdatesToStoppedOnContainerStopped(t *testing.T) {
	repo := newMockContainerRepo()
	ctx := context.TODO()
	session := &types.AppSession{ID: 1002, UserID: "u1", Status: "RUNNING"}
	if err := repo.CreateAppSession(ctx, session); err != nil {
		t.Fatalf("seed app session failed: %v", err)
	}
	inst := &types.ContainerInstance{ID: 2002, OwnerType: types.ContainerOwnerAppSession, OwnerID: session.ID}
	if err := repo.CreateContainerInstance(ctx, inst); err != nil {
		t.Fatalf("seed container instance failed: %v", err)
	}

	h := NewAppSessionEventHandler(repo, nil)
	h.Handle(types.ContainerEvent{ContainerInstanceID: inst.ID, Event: "ContainerStopped"})

	stored, err := repo.GetAppSessionByID(ctx, session.ID)
	if err != nil {
		t.Fatalf("load app session failed: %v", err)
	}
	if stored.Status != "STOPPED" {
		t.Fatalf("expected STOPPED, got %s", stored.Status)
	}
	if stored.StoppedAt == nil {
		t.Fatalf("expected StoppedAt to be set")
	}
}

func TestAppSessionEventHandler_Handle_UpdatesToFailedOnContainerFailure(t *testing.T) {
	repo := newMockContainerRepo()
	ctx := context.TODO()
	session := &types.AppSession{ID: 1003, UserID: "u1", Status: "CREATING"}
	if err := repo.CreateAppSession(ctx, session); err != nil {
		t.Fatalf("seed app session failed: %v", err)
	}
	inst := &types.ContainerInstance{ID: 2003, OwnerType: types.ContainerOwnerAppSession, OwnerID: session.ID}
	if err := repo.CreateContainerInstance(ctx, inst); err != nil {
		t.Fatalf("seed container instance failed: %v", err)
	}

	h := NewAppSessionEventHandler(repo, nil)
	h.Handle(types.ContainerEvent{ContainerInstanceID: inst.ID, Event: "ContainerStartFailed"})

	stored, err := repo.GetAppSessionByID(ctx, session.ID)
	if err != nil {
		t.Fatalf("load app session failed: %v", err)
	}
	if stored.Status != "FAILED" {
		t.Fatalf("expected FAILED, got %s", stored.Status)
	}
	if stored.StoppedAt == nil {
		t.Fatalf("expected StoppedAt to be set")
	}
}

func TestAppSessionEventHandler_Handle_IgnoresNonAppSessionOwner(t *testing.T) {
	repo := newMockContainerRepo()
	ctx := context.TODO()
	session := &types.AppSession{ID: 1004, UserID: "u1", Status: "CREATING"}
	if err := repo.CreateAppSession(ctx, session); err != nil {
		t.Fatalf("seed app session failed: %v", err)
	}
	inst := &types.ContainerInstance{ID: 2004, OwnerType: types.ContainerOwnerDagNode, OwnerID: session.ID}
	if err := repo.CreateContainerInstance(ctx, inst); err != nil {
		t.Fatalf("seed container instance failed: %v", err)
	}

	h := NewAppSessionEventHandler(repo, nil)
	h.Handle(types.ContainerEvent{ContainerInstanceID: inst.ID, Event: "ContainerStarted"})

	stored, err := repo.GetAppSessionByID(ctx, session.ID)
	if err != nil {
		t.Fatalf("load app session failed: %v", err)
	}
	if stored.Status != "CREATING" {
		t.Fatalf("expected status unchanged CREATING, got %s", stored.Status)
	}
}
