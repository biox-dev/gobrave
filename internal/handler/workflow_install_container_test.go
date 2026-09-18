package handler

import (
	"context"
	"fmt"
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

// fakeContainerService 只实现安装容器资产用到的 6 个方法，其余方法由嵌入的（nil）接口提供。
// CreateContainerTemplate 复刻真实 service 层的校验：模板引用的镜像必须已存在，
// 以此验证安装侧「先镜像、后模板」的顺序（镜像缺失时模板导入必须失败）。
type fakeContainerService struct {
	interfaces.ContainerService
	images    map[int64]*types.ContainerImage
	templates map[int64]*types.ContainerTemplate
	calls     []string
}

func newFakeContainerService() *fakeContainerService {
	return &fakeContainerService{
		images:    make(map[int64]*types.ContainerImage),
		templates: make(map[int64]*types.ContainerTemplate),
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

func (f *fakeContainerService) CreateContainerTemplate(_ context.Context, item *types.ContainerTemplate) error {
	if _, ok := f.images[item.ImageID]; !ok {
		return gorm.ErrRecordNotFound
	}
	clone := *item
	f.templates[item.ID] = &clone
	f.calls = append(f.calls, fmt.Sprintf("create-template:%d", item.ID))
	return nil
}

func (f *fakeContainerService) GetContainerTemplateByID(_ context.Context, id int64) (*types.ContainerTemplate, error) {
	item, ok := f.templates[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	clone := *item
	return &clone, nil
}

func (f *fakeContainerService) UpdateContainerTemplate(_ context.Context, item *types.ContainerTemplate) error {
	if _, ok := f.images[item.ImageID]; !ok {
		return gorm.ErrRecordNotFound
	}
	clone := *item
	f.templates[item.ID] = &clone
	f.calls = append(f.calls, fmt.Sprintf("update-template:%d", item.ID))
	return nil
}

// TestInstallContainerAssetsUpsertsByID 覆盖安装侧对 container_images / container_templates 的消费：
// 有 id 且已存在→更新，不存在→新增；模板先于镜像导入时必须失败，顺序正确时成功且引用（image_id）保持。
func TestInstallContainerAssetsUpsertsByID(t *testing.T) {
	svc := newFakeContainerService()
	svc.images[101] = &types.ContainerImage{ID: 101, Name: "old-101", PullPolicy: types.PullPolicyAlways}
	h := &WorkflowHandler{containerService: svc}

	images := []map[string]any{
		{"id": "101", "name": "new-101", "full_name": "docker.io/a/b:1", "pull_policy": types.PullPolicyNever},
		{"id": "100", "name": "new-100", "full_name": "docker.io/a/c:2", "size": float64(42)},
	}
	templates := []map[string]any{
		{"id": "200", "name": "tpl-200", "image_id": "100", "port": float64(8787)},
	}

	imageCount, templateCount, err := h.installContainerAssets(context.Background(), images, templates)
	if err != nil {
		t.Fatalf("installContainerAssets() error = %v", err)
	}
	if imageCount != 2 || templateCount != 1 {
		t.Fatalf("counts = (%d, %d), want (2, 1)", imageCount, templateCount)
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
	if got := svc.templates[200]; got == nil || got.ImageID != 100 || got.Port != 8787 {
		t.Errorf("missing template not created correctly: %+v", got)
	}

	wantCalls := []string{"update-image:101", "create-image:100", "create-template:200"}
	for i, want := range wantCalls {
		if svc.calls[i] != want {
			t.Fatalf("calls = %v, want %v", svc.calls, wantCalls)
		}
	}

	// 再跑一次：镜像与模板都已存在，必须走更新而不是重复新增。
	svc.calls = nil
	if _, _, err := h.installContainerAssets(context.Background(), images, templates); err != nil {
		t.Fatalf("second installContainerAssets() error = %v", err)
	}
	wantSecond := []string{"update-image:101", "update-image:100", "update-template:200"}
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
	// 老产物没有 container_images / container_templates 字段：空列表直接放行，
	// 即使 containerService 未装配也不报错。
	if _, _, err := (&WorkflowHandler{}).installContainerAssets(context.Background(), nil, nil); err != nil {
		t.Fatalf("empty assets error = %v, want nil", err)
	}

	// 有内容但没有 containerService（DI 未装配）时不能静默跳过。
	if _, _, err := (&WorkflowHandler{}).installContainerAssets(context.Background(), nil, []map[string]any{{"id": "1"}}); err == nil {
		t.Fatal("nil container service: want error, got nil")
	}

	svc := newFakeContainerService()
	h := &WorkflowHandler{containerService: svc}
	if _, _, err := h.installContainerAssets(context.Background(), nil, []map[string]any{{"name": "no-id"}}); err == nil {
		t.Fatal("template without id: want error, got nil")
	}
	if _, _, err := h.installContainerAssets(context.Background(), []map[string]any{{"name": "no-id"}}, nil); err == nil {
		t.Fatal("image without id: want error, got nil")
	}
	if len(svc.calls) != 0 {
		t.Fatalf("unexpected calls: %v", svc.calls)
	}
}

// TestInstallContainerAssetsFailsWhenImageMissing 覆盖「模板引用的镜像不在导出列表里」：
// 镜像集合缺失时模板导入必须失败（保留 service 层不变量），而不是静默写入悬空引用。
func TestInstallContainerAssetsFailsWhenImageMissing(t *testing.T) {
	h := &WorkflowHandler{containerService: newFakeContainerService()}

	_, _, err := h.installContainerAssets(context.Background(),
		nil,
		[]map[string]any{{"id": "300", "name": "tpl-300", "image_id": "999"}},
	)
	if err == nil {
		t.Fatal("template referencing missing image: want error, got nil")
	}
}
