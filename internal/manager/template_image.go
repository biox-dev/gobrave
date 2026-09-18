package manager

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// resolveTemplateImage 返回容器模板绑定行所指向的镜像。
//
// 读模型（types.ContainerTemplate）在 repo 组装时已经带出 Image，因此正常情况下不需要
// 再查一次 go_container_image；只有读模型没带出镜像（Image == nil，例如历史数据或
// 手工构造的对象）时才回落到按 ImageID 查询，保证错误信息依然明确。
func resolveTemplateImage(ctx context.Context, repo interfaces.ContainerRepository, tpl *types.ContainerTemplate) (*types.ContainerImage, error) {
	if tpl == nil {
		return nil, fmt.Errorf("container template is required")
	}
	if tpl.Image != nil {
		return tpl.Image, nil
	}
	if tpl.ImageID == 0 {
		return nil, fmt.Errorf("container template %d does not bind any image", tpl.ID)
	}
	return repo.GetContainerImageByID(ctx, tpl.ImageID)
}
