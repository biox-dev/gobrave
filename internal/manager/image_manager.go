package manager

import (
	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

type ImageManager struct {
	repo interfaces.ContainerRepository
	reg  *containerruntime.Registry
}

func NewImageManager(repo interfaces.ContainerRepository, reg *containerruntime.Registry) *ImageManager {
	return &ImageManager{repo: repo, reg: reg}
}

// RunImageStatusRefreshOnStart triggers an async image status refresh once at startup.
// func RunImageStatusRefreshOnStart(mgr *ImageManager) {
// 	go func() {
// 		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
// 		defer cancel()

// 		if err := mgr.RefreshAllStatuses(ctx); err != nil {
// 			logger.Warnf(ctx, "[ImageManager] refresh all image statuses failed: %v", err)
// 		}
// 	}()
// }

// func (m *ImageManager) RefreshAllStatuses(ctx context.Context) error {
// 	runtimeName, err := m.defaultRuntimeName()
// 	if err != nil {
// 		return err
// 	}

// 	images, err := m.repo.ListContainerImage(ctx)
// 	if err != nil {
// 		return err
// 	}

// 	var firstErr error
// 	for _, img := range images {
// 		if err := ctx.Err(); err != nil {
// 			return err
// 		}
// 		if img == nil {
// 			continue
// 		}
// 		if img.Status == types.ImageStatusDeleted || img.Status == types.ImageStatusDisabled {
// 			continue
// 		}

// 		if err := m.EnsureImageReadyByEntity(ctx, runtimeName, img); err != nil {
// 			if firstErr == nil {
// 				firstErr = err
// 			}
// 			logger.Warnf(ctx, "[ImageManager] refresh image status failed, image_id=%d image=%s err=%v", img.ID, img.FullName, err)
// 		}
// 	}

// 	return firstErr
// }

// func (m *ImageManager) EnsureImageReady(ctx context.Context, runtimeName string, imageID int64) (*types.ContainerImage, error) {
// 	img, err := m.repo.GetContainerImageByID(ctx, imageID)
// 	if err != nil {
// 		return nil, err
// 	}
// 	if err := m.EnsureImageReadyByEntity(ctx, runtimeName, img); err != nil {
// 		return nil, err
// 	}
// 	return img, nil
// }

// func (m *ImageManager) EnsureImageReadyByEntity(ctx context.Context, runtimeName string, img *types.ContainerImage) error {
// 	if img == nil {
// 		return fmt.Errorf("container image is nil")
// 	}
// 	if strings.TrimSpace(img.FullName) == "" {
// 		return fmt.Errorf("container image full name is required")
// 	}
// 	if img.Status == types.ImageStatusDeleted {
// 		return fmt.Errorf("container image is deleted")
// 	}
// 	if img.Status == types.ImageStatusDisabled {
// 		return fmt.Errorf("container image is disabled")
// 	}

// 	rt, err := m.getRuntimeByName(runtimeName)
// 	if err != nil {
// 		return err
// 	}

// 	imageRuntime, ok := rt.(containerruntime.RuntimeImageManager)
// 	if !ok {
// 		return fmt.Errorf("runtime %s does not support image management", runtimeName)
// 	}

// 	img.Status = types.ImageStatusPulling
// 	img.LastError = ""
// 	if err := m.repo.UpdateContainerImage(ctx, img); err != nil {
// 		return err
// 	}

// 	if err := imageRuntime.EnsureImage(ctx, img.FullName, img.PullPolicy); err != nil {
// 		img.Status = types.ImageStatusFailed
// 		img.LastError = err.Error()
// 		_ = m.repo.UpdateContainerImage(ctx, img)
// 		return err
// 	}

// 	now := time.Now()
// 	img.Status = types.ImageStatusReady
// 	img.LastPullTime = &now
// 	img.LastError = ""
// 	return m.repo.UpdateContainerImage(ctx, img)
// }

// func (m *ImageManager) getRuntimeByName(name string) (containerruntime.Runtime, error) {
// 	name = strings.TrimSpace(name)
// 	if name == "" {
// 		return nil, fmt.Errorf("runtime name is required")
// 	}
// 	if m.reg == nil {
// 		return nil, fmt.Errorf("runtime registry is nil")
// 	}

// 	rt := m.reg.Get(name)
// 	if rt == nil {
// 		return nil, fmt.Errorf("runtime not found: %s", name)
// 	}
// 	return rt, nil
// }

// func (m *ImageManager) defaultRuntimeName() (string, error) {
// 	if m.reg == nil {
// 		return "", fmt.Errorf("runtime registry is nil")
// 	}

// 	if rt := m.reg.Get("docker"); rt != nil {
// 		return rt.Name(), nil
// 	}

// 	items := m.reg.List()
// 	if len(items) == 0 {
// 		return "", fmt.Errorf("runtime not found")
// 	}

// 	names := make([]string, 0, len(items))
// 	for _, item := range items {
// 		if item == nil {
// 			continue
// 		}
// 		name := strings.TrimSpace(item.Name())
// 		if name != "" {
// 			names = append(names, name)
// 		}
// 	}
// 	if len(names) == 0 {
// 		return "", fmt.Errorf("runtime not found")
// 	}

// 	sort.Strings(names)
// 	return names[0], nil
// }
