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
// CreateContainerTemplateDefinition 复刻真实 service 层的校验：绑定行引用的镜像必须已存在，
// 以此验证安装侧「先镜像 → 再运行配置 → 最后绑定行」的顺序（镜像缺失时绑定行导入必须失败）。
type fakeContainerService struct {
	interfaces.ContainerService
	images      map[int64]*types.ContainerImage
	specs       map[int64]*types.ContainerTemplateSpec
	definitions map[int64]*types.ContainerTemplateDefinition
	calls       []string
}

func newFakeContainerService() *fakeContainerService {
	return &fakeContainerService{
		images:      make(map[int64]*types.ContainerImage),
		specs:       make(map[int64]*types.ContainerTemplateSpec),
		definitions: make(map[int64]*types.ContainerTemplateDefinition),
	}
}

func (f *fakeContainerService) CreateContainerImage(_ context.Context, item *types.ContainerImage) error {
	clone := *item
	f.images[item.ID] = &clone
	f.calls = append(f.calls, fmt.Sprintf("create-image:%d", item.ID))
	return nil
}

func (f *fakeContainerService) GetContainerImageByID(_ context.Context, id int64) (*types.ContainerImage, error) {
	item, ok := f.images[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	clone := *item
	return &clone, nil
}

func (f *fakeContainerService) UpdateContainerImage(_ context.Context, item *types.ContainerImage) error {
	clone := *item
	f.images[item.ID] = &clone
	f.calls = append(f.calls, fmt.Sprintf("update-image:%d", item.ID))
	return nil
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
	if _, ok := f.images[item.ImageID]; !ok {
		return gorm.ErrRecordNotFound
	}
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
	if _, ok := f.images[item.ImageID]; !ok {
		return gorm.ErrRecordNotFound
	}
	clone := *item
	f.definitions[item.ID] = &clone
	f.calls = append(f.calls, fmt.Sprintf("update-definition:%d", item.ID))
	return nil
}

// TestInstallContainerAssetsUpsertsByID 覆盖安装侧对 container_images / container_template_specs /
// container_template_definitions 的消费：有 id 且已存在→更新，不存在→新增；
// 顺序必须是「镜像 → 运行配置 → 绑定行」，且绑定行对镜像（image_id）与运行配置（spec_id）的引用保持原样。
func TestInstallContainerAssetsUpsertsByID(t *testing.T) {
	svc := newFakeContainerService()
	svc.images[101] = &types.ContainerImage{ID: 101, Name: "old-101", PullPolicy: types.PullPolicyAlways}
	c := &Codec{containerService: svc}

	images := []map[string]any{
		{"id": "101", "name": "new-101", "full_name": "docker.io/a/b:1", "pull_policy": types.PullPolicyNever},
		{"id": "100", "name": "new-100", "full_name": "docker.io/a/c:2", "size": float64(42)},
	}
	specs := []map[string]any{
		{"id": "300", "name": "spec-300", "command": "R -e 1", "cpu": float64(2), "port": float64(8787)},
	}
	definitions := []map[string]any{
		{"id": "200", "spec_id": "300", "image_id": "100", "r_library_path": "/lib/R"},
	}

	imageCount, specCount, definitionCount, err := c.installContainerAssets(context.Background(), images, specs, definitions)
	if err != nil {
		t.Fatalf("installContainerAssets() error = %v", err)
	}
	if imageCount != 2 || specCount != 1 || definitionCount != 1 {
		t.Fatalf("counts = (%d, %d, %d), want (2, 1, 1)", imageCount, specCount, definitionCount)
	}

	if got := svc.images[101].Name; got != "new-101" {
		t.Errorf("existing image not updated: name = %q", got)
	}
	if got := svc.images[101].PullPolicy; got != types.PullPolicyNever {
		t.Errorf("existing image pull_policy = %q, want %q", got, types.PullPolicyNever)
	}
	if got := svc.images[100]; got == nil || got.Name != "new-100" || got.Size != 42 {
		t.Errorf("missing image not created correctly: %+v", got)
	}
	// 导出文件没有 pull_policy 时按 service 同款兜底（ImportContainerTemplate = IfNotPresent）。
	if got := svc.images[100].PullPolicy; got != types.PullPolicyIfNotPresent {
		t.Errorf("created image pull_policy = %q, want %q", got, types.PullPolicyIfNotPresent)
	}
	if got := svc.specs[300]; got == nil || got.Name != "spec-300" || got.Port != 8787 || got.CPU != 2 {
		t.Errorf("missing spec not created correctly: %+v", got)
	}
	if got := svc.definitions[200]; got == nil || got.SpecID != 300 || got.ImageID != 100 || got.RLibraryPath != "/lib/R" {
		t.Errorf("missing definition not created correctly: %+v", got)
	}

	wantCalls := []string{"update-image:101", "create-image:100", "create-spec:300", "create-definition:200"}
	for i, want := range wantCalls {
		if svc.calls[i] != want {
			t.Fatalf("calls = %v, want %v", svc.calls, wantCalls)
		}
	}

	// 再跑一次：镜像、运行配置与绑定行都已存在，必须走更新而不是重复新增。
	svc.calls = nil
	if _, _, _, err := c.installContainerAssets(context.Background(), images, specs, definitions); err != nil {
		t.Fatalf("second installContainerAssets() error = %v", err)
	}
	wantSecond := []string{"update-image:101", "update-image:100", "update-spec:300", "update-definition:200"}
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
// 且空列表在 containerService 未装配时也不应报错（老产物没有这三个字段）。
func TestInstallContainerAssetsRequiresID(t *testing.T) {
	// 老产物没有 container_images / container_template_specs / container_template_definitions 字段：
	// 空列表直接放行，即使 containerService 未装配也不报错。
	if _, _, _, err := (&Codec{}).installContainerAssets(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("empty assets error = %v, want nil", err)
	}

	// 有内容但没有 containerService（DI 未装配）时不能静默跳过。
	if _, _, _, err := (&Codec{}).installContainerAssets(context.Background(), nil, nil, []map[string]any{{"id": "1"}}); err == nil {
		t.Fatal("nil container service: want error, got nil")
	}

	svc := newFakeContainerService()
	c := &Codec{containerService: svc}
	if _, _, _, err := c.installContainerAssets(context.Background(), nil, []map[string]any{{"name": "no-id"}}, nil); err == nil {
		t.Fatal("spec without id: want error, got nil")
	}
	if _, _, _, err := c.installContainerAssets(context.Background(), nil, nil, []map[string]any{{"name": "no-id"}}); err == nil {
		t.Fatal("definition without id: want error, got nil")
	}
	if _, _, _, err := c.installContainerAssets(context.Background(), []map[string]any{{"name": "no-id"}}, nil, nil); err == nil {
		t.Fatal("image without id: want error, got nil")
	}
	if len(svc.calls) != 0 {
		t.Fatalf("unexpected calls: %v", svc.calls)
	}
}

// TestInstallContainerAssetsFailsWhenImageMissing 覆盖「绑定行引用的镜像不在导出列表里」：
// 镜像集合缺失时绑定行导入必须失败（保留 service 层不变量），而不是静默写入悬空引用。
func TestInstallContainerAssetsFailsWhenImageMissing(t *testing.T) {
	c := &Codec{containerService: newFakeContainerService()}

	_, _, _, err := c.installContainerAssets(context.Background(),
		nil,
		[]map[string]any{{"id": "300", "name": "spec-300"}},
		[]map[string]any{{"id": "200", "spec_id": "300", "image_id": "999"}},
	)
	if err == nil {
		t.Fatal("definition referencing missing image: want error, got nil")
	}
}
