package manager

import (
	"context"
	"errors"
	"fmt"
	"testing"

	containerruntime "github.com/gobravedev/gobrave/internal/container_runtime"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"gorm.io/datatypes"
)

type mockContainerRepo struct {
	images    map[int64]*types.ContainerImage
	templates map[int64]*types.ContainerTemplate
	sessions  map[int64]*types.AppSession
	projects  map[string]*types.Project
	instances map[int64]*types.ContainerInstance
	events    []*types.ContainerEvent
	outbox    []*types.OutboxEvent
	nextID    int64
}

func newMockContainerRepo() *mockContainerRepo {
	return &mockContainerRepo{
		images:    map[int64]*types.ContainerImage{},
		templates: map[int64]*types.ContainerTemplate{},
		sessions:  map[int64]*types.AppSession{},
		projects:  map[string]*types.Project{},
		instances: map[int64]*types.ContainerInstance{},
		events:    make([]*types.ContainerEvent, 0),
		outbox:    make([]*types.OutboxEvent, 0),
		nextID:    100,
	}
}

func (m *mockContainerRepo) next() int64 {
	m.nextID++
	return m.nextID
}

func (m *mockContainerRepo) WithTransaction(ctx context.Context, fn func(interfaces.ContainerRepository) error) error {
	return fn(m)
}

func (m *mockContainerRepo) CreateContainerImage(ctx context.Context, item *types.ContainerImage) error {
	if item.ID == 0 {
		item.ID = m.next()
	}
	m.images[item.ID] = item
	return nil
}

func (m *mockContainerRepo) GetContainerImageByID(ctx context.Context, id int64) (*types.ContainerImage, error) {
	item, ok := m.images[id]
	if !ok {
		return nil, errors.New("container image not found")
	}
	return item, nil
}

func (m *mockContainerRepo) UpdateContainerImage(ctx context.Context, item *types.ContainerImage) error {
	m.images[item.ID] = item
	return nil
}

func (m *mockContainerRepo) DeleteContainerImage(ctx context.Context, id int64) error {
	delete(m.images, id)
	return nil
}

func (m *mockContainerRepo) ListContainerImage(ctx context.Context) ([]*types.ContainerImage, error) {
	items := make([]*types.ContainerImage, 0, len(m.images))
	for _, v := range m.images {
		items = append(items, v)
	}
	return items, nil
}

func (m *mockContainerRepo) PageContainerImage(ctx context.Context, pagination *types.Pagination) ([]*types.ContainerImage, int64, error) {
	items, err := m.ListContainerImage(ctx)
	if err != nil {
		return nil, 0, err
	}
	return items, int64(len(items)), nil
}

func (m *mockContainerRepo) CreateContainerTemplate(ctx context.Context, item *types.ContainerTemplate) error {
	if item.ID == 0 {
		item.ID = m.next()
	}
	m.templates[item.ID] = item
	return nil
}

func (m *mockContainerRepo) GetContainerTemplateByID(ctx context.Context, id int64) (*types.ContainerTemplate, error) {
	item, ok := m.templates[id]
	if !ok {
		return nil, errors.New("container template not found")
	}
	return item, nil
}

func (m *mockContainerRepo) UpdateContainerTemplate(ctx context.Context, item *types.ContainerTemplate) error {
	m.templates[item.ID] = item
	return nil
}

func (m *mockContainerRepo) DeleteContainerTemplate(ctx context.Context, id int64) error {
	delete(m.templates, id)
	return nil
}

func (m *mockContainerRepo) ListContainerTemplate(ctx context.Context) ([]*types.ContainerTemplate, error) {
	items := make([]*types.ContainerTemplate, 0, len(m.templates))
	for _, v := range m.templates {
		items = append(items, v)
	}
	return items, nil
}

func (m *mockContainerRepo) PageContainerTemplate(ctx context.Context, pagination *types.Pagination) ([]*types.ContainerTemplate, int64, error) {
	items, err := m.ListContainerTemplate(ctx)
	if err != nil {
		return nil, 0, err
	}
	return items, int64(len(items)), nil
}

func (m *mockContainerRepo) CreateAppSession(ctx context.Context, item *types.AppSession) error {
	if item.ID == 0 {
		item.ID = m.next()
	}
	m.sessions[item.ID] = item
	return nil
}

func (m *mockContainerRepo) GetAppSessionByID(ctx context.Context, id int64) (*types.AppSession, error) {
	item, ok := m.sessions[id]
	if !ok {
		return nil, errors.New("app session not found")
	}
	return item, nil
}

func (m *mockContainerRepo) GetProjectByProjectID(ctx context.Context, projectID string) (*types.Project, error) {
	item, ok := m.projects[projectID]
	if !ok {
		return nil, errors.New("project not found")
	}
	return item, nil
}

func (m *mockContainerRepo) UpdateAppSession(ctx context.Context, item *types.AppSession) error {
	m.sessions[item.ID] = item
	return nil
}

func (m *mockContainerRepo) DeleteAppSession(ctx context.Context, id int64) error {
	delete(m.sessions, id)
	return nil
}

