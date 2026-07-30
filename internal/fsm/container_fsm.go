package fsm

import (
	"errors"
)

type State string

const (
	Pending  State = "pending"
	Creating State = "creating"
	Running  State = "running"
	Paused   State = "paused"
	Failed   State = "failed"
	Stopped  State = "stopped"
)

type FSM struct{}

func (f *FSM) Transition(
	from State,
	to State,
) error {

	switch from {

	case Pending:
		if to == Creating || to == Failed {
			return nil
		}

		// 如果容器运行太快，ContainerStarted与 ContainerExited可能会竞争
	case Creating:
		if to == Running || to == Failed || to == Stopped {
			return nil
		}

	case Running:
		if to == Stopped || to == Paused || to == Failed {
			return nil
		}

	case Paused:
		if to == Running || to == Stopped || to == Failed {
			return nil
		}

	case Stopped:
		if to == Running {
			return nil
		}
	}

	return errors.New("invalid transition")
}
