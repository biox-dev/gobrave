package v1

import (
	"context"
	"fmt"
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

// fakeContainerService 只实现安装容器资产用到的那些方法，其余方法由嵌入的（nil）接口提供。
type fakeContainerService struct {
	interfaces.ContainerService
	specs       map[int64]*types.ContainerTemplateSpec
	definitions map[int64]*types.ContainerTemplateDefinition
	calls       []string
}

func newFakeContainerService() *fakeContainerService {
	return &fakeContainerService{
		specs:       make(map[int64]*types.ContainerTemplateSpec),
		definitions: make(map[int64]*types.ContainerTemplateDefinition),
	}
}

func (f *fakeContainerService) CreateContainerTemplateSpec(_ context.Context, item *types.ContainerTemplateSpec) error {
	clone := *item
	f.specs[item.ID] = &clone
	f.calls = append(f.calls, fmt.Sprintf("create-spec:%d", item.ID))
	return nil
}

func (f *fakeContainerService) GetContainerTemplateSpecByID(_ context.Context, id int64) (*types.ContainerTemplateSpec, error) {
	item, ok := f.specs[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	clone := *item
	return &clone, nil
}

func (f *fakeContainerService) UpdateContainerTemplateSpec(_ context.Context, item *types.ContainerTemplateSpec) error {
	clone := *item
	f.specs[item.ID] = &clone
	f.calls = append(f.calls, fmt.Sprintf("update-spec:%d", item.ID))
	return nil
}

func (f *fakeContainerService) CreateContainerTemplateDefinition(_ context.Context, item *types.ContainerTemplateDefinition) error {
	clone := *item
	f.definitions[item.ID] = &clone
	f.calls = append(f.calls, fmt.Sprintf("create-definition:%d", item.ID))
	return nil
}

func (f *fakeContainerService) GetContainerTemplateDefinitionByID(_ context.Context, id int64) (*types.ContainerTemplateDefinition, error) {
	item, ok := f.definitions[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	clone := *item
	return &clone, nil
}

func (f *fakeContainerService) UpdateContainerTemplateDefinition(_ context.Context, item *types.ContainerTemplateDefinition) error {
	clone := *item
	f.definitions[item.ID] = &clone
	f.calls = append(f.calls, fmt.Sprintf("update-definition:%d", item.ID))
	return nil
}

// TestInstallContainerAssetsUpsertsByID 覆盖安装侧对 container_template_specs /
// container_template_definitions 的消费：有 id 且已存在→更新，不存在→新增；
// 顺序必须是「运行配置 → 绑定行」，且绑定行对运行配置（spec_id）的引用与镜像配置保持原样。
func TestInstallContainerAssetsUpsertsByID(t *testing.T) {
	svc := newFakeContainerService()
	svc.specs[301] = &types.ContainerTemplateSpec{ID: 301, Name: "old-301"}
	svc.definitions[201] = &types.ContainerTemplateDefinition{ID: 201, SpecID: 301, ImageName: "old:1", PullPolicy: types.PullPolicyAlways}
	c := &Codec{containerService: svc}

	specs := []types.ContainerTemplateSpec{
		{ID: 301, Name: "new-301", Command: "R -e 1"},
		{ID: 300, Name: "spec-300", Command: "R -e 1", CPU: 2, Port: 8787},
	}
	definitions := []types.ContainerTemplateDefinition{
		{ID: 201, SpecID: 301, ImageName: "docker.io/a/b:1", PullPolicy: types.PullPolicyNever},
		{ID: 200, SpecID: 300, ImageName: "docker.io/a/c:2", PullPolicy: types.PullPolicyIfNotPresent},
	}

	specCount, definitionCount, err := c.installContainerAssets(context.Background(), specs, definitions)
	if err != nil {
		t.Fatalf("installContainerAssets() error = %v", err)
	}
	if specCount != 2 || definitionCount != 2 {
		t.Fatalf("counts = (%d, %d), want (2, 2)", specCount, definitionCount)
	}

	if got := svc.specs[301].Name; got != "new-301" {
		t.Errorf("existing spec not updated: name = %q", got)
	}
	if got := svc.specs[300]; got == nil || got.Name != "spec-300" || got.Port != 8787 || got.CPU != 2 {
		t.Errorf("missing spec not created correctly: %+v", got)
	}
	if got := svc.definitions[201]; got == nil || got.ImageName != "docker.io/a/b:1" || got.PullPolicy != types.PullPolicyNever {
		t.Errorf("existing definition not updated correctly: %+v", got)
	}
	if got := svc.definitions[200]; got == nil || got.SpecID != 300 || got.ImageName != "docker.io/a/c:2" {
		t.Errorf("missing definition not created correctly: %+v", got)
	}

	wantCalls := []string{"update-spec:301", "create-spec:300", "update-definition:201", "create-definition:200"}
	for i, want := range wantCalls {
		if svc.calls[i] != want {
			t.Fatalf("calls = %v, want %v", svc.calls, wantCalls)
		}
	}

	// 再跑一次：运行配置与绑定行都已存在，必须走更新而不是重复新增。
	svc.calls = nil
	if _, _, err := c.installContainerAssets(context.Background(), specs, definitions); err != nil {
		t.Fatalf("second installContainerAssets() error = %v", err)
	}
	wantSecond := []string{"update-spec:301", "update-spec:300", "update-definition:201", "update-definition:200"}
	if len(svc.calls) != len(wantSecond) {
		t.Fatalf("second calls = %v, want %v", svc.calls, wantSecond)
	}
	for i, want := range wantSecond {
		if svc.calls[i] != want {
			t.Fatalf("second calls = %v, want %v", svc.calls, wantSecond)
		}
	}
}

// TestInstallContainerAssetsRequiresID 覆盖 id 缺失的导出内容（引用键断链）直接报错，
// 且空列表在 containerService 未装配时也不应报错（老产物没有这两个字段）。
func TestInstallContainerAssetsRequiresID(t *testing.T) {
	// 老产物没有 container_template_specs / container_template_definitions 字段：
	// 空列表直接放行，即使 containerService 未装配也不报错。
	if _, _, err := (&Codec{}).installContainerAssets(context.Background(), nil, nil); err != nil {
		t.Fatalf("empty assets error = %v, want nil", err)
	}

	// 有内容但没有 containerService（DI 未装配）时不能静默跳过。
	if _, _, err := (&Codec{}).installContainerAssets(context.Background(), nil, []types.ContainerTemplateDefinition{{ID: 1}}); err == nil {
		t.Fatal("nil container service: want error, got nil")
	}

	svc := newFakeContainerService()
	c := &Codec{containerService: svc}
	if _, _, err := c.installContainerAssets(context.Background(), []types.ContainerTemplateSpec{{Name: "no-id"}}, nil); err == nil {
		t.Fatal("spec without id: want error, got nil")
	}
	if _, _, err := c.installContainerAssets(context.Background(), nil, []types.ContainerTemplateDefinition{{SpecID: 1, ImageName: "docker.io/a/b:1"}}); err == nil {
		t.Fatal("definition without id: want error, got nil")
	}
	if len(svc.calls) != 0 {
		t.Fatalf("unexpected calls: %v", svc.calls)
	}
}