func (m *mockContainerRepo) ListAppSession(ctx context.Context) ([]*types.AppSession, error) {
	items := make([]*types.AppSession, 0, len(m.sessions))
	for _, v := range m.sessions {
		items = append(items, v)
	}
	return items, nil
}

func (m *mockContainerRepo) PageAppSessionByUserID(ctx context.Context, userID string, pagination *types.Pagination, query *types.AppSessionPageQuery) ([]*types.AppSession, int64, error) {
	items := make([]*types.AppSession, 0)
	for _, v := range m.sessions {
		if userID != "" && v.UserID != userID {
			continue
		}
		if query != nil && query.AnalysisNodeID != nil && v.AnalysisNodeID != *query.AnalysisNodeID {
			continue
		}
		if query != nil && query.ProjectID != nil && v.ProjectID != *query.ProjectID {
			continue
		}
		items = append(items, v)
	}
	return items, int64(len(items)), nil
}

func (m *mockContainerRepo) CreateContainerInstance(ctx context.Context, item *types.ContainerInstance) error {
	if item.ID == 0 {
		item.ID = m.next()
	}
	m.instances[item.ID] = item
	return nil
}

func (m *mockContainerRepo) GetContainerInstanceByID(ctx context.Context, id int64) (*types.ContainerInstance, error) {
	item, ok := m.instances[id]
	if !ok {
		return nil, errors.New("container instance not found")
	}
	return item, nil
}

func (m *mockContainerRepo) GetContainerInstanceByRuntimeID(ctx context.Context, runtimeID string) (*types.ContainerInstance, error) {
	for _, v := range m.instances {
		if v.RuntimeID == runtimeID {
			return v, nil
		}
	}
	return nil, errors.New("container instance not found")
}

func (m *mockContainerRepo) GetContainerInstanceByOwner(ctx context.Context, ownerType types.ContainerOwnerType, ownerID int64) (*types.ContainerInstance, error) {
	for _, v := range m.instances {
		if v.OwnerType == ownerType && v.OwnerID == ownerID {
			return v, nil
		}
	}
	return nil, errors.New("container instance not found")
}

func (m *mockContainerRepo) UpdateContainerInstance(ctx context.Context, item *types.ContainerInstance) error {
	m.instances[item.ID] = item
	return nil
}

func (m *mockContainerRepo) DeleteContainerInstance(ctx context.Context, id int64) error {
	delete(m.instances, id)
	return nil
}

func (m *mockContainerRepo) ListContainerInstance(ctx context.Context) ([]*types.ContainerInstance, error) {
	items := make([]*types.ContainerInstance, 0, len(m.instances))
	for _, v := range m.instances {
		items = append(items, v)
	}
	return items, nil
}

func (m *mockContainerRepo) CountContainerInstanceByStatuses(ctx context.Context, statuses []types.ContainerStatus) (int64, error) {
	if len(statuses) == 0 {
		return 0, nil
	}

	statusSet := make(map[types.ContainerStatus]struct{}, len(statuses))
	for _, status := range statuses {
		statusSet[status] = struct{}{}
	}

	var total int64
	for _, item := range m.instances {
		if item == nil {
			continue
		}
		if _, ok := statusSet[item.Status]; ok {
			total++
		}
	}

	return total, nil
}

func (m *mockContainerRepo) ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx context.Context, ownerType types.ContainerOwnerType, ownerIDs []int64) ([]*types.ContainerInstance, error) {
	if len(ownerIDs) == 0 {
		return []*types.ContainerInstance{}, nil
	}

	ownerSet := make(map[int64]struct{}, len(ownerIDs))
	for _, id := range ownerIDs {
		ownerSet[id] = struct{}{}
	}

	items := make([]*types.ContainerInstance, 0)
	for _, v := range m.instances {
		if v.OwnerType != ownerType {
			continue
		}
		if _, ok := ownerSet[v.OwnerID]; !ok {
			continue
		}
		items = append(items, v)
	}
	return items, nil
}

func (m *mockContainerRepo) PageContainerInstance(ctx context.Context, pagination *types.Pagination) ([]*types.ContainerInstance, int64, error) {
	items, err := m.ListContainerInstance(ctx)
	if err != nil {
		return nil, 0, err
	}
	return items, int64(len(items)), nil
}

func (m *mockContainerRepo) CreateContainerEvent(ctx context.Context, item *types.ContainerEvent) error {
	if item.ID == 0 {
		item.ID = m.next()
	}
	m.events = append(m.events, item)
	return nil
}

func (m *mockContainerRepo) GetContainerEventByID(ctx context.Context, id int64) (*types.ContainerEvent, error) {
	for _, v := range m.events {
		if v.ID == id {
			return v, nil
		}
	}
	return nil, errors.New("container event not found")
}

func (m *mockContainerRepo) UpdateContainerEvent(ctx context.Context, item *types.ContainerEvent) error {
	return nil
}

func (m *mockContainerRepo) DeleteContainerEvent(ctx context.Context, id int64) error {
	return nil
}

