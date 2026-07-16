package events

import "testing"

func TestOutboxStateTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from OutboxState
		to   OutboxState
		want bool
	}{
		{OutboxPending, OutboxProcessing, true},
		{OutboxPending, OutboxFailed, true},
		{OutboxProcessing, OutboxPending, true},
		{OutboxProcessing, OutboxCompleted, true},
		{OutboxProcessing, OutboxFailed, true},
		{OutboxCompleted, OutboxPending, false},
		{OutboxFailed, OutboxPending, false},
		{OutboxPending, OutboxPending, false},
		{OutboxState("unknown"), OutboxPending, false},
	}
	for _, test := range tests {
		if got := test.from.CanTransitionTo(test.to); got != test.want {
			t.Errorf("%q.CanTransitionTo(%q) = %v, want %v", test.from, test.to, got, test.want)
		}
	}
}
