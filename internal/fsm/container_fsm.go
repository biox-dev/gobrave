package fsm

import (
	"errors"
)

type State string

const (
	Pending       State = "create_pending"
	Creating      State = "creating"
	Running       State = "running"
	Paused        State = "paused"
	StopPending   State = "stop_pending"
	Stopping      State = "stopping"
	StartPending  State = "start_pending"
	Starting      State = "starting"
	DeletePending State = "delete_pending"
	Deleting      State = "deleting"
	Failed        State = "failed"
	Stopped       State = "stopped"
)

type FSM struct{}

func (f *FSM) Transition(
	from State,
	to State,
) error {

	switch from {

	case Pending:
		if to == Creating || to == Failed || to == DeletePending {
			return nil
		}

		// 如果容器运行太快，ContainerStarted与 ContainerExited可能会竞争
	case Creating:
		if to == Running || to == Failed || to == Stopped || to == DeletePending {
			return nil
		}

	case Running:
		if to == Stopped || to == StopPending || to == StartPending || to == DeletePending || to == Paused || to == Failed {
			return nil
		}

	case Paused:
		if to == Running || to == Stopped || to == StopPending || to == StartPending || to == DeletePending || to == Failed {
			return nil
		}

	case StopPending:
		if to == Stopping || to == DeletePending || to == Failed {
			return nil
		}

	case Stopping:
		if to == Stopped || to == DeletePending || to == Failed {
			return nil
		}

	case Stopped:
		if to == Running || to == StartPending || to == DeletePending {
			return nil
		}

	case StartPending:
		if to == Starting || to == Failed || to == StopPending || to == DeletePending {
			return nil
		}

	case Starting:
		if to == Running || to == Failed || to == DeletePending || to == StopPending {
			return nil
		}

	case DeletePending:
		if to == Deleting || to == Failed {
			return nil
		}

	case Deleting:
		if to == Stopped || to == Failed {
			return nil
		}

	case Failed:
		if to == DeletePending || to == Running || to == Stopped {
			return nil
		}
	}

	return errors.New("invalid transition")
}
