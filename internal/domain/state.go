package domain

import "fmt"

type State string

const (
	StatePending           State = "pending"
	StateCreatingDatabase  State = "creating_database"
	StatePullingImage      State = "pulling_image"
	StateCreatingContainer State = "creating_container"
	StateStarting          State = "starting"
	StateMigrating         State = "migrating"
	StateHealthchecking    State = "healthchecking"
	StateActive            State = "active"
	StateStopped           State = "stopped"
	StateUpdating          State = "updating"
	StateFailed            State = "failed"
	StateDeleting          State = "deleting"
	StateDeleted           State = "deleted"
)

var transitions = map[State]map[State]bool{
	StatePending:           {StateCreatingDatabase: true, StateFailed: true, StateDeleting: true},
	StateCreatingDatabase:  {StatePullingImage: true, StateFailed: true},
	StatePullingImage:      {StateCreatingContainer: true, StateFailed: true},
	StateCreatingContainer: {StateStarting: true, StateFailed: true},
	StateStarting:          {StateMigrating: true, StateHealthchecking: true, StateFailed: true},
	StateMigrating:         {StateHealthchecking: true, StateFailed: true},
	StateHealthchecking:    {StateActive: true, StateStopped: true, StateFailed: true},
	StateActive:            {StateStopped: true, StateStarting: true, StateUpdating: true, StateDeleting: true, StateFailed: true},
	StateStopped:           {StateStarting: true, StateUpdating: true, StateDeleting: true, StateFailed: true},
	StateUpdating:          {StateActive: true, StateStopped: true, StateFailed: true},
	StateFailed:            {StatePending: true, StateDeleting: true},
	StateDeleting:          {StateDeleted: true, StateFailed: true},
	StateDeleted:           {StatePending: true, StateDeleting: true},
}

func CanTransition(from, to State) bool { return transitions[from][to] }

func ValidateTransition(from, to State) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("invalid deployment transition: %s -> %s", from, to)
	}
	return nil
}
