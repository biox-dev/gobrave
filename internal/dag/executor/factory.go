package executor

import (
	"github.com/gobravedev/gobrave/internal/manager"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
)

type FactoryDeps struct {
	WorkflowRepository interfaces.WorkflowRepository
	ContainerManager   *manager.ContainerManager
}

const (
	executorContainer = "container"
	executorNextflow  = "nextflow"
	executorLocal     = "local"
)

// defaultExecutor is returned when the requested name is empty or unknown.
const defaultExecutor = executorContainer

type ExecuterFactory struct {
	executors map[string]Executor
}

func NewFactory(workflowRepository interfaces.WorkflowRepository,
	containerManager *manager.ContainerManager) *ExecuterFactory {
	local := NewLocalExecutor()
	container := NewContainerExecutor(
		workflowRepository,
		containerManager,
	)
	return &ExecuterFactory{
		executors: map[string]Executor{
			executorContainer: container,
			executorNextflow:  NewNextflowExecutor(),
			executorLocal:     local,
		},
	}
}

func (f *ExecuterFactory) Resolve(executorName string) Executor {
	// name := strings.TrimSpace(strings.ToLower(executorName))
	// if name == "" {
	// 	name = defaultExecutor
	// }
	// if ex, ok := f.executors[name]; ok {
	// 	return ex
	// }
	return f.executors[defaultExecutor]
}