func (m *mockContainerRepo) ListContainerEvent(ctx context.Context) ([]*types.ContainerEvent, error) {
	return m.events, nil
}

func (m *mockContainerRepo) PageContainerEvent(ctx context.Context, pagination *types.Pagination) ([]*types.ContainerEvent, int64, error) {
	items, err := m.ListContainerEvent(ctx)
	if err != nil {
		return nil, 0, err
	}
	return items, int64(len(items)), nil
}

func (m *mockContainerRepo) CreateOutboxEvent(ctx context.Context, item *types.OutboxEvent) error {
	if item.ID == 0 {
		item.ID = m.next()
	}
	if item.Status == "" {
		item.Status = "pending"
	}
	m.outbox = append(m.outbox, item)
	return nil
}

func (m *mockContainerRepo) ListPendingOutboxEvent(ctx context.Context, limit int) ([]*types.OutboxEvent, error) {
	items := make([]*types.OutboxEvent, 0)
	for _, v := range m.outbox {
		if v.Status == "pending" {
			items = append(items, v)
			if limit > 0 && len(items) >= limit {
				break
			}
		}
	}
	return items, nil
}

func (m *mockContainerRepo) ListPendingOutboxEventsByType(ctx context.Context, eventType string, limit int) ([]*types.OutboxEvent, error) {
	items := make([]*types.OutboxEvent, 0)
	for _, v := range m.outbox {
		if v.Status == "pending" && v.Type == eventType {
			items = append(items, v)
			if limit > 0 && len(items) >= limit {
				break
			}
		}
	}
	return items, nil
}

func (m *mockContainerRepo) CountPendingOutboxEvents(ctx context.Context, eventTypes ...string) (int64, error) {
	if len(eventTypes) == 0 {
		var total int64
		for _, v := range m.outbox {
			if v.Status == "pending" {
				total++
			}
		}
		return total, nil
	}

	allowed := make(map[string]struct{}, len(eventTypes))
	for _, eventType := range eventTypes {
		allowed[eventType] = struct{}{}
	}

	var count int64
	for _, v := range m.outbox {
		if v.Status != "pending" {
			continue
		}
		if _, ok := allowed[v.Type]; ok {
			count++
		}
	}
	return count, nil
}

func (m *mockContainerRepo) CountPendingOutboxEventsByType(ctx context.Context, eventType string) (int64, error) {
	var count int64
	for _, v := range m.outbox {
		if v.Status == "pending" && v.Type == eventType {
			count++
		}
	}
	return count, nil
}

func (m *mockContainerRepo) MarkOutboxEventProcessing(ctx context.Context, id int64) error {
	for _, v := range m.outbox {
		if v.ID == id {
			v.Status = "processing"
			return nil
		}
	}
	return errors.New("outbox event not found")
}

func (m *mockContainerRepo) MarkOutboxEventPending(ctx context.Context, id int64) error {
	for _, v := range m.outbox {
		if v.ID == id {
			v.Status = "pending"
			return nil
		}
	}
	return errors.New("outbox event not found")
}

func (m *mockContainerRepo) MarkOutboxEventSent(ctx context.Context, id int64) error {
	for _, v := range m.outbox {
		if v.ID == id {
			v.Status = "sent"
			return nil
		}
	}
	return errors.New("outbox event not found")
}

func (m *mockContainerRepo) PageOutboxEvent(ctx context.Context, pagination *types.Pagination) ([]*types.OutboxEvent, int64, error) {
	return m.outbox, int64(len(m.outbox)), nil
}

type dockerMockRuntime struct {
	handler    containerruntime.RuntimeEventHandler
	runtimeID  string
	lastSpec   *types.ContainerSpec
	lastImage  string
	lastPolicy string
	monitored  []string
	startErr   error
	stopErr    error
	deleteErr  error
	createErr  error
	imageErr   error
	monitorErr error
	logsResult string
}

func (d *dockerMockRuntime) Name() string { return "docker" }

func (d *dockerMockRuntime) Create(ctx context.Context, spec *types.ContainerSpec) (string, error) {
	d.lastSpec = spec
	if d.createErr != nil {
		return "", d.createErr
	}
	if d.runtimeID == "" {
		d.runtimeID = "docker-rt-1"
	}
	return d.runtimeID, nil
}

func (d *dockerMockRuntime) EnsureImage(ctx context.Context, image string, pullPolicy string) error {
	d.lastImage = image
	d.lastPolicy = pullPolicy
	return d.imageErr
}

func (d *dockerMockRuntime) Start(ctx context.Context, runtimeID string) error  { return d.startErr }
func (d *dockerMockRuntime) Stop(ctx context.Context, runtimeID string) error   { return d.stopErr }
func (d *dockerMockRuntime) Pause(ctx context.Context, runtimeID string) error  { return nil }
func (d *dockerMockRuntime) Resume(ctx context.Context, runtimeID string) error { return nil }

func (d *dockerMockRuntime) Delete(ctx context.Context, runtimeID string) error { return d.deleteErr }

