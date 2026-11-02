package manager

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/config"
	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/types"
	"gorm.io/datatypes"
)

// syncDockerMockRuntime adds the synchronous start hook used by probe containers.
type syncDockerMockRuntime struct {
	*dockerMockRuntime
	exitCode int
	waitErr  error
	started  bool
	deleted  []string
}

func (d *syncDockerMockRuntime) StartAndWait(_ context.Context, runtimeID string) (int, error) {
	d.started = true
	return d.exitCode, d.waitErr
}

func (d *syncDockerMockRuntime) Delete(ctx context.Context, runtimeID string) error {
	d.deleted = append(d.deleted, runtimeID)
	return d.dockerMockRuntime.Delete(ctx, runtimeID)
}

func TestContainerProbeRunner_UsesTemplateEnvAndPackageVars(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()

	repo := newMockContainerRepo()
	repo.projects[7] = &types.Project{ID: 7}
	repo.templates[10] = &types.ContainerTemplate{
		ID:         10,
		ImageName:  "example/probe:latest",
		PullPolicy: types.PullPolicyIfNotPresent,
		Env: datatypes.JSON([]byte(
			`{"R_LIBS_USER":"$R_PACKAGE_DIR","PROJECT_MEMO":"$PROJECT_DIR","KEEP":"template"}`,
		)),
		Mounts: datatypes.JSON([]byte(`[{"source":"/tmp/probe-src","target":"/data"}]`)),
	}

	rt := &syncDockerMockRuntime{
		dockerMockRuntime: &dockerMockRuntime{runtimeID: "docker-probe-1", logsResult: "R 4.3.0\n"},
	}
	reg := containerruntime.NewRegistry()
	reg.Register("docker", rt)

	projectRepo := &mockProjectRepo{projects: repo.projects}
	cfg := &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}}
	mgr := NewContainerManager(repo, nil, projectRepo, nil, reg, nil, NewDefaultContainerRuntimeResolver(), cfg)
	worker := NewContainerCreateWorker(repo, projectRepo, nil, nil, reg, NewDefaultContainerRuntimeResolver(), mgr, cfg)

	result, err := NewContainerProbeRunner(worker).ProbeContainerEnv(ctx, runtimecontext.ContainerEnvProbeInput{
		ProjectID:           7,
		ContainerTemplateID: 10,
		Command:             "R --version",
	})
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if !rt.started {
		t.Fatalf("expected synchronous start to be called")
	}
	if !result.Success || result.ExitCode != 0 {
		t.Fatalf("unexpected result: success=%v exit=%d", result.Success, result.ExitCode)
	}
	if result.Log != "R 4.3.0\n" {
		t.Fatalf("unexpected log: %q", result.Log)
	}
	if len(rt.deleted) != 1 || rt.deleted[0] != "docker-probe-1" {
		t.Fatalf("expected container cleanup, got %v", rt.deleted)
	}

	spec := rt.lastSpec
	if spec == nil {
		t.Fatalf("expected a container spec")
	}
	projectDir := filepath.Join(baseDir, "data", "7")
	rPackageDir := filepath.Join(projectDir, "package", "R-10")
	if got := spec.Env["R_LIBS_USER"]; got != rPackageDir {
		t.Fatalf("expected R_LIBS_USER to resolve to %q, got %q", rPackageDir, got)
	}
	if got := spec.Env["PROJECT_MEMO"]; got != projectDir {
		t.Fatalf("expected PROJECT_MEMO to resolve to %q, got %q", projectDir, got)
	}
	if got := spec.Env["KEEP"]; got != "template" {
		t.Fatalf("expected template env to be preserved, got %q", got)
	}
	if got := spec.Env["R_PACKAGE_DIR"]; got != rPackageDir {
		t.Fatalf("expected R_PACKAGE_DIR to be filled, got %q", got)
	}
	if got := spec.Env["PROJECT_DIR"]; got != projectDir {
		t.Fatalf("expected PROJECT_DIR to be filled, got %q", got)
	}
	if spec.WorkDir != projectDir {
		t.Fatalf("expected default workdir %q, got %q", projectDir, spec.WorkDir)
	}
	if len(spec.Command) != 1 || spec.Command[0] != "R --version" {
		t.Fatalf("unexpected command: %v", spec.Command)
	}
	if len(spec.Volumes) == 0 || spec.Volumes[0].Target != "/data" {
		t.Fatalf("expected template mount in probe spec, got %+v", spec.Volumes)
	}
}
