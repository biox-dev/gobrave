package service

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/biox-dev/gobrave/internal/exportcodec"
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

// TestCollectScriptContainerAssetsDeduplicates 覆盖脚本导出容器资产的三段链路：
// container_template_id（绑定行主键）→ 绑定行 + spec_id 运行配置 + image_id 镜像。
// 三者各自按主键去重，且引用关系（spec_id / image_id）保持原样。
func TestCollectScriptContainerAssetsDeduplicates(t *testing.T) {
	repo := &stubContainerRepo{
		images: map[int64]*types.ContainerImage{
			100: {ID: 100, Name: "rocker/rstudio", FullName: "docker.io/rocker/rstudio:4.4", PullPolicy: types.PullPolicyIfNotPresent},
		},
		specs: map[int64]*types.ContainerTemplateSpec{
			300: {ID: 300, Name: "spec-300", Command: "R -e 1", Port: 8787},
		},
		definitions: map[int64]*types.ContainerTemplateDefinition{
			200: {ID: 200, SpecID: 300, ImageID: 100},
		},
	}
	svc := &workflowService{containerRepo: repo}

	bundle := exportcodec.NewContainerAssetBundle()
	// 两个脚本引用同一绑定行：绑定行 / 运行配置 / 镜像都只导出一份。
	for i := 0; i < 2; i++ {
		if err := svc.collectScriptContainerAssets(context.Background(), 200, bundle); err != nil {
			t.Fatalf("collectScriptContainerAssets: %v", err)
		}
	}

	if len(bundle.Definitions) != 1 || len(bundle.Specs) != 1 || len(bundle.Images) != 1 {
		t.Fatalf("lens = (%d, %d, %d), want (1, 1, 1)", len(bundle.Definitions), len(bundle.Specs), len(bundle.Images))
	}
	definition := bundle.Definitions[0]
	if definition.ID != 200 || definition.SpecID != 300 || definition.ImageID != 100 {
		t.Fatalf("definition refs = %#v", definition)
	}
	if bundle.Specs[0].ID != 300 || bundle.Specs[0].Name != "spec-300" {
		t.Fatalf("spec = %#v", bundle.Specs[0])
	}
	if bundle.Images[0].ID != 100 {
		t.Fatalf("image = %#v", bundle.Images[0])
	}

	// container_template_id 为 0（脚本未绑定模板）与绑定行已删除都应跳过，不报错也不追加。
	if err := svc.collectScriptContainerAssets(context.Background(), 0, bundle); err != nil {
		t.Fatalf("templateID 0 should be skipped: %v", err)
	}
	if err := svc.collectScriptContainerAssets(context.Background(), 999, bundle); err != nil {
		t.Fatalf("missing definition should be skipped: %v", err)
	}
	if len(bundle.Definitions) != 1 || len(bundle.Specs) != 1 || len(bundle.Images) != 1 {
		t.Fatalf("lens = (%d, %d, %d), want (1, 1, 1) after skipped lookups", len(bundle.Definitions), len(bundle.Specs), len(bundle.Images))
	}
}

// TestExportOmitWorkflowScriptFields 保证 workflow / script 对象导出时剔除
// id / project_id / store_id / created_at（以及 updated_at），
// 但保留 script_id（component_id）等业务字段；同时不污染容器资产的 exportOmitFields。
func TestExportOmitWorkflowScriptFields(t *testing.T) {
	now := time.Now()
	script := &types.Script{
		ID:        7,
		StoreID:   8,
		ProjectID: 9,
		ScriptID:  "script-abc",
		CreatedAt: now,
		UpdatedAt: now,
	}

	scriptMap, err := structToMap(script)
	if err != nil {
		t.Fatalf("structToMap: %v", err)
	}
	omitExportFields(scriptMap, exportOmitWorkflowScriptFields)

	for _, field := range []string{"id", "project_id", "store_id", "created_at", "updated_at"} {
		if _, exists := scriptMap[field]; exists {
			t.Fatalf("script export must not contain %q: %#v", field, scriptMap)
		}
	}
	if scriptMap["component_id"] != "script-abc" {
		t.Fatalf("script component_id = %#v, want \"script-abc\"", scriptMap["component_id"])
	}

	// 容器资产仍需保留 id（引用键），因此 exportOmitFields 不能被污染。
	for _, field := range []string{"id", "project_id", "store_id", "created_at"} {
		for _, omitted := range exportOmitFields {
			if omitted == field {
				t.Fatalf("exportOmitFields must not contain %q", field)
			}
		}
	}
}