func (d *dockerMockRuntime) Logs(ctx context.Context, runtimeID string, tail int) (string, error) {
	if d.logsResult == "" {
		return fmt.Sprintf("logs(%s,%d)", runtimeID, tail), nil
	}
	return d.logsResult, nil
}

func (d *dockerMockRuntime) SetEventHandler(handler containerruntime.RuntimeEventHandler) {
	d.handler = handler
}

func (d *dockerMockRuntime) Exec(ctx context.Context, runtimeID string, cmd []string) (string, error) {
	return "", nil
}

func (d *dockerMockRuntime) Monitor(ctx context.Context, runtimeID string) error {
	if d.monitorErr != nil {
		return d.monitorErr
	}
	d.monitored = append(d.monitored, runtimeID)
	return nil
}

func newTestManager(repo *mockContainerRepo, rt *dockerMockRuntime) *ContainerManager {
	mgr, _ := newTestManagerWithWorker(repo, rt)
	return mgr
}

func newTestManagerWithWorker(repo *mockContainerRepo, rt *dockerMockRuntime) (*ContainerManager, *ContainerCreateWorker) {
	reg := containerruntime.NewRegistry()
	reg.Register("docker", rt)
	imgMgr := NewImageManager(repo, reg)
	mgr := NewContainerManager(repo, nil, nil, nil, reg, nil, NewDefaultContainerRuntimeResolver(), imgMgr, nil)
	worker := NewContainerCreateWorker(repo, nil, nil, nil, reg, NewDefaultContainerRuntimeResolver(), imgMgr, nil, nil)
	return mgr, worker
}

func TestContainerManager_QueueStatus_PendingIncludesCreateAndStart(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	repo.outbox = append(repo.outbox,
		&types.OutboxEvent{Type: OutboxEventTypeCreateRequest, Status: "pending"},
		&types.OutboxEvent{Type: OutboxEventTypeStartRequest, Status: "pending"},
		&types.OutboxEvent{Type: OutboxEventTypeStopRequest, Status: "pending"},
		&types.OutboxEvent{Type: OutboxEventTypeStartRequest, Status: "sent"},
	)

	mgr := NewContainerManager(repo, nil, nil, nil, nil, nil, NewDefaultContainerRuntimeResolver(), nil, nil)
	status, err := mgr.QueueStatus(ctx)
	if err != nil {
		t.Fatalf("QueueStatus failed: %v", err)
	}

	if status.PendingCount != 2 {
		t.Fatalf("expected pending create/start count 2, got %d", status.PendingCount)
	}
}

func mustSeedTemplate(t *testing.T, repo *mockContainerRepo) {
	t.Helper()
	repo.images[1] = &types.ContainerImage{ID: 1, FullName: "docker.io/library/busybox:latest"}
	repo.templates[10] = &types.ContainerTemplate{ID: 10, ImageID: 1, Command: "echo ok"}
}

func TestContainerManager_CreateByTemplate_StaysCreatingUntilStartedEvent(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	mustSeedTemplate(t, repo)
	rt := &dockerMockRuntime{runtimeID: "docker-abc-1"}
	mgr, worker := newTestManagerWithWorker(repo, rt)

	// create instance via CreateByTemplate (always enqueues)
	inst, err := mgr.CreateByTemplate(ctx, "docker", 10, types.ContainerOwnerAppSession, 1001, "demo")
	if err != nil {
		t.Fatalf("CreateByTemplate failed: %v", err)
	}

	// After enqueue, status should still be pending (not yet processed by worker)
	if inst.Status != types.ContainerCreatePending {
		t.Fatalf("expected pending after enqueue, got %s", inst.Status)
	}

	// Now simulate worker processing: execute create synchronously
	if err := worker.executeCreate(ctx, "docker", 10, types.ContainerOwnerAppSession, 1001, "demo", inst.ID); err != nil {
		t.Fatalf("worker.executeCreate failed: %v", err)
	}

	// After execution, status should be creating (before started event)
	stored, err := repo.GetContainerInstanceByID(ctx, inst.ID)
	if err != nil {
		t.Fatalf("load instance failed: %v", err)
	}
	if stored.Status != types.ContainerCreating {
		t.Fatalf("expected stored status creating before runtime started event, got %s", stored.Status)
	}
}

func TestContainerManager_OnEvent_ContainerDeleted_DeletesContainerInstance(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	rt := &dockerMockRuntime{}
	mgr := newTestManager(repo, rt)

	inst := &types.ContainerInstance{RuntimeID: "docker-abc-deleted", Status: types.ContainerRunning, Name: "demo"}
	if err := repo.CreateContainerInstance(ctx, inst); err != nil {
		t.Fatalf("seed instance failed: %v", err)
	}

	mgr.OnEvent(containerruntime.RuntimeEvent{Type: "ContainerDeleted", RuntimeID: inst.RuntimeID, Message: "container not found"})
	if _, err := repo.GetContainerInstanceByID(ctx, inst.ID); err == nil {
		t.Fatalf("expected container instance deleted after ContainerDeleted event")
	}
}

