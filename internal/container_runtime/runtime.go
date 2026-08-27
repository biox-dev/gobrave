package containerruntime

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

type Runtime interface {
	Name() string

	Create(ctx context.Context, spec *types.ContainerSpec) (string, error)

	Start(ctx context.Context, runtimeID string) error

	Stop(ctx context.Context, runtimeID string) error

	Pause(ctx context.Context, runtimeID string) error

	Resume(ctx context.Context, runtimeID string) error

	Delete(ctx context.Context, runtimeID string) error

	Logs(ctx context.Context, runtimeID string, tail int) (string, error)

	SetEventHandler(handler RuntimeEventHandler)

	Exec(ctx context.Context, runtimeID string, cmd []string) (string, error)
}

// RuntimeMonitor is an optional extension interface for reconnecting runtime
// lifecycle monitoring after process restarts.
type RuntimeMonitor interface {
	Monitor(ctx context.Context, runtimeID string) error
}

// RuntimeImageManager is an optional extension interface for runtime-side
// image lifecycle operations used by ImageManager.
type RuntimeImageManager interface {
	EnsureImage(ctx context.Context, image string, pullPolicy string) error
}

// RuntimeInspection carries runtime-specific inspect data used by manager/service.
type RuntimeInspection struct {
	IPAddress string
	NodeName  string
}

// RuntimeInspector is an optional extension interface. Implementations can expose
// inspect metadata (such as container internal IP) without forcing all runtimes.
type RuntimeInspector interface {
	Inspect(ctx context.Context, runtimeID string) (*RuntimeInspection, error)
}

type RuntimeEvent struct {
	Type      string
	RuntimeID string
	Message   string
}

type RuntimeEventHandler interface {
	OnEvent(event RuntimeEvent)
}
