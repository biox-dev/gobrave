package service

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// stubContainerRepo 只实现工作流导出用到的 GetContainerImageByID；其余方法由嵌入的（nil）接口提供。
type stubContainerRepo struct {
	interfaces.ContainerRepository
	images map[int64]*types.ContainerImage
	calls  int
}

func (s *stubContainerRepo) GetContainerImageByID(_ context.Context, id int64) (*types.ContainerImage, error) {
	s.calls++
	image, ok := s.images[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return image, nil
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