func TestContainerManager_RecoverRuntimeMonitoring_OnlyActiveStatuses(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	rt := &dockerMockRuntime{}
	mgr := newTestManager(repo, rt)

	activeRunning := &types.ContainerInstance{RuntimeID: "docker-run-1", Status: types.ContainerRunning, Name: "run"}
	activePaused := &types.ContainerInstance{RuntimeID: "docker-pause-1", Status: types.ContainerPaused, Name: "pause"}
	inactiveStopped := &types.ContainerInstance{RuntimeID: "docker-stop-1", Status: types.ContainerStopped, Name: "stop"}
	noRuntimeID := &types.ContainerInstance{RuntimeID: "", Status: types.ContainerRunning, Name: "no-runtime"}

	for _, inst := range []*types.ContainerInstance{activeRunning, activePaused, inactiveStopped, noRuntimeID} {
		if err := repo.CreateContainerInstance(ctx, inst); err != nil {
			t.Fatalf("seed instance failed: %v", err)
		}
	}

	recovered, err := mgr.RecoverRuntimeMonitoring(ctx)
	if err != nil {
		t.Fatalf("recover runtime monitoring failed: %v", err)
	}
	if recovered != 2 {
		t.Fatalf("expected 2 recovered monitors, got %d", recovered)
	}
	if len(rt.monitored) != 2 {
		t.Fatalf("expected 2 monitor calls, got %d", len(rt.monitored))
	}
}

func TestParseEnv_SupportsMixedScalarValues(t *testing.T) {
	raw := []byte(`{"USERID":"$USERID","GROUPID":"$GROUPID","R_SCRIPT":"$SCRIPT_FILE","R_LIBS_USER":"$R_PACKAGE_DIR","DISABLE_AUTH":true,"R_USER_WORKDIR":"$OUTPUT_DIR","POSIT_ASSISTANT_ENABLED":0,"RSTUDIO_DISABLE_CHECK_FOR_UPDATES":1}`)

	env := parseEnv(raw)

	if got := env["DISABLE_AUTH"]; got != "true" {
		t.Fatalf("expected DISABLE_AUTH=true, got %q", got)
	}
	if got := env["POSIT_ASSISTANT_ENABLED"]; got != "0" {
		t.Fatalf("expected POSIT_ASSISTANT_ENABLED=0, got %q", got)
	}
	if got := env["RSTUDIO_DISABLE_CHECK_FOR_UPDATES"]; got != "1" {
		t.Fatalf("expected RSTUDIO_DISABLE_CHECK_FOR_UPDATES=1, got %q", got)
	}
	if got := env["USERID"]; got != "$USERID" {
		t.Fatalf("expected USERID to preserve placeholder, got %q", got)
	}
}

func TestContainerManager_CreateByTemplate_ResolvesEnvAndVolumes(t *testing.T) {
	ctx := context.Background()
	t.Setenv("USERID", "u-100")
	t.Setenv("GROUPID", "g-200")
	t.Setenv("SCRIPT_FILE", "/workspace/run.R")
	t.Setenv("R_PACKAGE_DIR", "/workspace/.Rlibs")
	t.Setenv("OUTPUT_DIR", "/workspace/output")
	t.Setenv("R_PROFILE", "/home/rstudio/.Rprofile")

	repo := newMockContainerRepo()
	repo.images[1] = &types.ContainerImage{ID: 1, FullName: "docker.io/library/busybox:latest"}
	repo.templates[10] = &types.ContainerTemplate{
		ID:      10,
		ImageID: 1,
		Command: "bash -lc echo ok",
		Env:     datatypes.JSON([]byte(`{"USERID":"$USERID","GROUPID":"$GROUPID","R_SCRIPT":"$SCRIPT_FILE","R_LIBS_USER":"$R_PACKAGE_DIR","DISABLE_AUTH":true,"R_USER_WORKDIR":"$OUTPUT_DIR"}`)),
		Volumes: datatypes.JSON([]byte(`{"$R_PROFILE":{"bind":"/home/rstudio/.Rprofile","mode":"rw"}}`)),
	}

	rt := &dockerMockRuntime{runtimeID: "docker-abc-9"}
	_, worker := newTestManagerWithWorker(repo, rt)

	// Seed instance and execute create synchronously
	inst := &types.ContainerInstance{TemplateID: 10, OwnerType: types.ContainerOwnerAppSession, OwnerID: 1001, Name: "demo", Status: types.ContainerCreatePending}
	_ = repo.CreateContainerInstance(ctx, inst)
	if err := worker.executeCreate(ctx, "docker", 10, types.ContainerOwnerAppSession, 1001, "demo", inst.ID); err != nil {
		t.Fatalf("worker.executeCreate failed: %v", err)
	}

	if rt.lastSpec == nil {
		t.Fatalf("runtime create spec was not captured")
	}
	if got := rt.lastSpec.Env["USERID"]; got != "u-100" {
		t.Fatalf("expected USERID to resolve, got %q", got)
	}
	if got := rt.lastSpec.Env["GROUPID"]; got != "g-200" {
		t.Fatalf("expected GROUPID to resolve, got %q", got)
	}
	if got := rt.lastSpec.Env["DISABLE_AUTH"]; got != "true" {
		t.Fatalf("expected DISABLE_AUTH to become string true, got %q", got)
	}
	if got := rt.lastSpec.Env["R_USER_WORKDIR"]; got != "/workspace/output" {
		t.Fatalf("expected R_USER_WORKDIR to resolve, got %q", got)
	}
	if len(rt.lastSpec.Volumes) != 1 {
		t.Fatalf("expected one resolved volume, got %d", len(rt.lastSpec.Volumes))
	}
	if got := rt.lastSpec.Volumes[0].Target; got != "/home/rstudio/.Rprofile" {
		t.Fatalf("expected target to resolve from $R_PROFILE, got %q", got)
	}
	if got := rt.lastSpec.Volumes[0].Source; got != "/home/rstudio/.Rprofile" {
		t.Fatalf("expected source from bind, got %q", got)
	}
	if got := rt.lastSpec.Volumes[0].Mode; got != "rw" {
		t.Fatalf("expected mode rw, got %q", got)
	}
}

