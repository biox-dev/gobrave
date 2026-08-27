package fsm

import (
	"errors"

	"github.com/biox-dev/gobrave/internal/types"
)

// type State string

// const (
// 	Pending       State = "create_pending"
// 	Creating      State = "creating"
// 	Running       State = "running"
// 	Paused        State = "paused"
// 	StopPending   State = "stop_pending"
// 	Stopping      State = "stopping"
// 	StartPending  State = "start_pending"
// 	Starting      State = "starting"
// 	DeletePending State = "delete_pending"
// 	Deleting      State = "deleting"
// 	Failed        State = "failed"
// 	Stopped       State = "stopped"
// )

type FSM struct{}

func (f *FSM) Transition(
	from types.ContainerStatus,
	to types.ContainerStatus,
) error {

	switch from {
	case types.ContainerReCreating:
		if to == types.ContainerCreatePending {
			return nil
		}
	case types.ContainerReCreatePending:
		if to == types.ContainerReCreating {
			return nil
		}
	case types.ContainerCreatePending:
		if to == types.ContainerCreating || to == types.ContainerFailed {
			return nil
		}

		// 如果容器运行太快，ContainerStarted与 ContainerExited可能会竞争
	case types.ContainerCreating:
		if to == types.ContainerRunning || to == types.ContainerFailed || to == types.ContainerStopped || to == types.ContainerDeletePending {
			return nil
		}

	case types.ContainerRunning:
		if to == types.ContainerStopped ||
			to == types.ContainerStopPending ||
			to == types.ContainerStartPending ||
			to == types.ContainerDeletePending ||
			to == types.ContainerReCreatePending ||
			to == types.ContainerPaused ||
			to == types.ContainerFailed {
			return nil
		}

	case types.ContainerPaused:
		if to == types.ContainerRunning || to == types.ContainerStopped || to == types.ContainerStopPending || to == types.ContainerStartPending || to == types.ContainerDeletePending || to == types.ContainerFailed || to == types.ContainerReCreatePending {
			return nil
		}

	case types.ContainerStopPending:
		if to == types.ContainerStopping || to == types.ContainerDeletePending || to == types.ContainerFailed {
			return nil
		}

	case types.ContainerStopping:
		if to == types.ContainerStopped || to == types.ContainerDeletePending || to == types.ContainerFailed || to == types.ContainerDeleted {
			return nil
		}

	case types.ContainerStopped:
		if to == types.ContainerRunning || to == types.ContainerStartPending || to == types.ContainerDeletePending || to == types.ContainerReCreatePending {
			return nil
		}

	case types.ContainerStartPending:
		if to == types.ContainerStarting || to == types.ContainerFailed || to == types.ContainerStopPending || to == types.ContainerDeletePending {
			return nil
		}

	case types.ContainerStarting:
		if to == types.ContainerRunning || to == types.ContainerFailed || to == types.ContainerDeletePending || to == types.ContainerStopPending {
			return nil
		}

	case types.ContainerDeletePending:
		if to == types.ContainerDeleting || to == types.ContainerFailed {
			return nil
		}

	case types.ContainerDeleting:
		if to == types.ContainerStopped || to == types.ContainerFailed || to == types.ContainerDeleted {
			return nil
		}

	case types.ContainerFailed:
		if to == types.ContainerDeletePending || to == types.ContainerRunning || to == types.ContainerStopped || to == types.ContainerReCreatePending {
			return nil
		}
	}

	return errors.New("invalid transition")
}
