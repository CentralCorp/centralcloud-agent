package domain

import "testing"

func TestTransitions(t *testing.T) {
	if !CanTransition(StatePending, StateCreatingDatabase) {
		t.Fatal("creation transition rejected")
	}
	if CanTransition(StateActive, StatePending) {
		t.Fatal("invalid transition accepted")
	}
	if ValidateTransition(StateStopped, StateStarting) != nil {
		t.Fatal("start transition rejected")
	}
}