func TestContainerManager_CreateByTemplate_UsesAppSessionContextVariables(t *testing.T) {
	ctx := context.WithValue(context.Background(), types.UserIDContextKey, "ctx-user")
	t.Setenv("USERID", "env-user")
	t.Setenv("PROJECTID", "env-project")

	repo := newMockContainerRepo()
	repo.images[1] = &types.ContainerImage{ID: 1, FullName: "docker.io/library/busybox:latest"}
	repo.templates[10] = &types.ContainerTemplate{
		ID:      10,
		ImageID: 1,
		Command: "bash -lc echo ok",
		Env:     datatypes.JSON([]byte(`{"USERID":"$USERID","PROJECTID":"$PROJECTID","APP_SESSION_ID":"$APP_SESSION_ID"}`)),
	}
	repo.sessions[1001] = &types.AppSession{ID: 1001, UserID: "session-user", ProjectID: 001}

	rt := &dockerMockRuntime{runtimeID: "docker-abc-10"}
	_, worker := newTestManagerWithWorker(repo, rt)

	// Seed instance and execute create synchronously
	inst := &types.ContainerInstance{TemplateID: 10, OwnerType: types.ContainerOwnerAppSession, OwnerID: 1001, Name: "demo", Status: types.ContainerCreatePending}
	_ = repo.CreateContainerInstance(ctx, inst)
	if err := worker.executeCreate(ctx, "docker", 10, types.ContainerOwnerAppSession, 1001, "demo", inst.ID); err != nil {
		t.Fatalf("worker.executeCreate failed: %v", err)
	}

	if rt.lastSpec == nil {
		t.Fatalf("runtime create spec was not captured")
	}
	if got := rt.lastSpec.Env["USERID"]; got != "session-user" {
		t.Fatalf("expected USERID from app session context, got %q", got)
	}
	if got := rt.lastSpec.Env["PROJECTID"]; got != "session-project" {
		t.Fatalf("expected PROJECTID from app session context, got %q", got)
	}
	if got := rt.lastSpec.Env["APP_SESSION_ID"]; got != "1001" {
		t.Fatalf("expected APP_SESSION_ID from owner context, got %q", got)
	}
}

func TestContainerManager_CreateByTemplate_AppSessionMergesProjectVolumes(t *testing.T) {
	ctx := context.Background()

	repo := newMockContainerRepo()
	repo.images[1] = &types.ContainerImage{ID: 1, FullName: "docker.io/library/busybox:latest"}
	repo.templates[10] = &types.ContainerTemplate{
		ID:      10,
		ImageID: 1,
		Command: "bash -lc echo ok",
		Volumes: datatypes.JSON([]byte(`[{"source":"/template/src","target":"/template/dst","mode":"ro"}]`)),
	}
	repo.sessions[1001] = &types.AppSession{ID: 1001, UserID: "session-user", ProjectID: 002}
	repo.projects["session-project"] = &types.Project{
		ProjectID: "session-project",
		Volumes:   datatypes.JSON([]byte(`[{"source":"/project/src","target":"/project/dst","mode":"rw"}]`)),
	}

	rt := &dockerMockRuntime{runtimeID: "docker-abc-11"}
	_, worker := newTestManagerWithWorker(repo, rt)

	// Seed instance and execute create synchronously
	inst := &types.ContainerInstance{TemplateID: 10, OwnerType: types.ContainerOwnerAppSession, OwnerID: 1001, Name: "demo", Status: types.ContainerCreatePending}
	_ = repo.CreateContainerInstance(ctx, inst)
	if err := worker.executeCreate(ctx, "docker", 10, types.ContainerOwnerAppSession, 1001, "demo", inst.ID); err != nil {
		t.Fatalf("worker.executeCreate failed: %v", err)
	}

	if rt.lastSpec == nil {
		t.Fatalf("runtime create spec was not captured")
	}
	if len(rt.lastSpec.Volumes) != 2 {
		t.Fatalf("expected merged template+project volumes, got %d", len(rt.lastSpec.Volumes))
	}
	if got := rt.lastSpec.Volumes[0].Source; got != "/template/src" {
		t.Fatalf("expected first volume from template, got %q", got)
	}
	if got := rt.lastSpec.Volumes[1].Source; got != "/project/src" {
		t.Fatalf("expected second volume from project, got %q", got)
	}
}

