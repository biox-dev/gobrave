package service

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// stubContainerRepo 只实现工作流导出用到的镜像 / 运行配置 / 绑定行查询；
// 其余方法由嵌入的（nil）接口提供。
type stubContainerRepo struct {
	interfaces.ContainerRepository
	images      map[int64]*types.ContainerImage
	specs       map[int64]*types.ContainerTemplateSpec
	definitions map[int64]*types.ContainerTemplateDefinition
	calls       int
}

func (s *stubContainerRepo) GetContainerImageByID(_ context.Context, id int64) (*types.ContainerImage, error) {
	s.calls++
	image, ok := s.images[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return image, nil
}

func (s *stubContainerRepo) GetContainerTemplateSpecByID(_ context.Context, id int64) (*types.ContainerTemplateSpec, error) {
	s.calls++
	spec, ok := s.specs[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return spec, nil
}

func (s *stubContainerRepo) GetContainerTemplateDefinitionByID(_ context.Context, id int64) (*types.ContainerTemplateDefinition, error) {
	s.calls++
	definition, ok := s.definitions[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return definition, nil
}

// TestBuildContainerImageExportMapOmitsTimestamps 保证镜像导出不含 updated_at（以及 created_at），
// 与模板 / 脚本导出口径一致，否则每次保存都会产生无意义的 git diff。
func TestBuildContainerImageExportMapOmitsTimestamps(t *testing.T) {
	now := time.Now()
	image := &types.ContainerImage{
		ID:          1,
		Name:        "rocker/rstudio",
		FullName:    "docker.io/rocker/rstudio:4.4",
		Description: "RStudio",
		Size:        1024,
		PullPolicy:  types.PullPolicyIfNotPresent,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	imageMap, err := buildContainerImageExportMap(image)
	if err != nil {
		t.Fatalf("buildContainerImageExportMap: %v", err)
	}

	for _, field := range exportOmitFields {
		if _, exists := imageMap[field]; exists {
			t.Fatalf("image export must not contain %q: %#v", field, imageMap)
		}
	}
	if _, exists := imageMap["created_at"]; exists {
		t.Fatalf("image export must not contain created_at: %#v", imageMap)
	}
	// id 是字符串（json:",string"），与其他主键字段的导出口径一致。
	if imageMap["id"] != "1" {
		t.Fatalf("image id = %#v, want \"1\"", imageMap["id"])
	}
	if imageMap["full_name"] != "docker.io/rocker/rstudio:4.4" {
		t.Fatalf("image full_name = %#v", imageMap["full_name"])
	}
}

// TestCollectContainerImageExportMapDeduplicates 覆盖 container_images 的去重与容错：
// 同一镜像只导出一次、只查一次库；imageID 为 0 或镜像不存在时静默跳过。
func TestCollectContainerImageExportMapDeduplicates(t *testing.T) {
	repo := &stubContainerRepo{images: map[int64]*types.ContainerImage{
		1: {
			ID:         1,
			Name:       "rocker/rstudio",
			FullName:   "docker.io/rocker/rstudio:4.4",
			PullPolicy: types.PullPolicyIfNotPresent,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		},
	}}
	svc := &workflowService{containerRepo: repo}

	images := make([]map[string]any, 0)
	seenImageIDs := make(map[int64]struct{})
	// 两个模板引用同一镜像：只应出现一次镜像，且只查一次库。
	for i := 0; i < 2; i++ {
		if err := svc.collectContainerImageExportMap(context.Background(), 1, &images, seenImageIDs); err != nil {
			t.Fatalf("collectContainerImageExportMap: %v", err)
		}
	}
	if len(images) != 1 {
		t.Fatalf("images len = %d, want 1 (dedup by image id)", len(images))
	}
	if repo.calls != 1 {
		t.Fatalf("GetContainerImageByID calls = %d, want 1", repo.calls)
	}

	// imageID 为 0（无模板/无镜像）与镜像被删除都应跳过，不报错也不追加。
	if err := svc.collectContainerImageExportMap(context.Background(), 0, &images, seenImageIDs); err != nil {
		t.Fatalf("imageID 0 should be skipped: %v", err)
	}
	if err := svc.collectContainerImageExportMap(context.Background(), 99, &images, seenImageIDs); err != nil {
		t.Fatalf("missing image should be skipped: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("images len = %d, want 1 after skipped lookups", len(images))
	}
}

// TestCollectScriptContainerAssetsDeduplicates 覆盖脚本导出容器资产的三段链路：
// container_template_id（绑定行主键）→ 绑定行 + spec_id 运行配置 + image_id 镜像。
// 三者各自按主键去重，且导出 map 里的引用关系（spec_id / image_id）保持原样。
func TestCollectScriptContainerAssetsDeduplicates(t *testing.T) {
	repo := &stubContainerRepo{
		images: map[int64]*types.ContainerImage{
			100: {ID: 100, Name: "rocker/rstudio", FullName: "docker.io/rocker/rstudio:4.4", PullPolicy: types.PullPolicyIfNotPresent},
		},
		specs: map[int64]*types.ContainerTemplateSpec{
			300: {ID: 300, Name: "spec-300", Command: "R -e 1", Port: 8787},
		},
		definitions: map[int64]*types.ContainerTemplateDefinition{
			200: {ID: 200, SpecID: 300, ImageID: 100, RLibraryPath: "/lib/R"},
		},
	}
	svc := &workflowService{containerRepo: repo}

	specs := make([]map[string]any, 0)
	definitions := make([]map[string]any, 0)
	images := make([]map[string]any, 0)
	seenSpecIDs := make(map[int64]struct{})
	seenDefinitionIDs := make(map[int64]struct{})
	seenImageIDs := make(map[int64]struct{})

	// 两个脚本引用同一绑定行：绑定行 / 运行配置 / 镜像都只导出一份。
	for i := 0; i < 2; i++ {
		if err := svc.collectScriptContainerAssets(context.Background(), 200, &specs, &definitions, &images, seenSpecIDs, seenDefinitionIDs, seenImageIDs); err != nil {
			t.Fatalf("collectScriptContainerAssets: %v", err)
		}
	}

	if len(definitions) != 1 || len(specs) != 1 || len(images) != 1 {
		t.Fatalf("lens = (%d, %d, %d), want (1, 1, 1)", len(definitions), len(specs), len(images))
	}
	if definitions[0]["id"] != "200" || definitions[0]["spec_id"] != "300" || definitions[0]["image_id"] != "100" {
		t.Fatalf("definition refs = %#v", definitions[0])
	}
	if specs[0]["id"] != "300" || specs[0]["name"] != "spec-300" {
		t.Fatalf("spec = %#v", specs[0])
	}
	if images[0]["id"] != "100" {
		t.Fatalf("image = %#v", images[0])
	}

	// container_template_id 为 0（脚本未绑定模板）与绑定行已删除都应跳过，不报错也不追加。
	if err := svc.collectScriptContainerAssets(context.Background(), 0, &specs, &definitions, &images, seenSpecIDs, seenDefinitionIDs, seenImageIDs); err != nil {
		t.Fatalf("templateID 0 should be skipped: %v", err)
	}
	if err := svc.collectScriptContainerAssets(context.Background(), 999, &specs, &definitions, &images, seenSpecIDs, seenDefinitionIDs, seenImageIDs); err != nil {
		t.Fatalf("missing definition should be skipped: %v", err)
	}
	if len(definitions) != 1 || len(specs) != 1 || len(images) != 1 {
		t.Fatalf("lens = (%d, %d, %d), want (1, 1, 1) after skipped lookups", len(definitions), len(specs), len(images))
	}
}