func TestContainerManager_CreateByTemplate_DagNodeUsesTaskCommand(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LOG_PATH", "/tmp/dag-task.log")

	repo := newMockContainerRepo()
	repo.images[1] = &types.ContainerImage{ID: 1, FullName: "docker.io/library/busybox:latest"}
	repo.templates[10] = &types.ContainerTemplate{
		ID:      10,
		ImageID: 1,
		Command: "sleep infinity",
	}

	rt := &dockerMockRuntime{runtimeID: "docker-abc-dag"}
	_, worker := newTestManagerWithWorker(repo, rt)

	// Seed instance and execute create synchronously
	inst := &types.ContainerInstance{TemplateID: 10, OwnerType: types.ContainerOwnerDagNode, OwnerID: 2001, Name: "dag-node-1", Status: types.ContainerCreatePending}
	_ = repo.CreateContainerInstance(ctx, inst)
	if err := worker.executeCreate(ctx, "docker", 10, types.ContainerOwnerDagNode, 2001, "dag-node-1", inst.ID); err != nil {
		t.Fatalf("worker.executeCreate failed: %v", err)
	}

	if rt.lastSpec == nil {
		t.Fatalf("runtime create spec was not captured")
	}
	if len(rt.lastSpec.Entrypoint) != 1 || rt.lastSpec.Entrypoint[0] != "bash" {
		t.Fatalf("expected entrypoint [bash], got %#v", rt.lastSpec.Entrypoint)
	}
	if len(rt.lastSpec.Command) != 2 {
		t.Fatalf("expected command length 2, got %#v", rt.lastSpec.Command)
	}
	if rt.lastSpec.Command[0] != "-c" {
		t.Fatalf("expected command[0] to be -c, got %q", rt.lastSpec.Command[0])
	}
	expectedScript := "bash ./run.sh 2>&1 | tee '/tmp/dag-task.log'; exit ${PIPESTATUS[0]}"
	if rt.lastSpec.Command[1] != expectedScript {
		t.Fatalf("unexpected task script, expected %q, got %q", expectedScript, rt.lastSpec.Command[1])
	}
}

func TestContainerManager_CreateByTemplate_ParsesSchedulingConstraint(t *testing.T) {
	ctx := context.Background()

	repo := newMockContainerRepo()
	repo.images[1] = &types.ContainerImage{ID: 1, FullName: "docker.io/library/busybox:latest"}
	repo.templates[10] = &types.ContainerTemplate{
		ID:      10,
		ImageID: 1,
		Command: "echo ok",
		SchedulingConstraint: datatypes.JSON([]byte(`{
			"constraints": [
				{"type":"node","key":"hostname","operator":"In","values":["worker01","worker02"]},
				{"type":"resource","key":"gpu","operator":"Exists"}
			]
		}`)),
	}

	rt := &dockerMockRuntime{runtimeID: "docker-abc-node-selector"}
	_, worker := newTestManagerWithWorker(repo, rt)

	// Seed instance and execute create synchronously
	inst := &types.ContainerInstance{TemplateID: 10, OwnerType: types.ContainerOwnerAppSession, OwnerID: 1001, Name: "demo", Status: types.ContainerCreatePending}
	_ = repo.CreateContainerInstance(ctx, inst)
	if err := worker.executeCreate(ctx, "docker", 10, types.ContainerOwnerAppSession, 1001, "demo", inst.ID); err != nil {
		t.Fatalf("worker.executeCreate failed: %v", err)
	}

	if rt.lastSpec == nil {
		t.Fatalf("runtime create spec was not captured")
	}
	if rt.lastSpec.SchedulingConstraint == nil {
		t.Fatalf("expected scheduling constraint to be parsed")
	}
	if len(rt.lastSpec.SchedulingConstraint.Constraints) != 2 {
		t.Fatalf("expected 2 constraints, got %d", len(rt.lastSpec.SchedulingConstraint.Constraints))
	}
	if got := rt.lastSpec.SchedulingConstraint.Constraints[0].Type; got != "node" {
		t.Fatalf("expected first constraint type node, got %q", got)
	}
	if got := rt.lastSpec.SchedulingConstraint.Constraints[0].Key; got != "hostname" {
		t.Fatalf("expected first constraint key hostname, got %q", got)
	}
	if got := rt.lastSpec.SchedulingConstraint.Constraints[0].Operator; got != "In" {
		t.Fatalf("expected first constraint operator In, got %q", got)
	}
	if got := len(rt.lastSpec.SchedulingConstraint.Constraints[0].Values); got != 2 {
		t.Fatalf("expected first constraint value size 2, got %d", got)
	}
}

func TestContainerManager_CreateByTemplate_UpdatesImageStatusToReady(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	repo.images[1] = &types.ContainerImage{ID: 1, FullName: "docker.io/library/busybox:latest", Status: types.ImageStatusPending}
	repo.templates[10] = &types.ContainerTemplate{ID: 10, ImageID: 1, Command: "echo ok"}

	rt := &dockerMockRuntime{runtimeID: "docker-abc-img"}
	_, worker := newTestManagerWithWorker(repo, rt)

	// Seed instance and execute create synchronously
	inst := &types.ContainerInstance{TemplateID: 10, OwnerType: types.ContainerOwnerAppSession, OwnerID: 1001, Name: "demo", Status: types.ContainerCreatePending}
	_ = repo.CreateContainerInstance(ctx, inst)
	if err := worker.executeCreate(ctx, "docker", 10, types.ContainerOwnerAppSession, 1001, "demo", inst.ID); err != nil {
		t.Fatalf("worker.executeCreate failed: %v", err)
	}

	img, err := repo.GetContainerImageByID(ctx, 1)
	if err != nil {
		t.Fatalf("load image failed: %v", err)
	}
	if img.Status != types.ImageStatusReady {
		t.Fatalf("expected image status ready, got %s", img.Status)
	}
	if rt.lastImage != "docker.io/library/busybox:latest" {
		t.Fatalf("expected image ensure call with full name, got %q", rt.lastImage)
	}
}

func TestContainerManager_CreateByTemplate_ImagePrepareFailureMarksImageFailed(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	repo.images[1] = &types.ContainerImage{ID: 1, FullName: "docker.io/library/busybox:latest", Status: types.ImageStatusPending}
	repo.templates[10] = &types.ContainerTemplate{ID: 10, ImageID: 1, Command: "echo ok"}

	rt := &dockerMockRuntime{runtimeID: "docker-abc-12", imageErr: errors.New("pull denied")}
	_, worker := newTestManagerWithWorker(repo, rt)

	// Seed instance and execute create synchronously
	inst := &types.ContainerInstance{TemplateID: 10, OwnerType: types.ContainerOwnerAppSession, OwnerID: 1001, Name: "demo", Status: types.ContainerCreatePending}
	_ = repo.CreateContainerInstance(ctx, inst)
	err := worker.executeCreate(ctx, "docker", 10, types.ContainerOwnerAppSession, 1001, "demo", inst.ID)
	if err == nil {
		t.Fatalf("expected worker.executeCreate to fail when image prepare fails")
	}

	img, err := repo.GetContainerImageByID(ctx, 1)
	if err != nil {
		t.Fatalf("load image failed: %v", err)
	}
	if img.Status != types.ImageStatusFailed {
		t.Fatalf("expected image status failed, got %s", img.Status)
	}
	if img.LastError == "" {
		t.Fatalf("expected image last error to be stored")
	}
}

func TestImageManager_RefreshAllStatuses_UpdatesReadyAndSkipsDisabledDeleted(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	repo.images[1] = &types.ContainerImage{ID: 1, FullName: "docker.io/library/busybox:latest", Status: types.ImageStatusPending}
	repo.images[2] = &types.ContainerImage{ID: 2, FullName: "docker.io/library/alpine:latest", Status: types.ImageStatusDisabled}
	repo.images[3] = &types.ContainerImage{ID: 3, FullName: "docker.io/library/debian:latest", Status: types.ImageStatusDeleted}

	rt := &dockerMockRuntime{}
	reg := containerruntime.NewRegistry()
	reg.Register("docker", rt)
	mgr := NewImageManager(repo, reg)

	if err := mgr.RefreshAllStatuses(ctx); err != nil {
		t.Fatalf("RefreshAllStatuses failed: %v", err)
	}

	if repo.images[1].Status != types.ImageStatusReady {
		t.Fatalf("expected image 1 status ready, got %s", repo.images[1].Status)
	}
	if repo.images[2].Status != types.ImageStatusDisabled {
		t.Fatalf("expected image 2 status unchanged disabled, got %s", repo.images[2].Status)
	}
	if repo.images[3].Status != types.ImageStatusDeleted {
		t.Fatalf("expected image 3 status unchanged deleted, got %s", repo.images[3].Status)
	}
}

func TestImageManager_RefreshAllStatuses_ReturnsErrorWhenImageRefreshFails(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	repo.images[1] = &types.ContainerImage{ID: 1, FullName: "docker.io/library/busybox:latest", Status: types.ImageStatusPending}

	rt := &dockerMockRuntime{imageErr: errors.New("pull denied")}
	reg := containerruntime.NewRegistry()
	reg.Register("docker", rt)
	mgr := NewImageManager(repo, reg)

	err := mgr.RefreshAllStatuses(ctx)
	if err == nil {
		t.Fatalf("expected refresh error")
	}
	if repo.images[1].Status != types.ImageStatusFailed {
		t.Fatalf("expected image status failed, got %s", repo.images[1].Status)
	}
}
