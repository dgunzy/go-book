package competition

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubmitResultAuthorizationAndIdempotency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		actor   Actor
		wantErr error
	}{
		{name: "participant", actor: actor("user-a1", "a1", RoleMember)},
		{name: "captain", actor: actor("user-ac", "ac", RoleMember)},
		{name: "admin", actor: actor("admin", "", RoleAdmin)},
		{name: "outsider", actor: actor("outsider", "x", RoleMember), wantErr: ErrUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			match := testMatch(t)
			command := submitCommand("submit-command", "submission", test.actor, sideOneWins())
			events, err := match.SubmitResult(command)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("SubmitResult() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				if match.Snapshot().State != MatchAwaitingResult {
					t.Fatal("rejected command changed match state")
				}
				return
			}
			if len(events) != 1 || events[0].Type != EventMatchResultSubmitted {
				t.Fatalf("SubmitResult() events = %#v", events)
			}
			if replayEvents, replayErr := match.SubmitResult(command); replayErr != nil || len(replayEvents) != 0 {
				t.Fatalf("SubmitResult() replay = (%#v, %v), want no-op", replayEvents, replayErr)
			}
			command.Result.Score = "different"
			if _, err := match.SubmitResult(command); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("changed replay error = %v, want ErrIdempotencyConflict", err)
			}
		})
	}
}

func TestConfirmResultRequiresIndependentOpposingActor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		actor   Actor
		wantErr error
	}{
		{name: "opposing participant", actor: actor("user-b1", "b1", RoleMember)},
		{name: "opposing captain", actor: actor("user-bc", "bc", RoleMember)},
		{name: "submitter", actor: actor("user-a1", "a1", RoleMember), wantErr: ErrOwnSubmission},
		{name: "same side teammate", actor: actor("user-a2", "a2", RoleMember), wantErr: ErrUnauthorized},
		{name: "same side captain", actor: actor("user-ac", "ac", RoleMember), wantErr: ErrNotOpposingSide},
		{name: "outsider", actor: actor("outsider", "x", RoleMember), wantErr: ErrUnauthorized},
		{name: "unassigned admin uses override path", actor: actor("admin", "", RoleAdmin), wantErr: ErrUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			match := submittedMatch(t)
			command := confirmCommand("confirm-command", "confirmation", "verification", test.actor)
			events, err := match.ConfirmResult(command)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ConfirmResult() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				if len(match.Snapshot().VerifiedResults) != 0 {
					t.Fatal("rejected confirmation verified result")
				}
				return
			}
			if len(events) != 1 || events[0].Type != EventMatchResultVerified {
				t.Fatalf("ConfirmResult() events = %#v", events)
			}
			snapshot := match.Snapshot()
			if snapshot.State != MatchVerified || len(snapshot.VerifiedResults) != 1 || snapshot.VerifiedResults[0].Method != VerificationOpponent {
				t.Fatalf("verified snapshot = %#v", snapshot)
			}
			if replay, err := match.ConfirmResult(command); err != nil || len(replay) != 0 {
				t.Fatalf("ConfirmResult() replay = (%#v, %v), want no-op", replay, err)
			}
		})
	}
}

func TestConflictingSubmissionDisputesWithoutVerification(t *testing.T) {
	t.Parallel()

	match := submittedMatch(t)
	events, err := match.SubmitResult(submitCommand(
		"conflicting-command", "conflicting-submission",
		actor("user-b1", "b1", RoleMember),
		Result{Outcome: OutcomeSideWin, WinningSideID: "side-b", Score: "1 up"},
	))
	if err != nil {
		t.Fatalf("SubmitResult(conflict) error = %v", err)
	}
	if len(events) != 2 || events[0].Type != EventMatchResultSubmitted || events[1].Type != EventMatchResultDisputed {
		t.Fatalf("conflicting submission events = %#v", events)
	}
	snapshot := match.Snapshot()
	if snapshot.State != MatchDisputed || len(snapshot.VerifiedResults) != 0 {
		t.Fatalf("disputed snapshot = %#v", snapshot)
	}
	for _, submission := range snapshot.Submissions {
		if submission.State != SubmissionDisputed {
			t.Fatalf("submission %s state = %s, want disputed", submission.ID, submission.State)
		}
	}
	if _, err := match.ConfirmResult(confirmCommand("late-confirm", "late-c", "late-v", actor("user-b1", "b1", RoleMember))); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ConfirmResult(disputed) error = %v, want ErrInvalidTransition", err)
	}
}

func TestDisputeAndAdminOverride(t *testing.T) {
	t.Parallel()

	match := submittedMatch(t)
	_, err := match.DisputeResult(DisputeResultCommand{
		CommandID: "dispute", ConfirmationID: "decision", SubmissionID: "submission",
		Actor: actor("user-bc", "bc", RoleMember), Reason: "The score was 2 up", OccurredAt: testTime().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("DisputeResult() error = %v", err)
	}
	if snapshot := match.Snapshot(); snapshot.State != MatchDisputed || len(snapshot.VerifiedResults) != 0 {
		t.Fatalf("after dispute = %#v", snapshot)
	}

	override := AdminOverrideCommand{
		CommandID: "override", VerificationID: "verified-admin", Actor: actor("admin", "", RoleAdmin),
		Result: sideOneWins(), Reason: "Reviewed signed scorecard", OccurredAt: testTime().Add(2 * time.Minute),
	}
	events, err := match.AdminOverride(override)
	if err != nil {
		t.Fatalf("AdminOverride() error = %v", err)
	}
	if len(events) != 1 || events[0].Type != EventMatchResultVerified || events[0].Reason == "" {
		t.Fatalf("AdminOverride() events = %#v", events)
	}
	snapshot := match.Snapshot()
	if snapshot.State != MatchVerified || len(snapshot.VerifiedResults) != 1 || snapshot.VerifiedResults[0].Method != VerificationAdmin {
		t.Fatalf("after override = %#v", snapshot)
	}
}

func TestAdminOverrideRequiresPrivilegeAndReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		actor   Actor
		reason  string
		wantErr error
	}{
		{name: "member cannot override", actor: actor("user-a1", "a1", RoleMember), reason: "because", wantErr: ErrUnauthorized},
		{name: "admin reason required", actor: actor("admin", "", RoleAdmin), wantErr: ErrReasonRequired},
		{name: "owner can override", actor: actor("owner", "", RoleOwner), reason: "scorecard reviewed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			match := testMatch(t)
			_, err := match.AdminOverride(AdminOverrideCommand{
				CommandID: "override", VerificationID: "verification", Actor: test.actor,
				Result: sideOneWins(), Reason: test.reason, OccurredAt: testTime(),
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("AdminOverride() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestCorrectResultAppendsSupersedingVersion(t *testing.T) {
	t.Parallel()

	match := verifiedMatch(t)
	corrected := Result{Outcome: OutcomeSideWin, WinningSideID: "side-b", Score: "2 & 1"}
	events, err := match.CorrectResult(CorrectResultCommand{
		CommandID: "correct", VerificationID: "verification-2", Actor: actor("owner", "", RoleOwner),
		Result: corrected, Reason: "Scorecard transcription corrected", OccurredAt: testTime().Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CorrectResult() error = %v", err)
	}
	if len(events) != 1 || events[0].Type != EventMatchResultCorrected || events[0].PreviousVerificationID != "verification" {
		t.Fatalf("CorrectResult() events = %#v", events)
	}
	results := match.Snapshot().VerifiedResults
	if len(results) != 2 || results[0].Result.WinningSideID != "side-a" || results[1].Version != 2 || results[1].SupersedesID != results[0].ID || !results[1].Result.equal(corrected) {
		t.Fatalf("verified result history = %#v", results)
	}

	_, err = match.CorrectResult(CorrectResultCommand{
		CommandID: "unchanged", VerificationID: "verification-3", Actor: actor("owner", "", RoleOwner),
		Result: corrected, Reason: "No actual change", OccurredAt: testTime().Add(3 * time.Minute),
	})
	if !errors.Is(err, ErrResultUnchanged) {
		t.Fatalf("unchanged correction error = %v, want ErrResultUnchanged", err)
	}
}

func TestConcurrentConfirmationsProduceOneVerification(t *testing.T) {
	t.Parallel()

	match := submittedMatch(t)
	commands := []ConfirmResultCommand{
		confirmCommand("confirm-b1", "confirmation-b1", "verification-b1", actor("user-b1", "b1", RoleMember)),
		confirmCommand("confirm-bc", "confirmation-bc", "verification-bc", actor("user-bc", "bc", RoleMember)),
	}
	start := make(chan struct{})
	var successful atomic.Int32
	var unexpected atomic.Int32
	var wait sync.WaitGroup
	for _, command := range commands {
		wait.Add(1)
		go func(command ConfirmResultCommand) {
			defer wait.Done()
			<-start
			_, err := match.ConfirmResult(command)
			switch {
			case err == nil:
				successful.Add(1)
			case errors.Is(err, ErrInvalidTransition):
			default:
				unexpected.Add(1)
			}
		}(command)
	}
	close(start)
	wait.Wait()

	if successful.Load() != 1 || unexpected.Load() != 0 {
		t.Fatalf("successful = %d, unexpected = %d; want 1, 0", successful.Load(), unexpected.Load())
	}
	snapshot := match.Snapshot()
	if len(snapshot.Confirmations) != 1 || len(snapshot.VerifiedResults) != 1 || snapshot.State != MatchVerified {
		t.Fatalf("concurrent confirmation snapshot = %#v", snapshot)
	}
}

func testTeams(t *testing.T) (Team, Team) {
	t.Helper()
	teamOne, err := NewTeam("team-a", "event", "Team A", []ID{"a1", "a2", "ac"}, []ID{"ac"})
	if err != nil {
		t.Fatal(err)
	}
	teamTwo, err := NewTeam("team-b", "event", "Team B", []ID{"b1", "b2", "bc"}, []ID{"bc"})
	if err != nil {
		t.Fatal(err)
	}
	return teamOne, teamTwo
}

func testMatch(t *testing.T) *Match {
	t.Helper()
	teamOne, teamTwo := testTeams(t)
	match, err := NewMatch(MatchSpec{
		ID: "match", EventID: "event", Format: FormatSingles,
		SideOne:   MatchSide{ID: "side-a", TeamID: teamOne.ID, Participants: []ID{"a1"}},
		SideTwo:   MatchSide{ID: "side-b", TeamID: teamTwo.ID, Participants: []ID{"b1"}},
		Scheduled: testTime(),
	}, teamOne, teamTwo)
	if err != nil {
		t.Fatal(err)
	}
	return match
}

func submittedMatch(t *testing.T) *Match {
	t.Helper()
	match := testMatch(t)
	if _, err := match.SubmitResult(submitCommand("submit", "submission", actor("user-a1", "a1", RoleMember), sideOneWins())); err != nil {
		t.Fatal(err)
	}
	return match
}

func verifiedMatch(t *testing.T) *Match {
	t.Helper()
	match := submittedMatch(t)
	if _, err := match.ConfirmResult(confirmCommand("confirm", "confirmation", "verification", actor("user-b1", "b1", RoleMember))); err != nil {
		t.Fatal(err)
	}
	return match
}

func actor(userID, playerID ID, role Role) Actor {
	return Actor{UserID: userID, PlayerID: playerID, Role: role}
}

func submitCommand(commandID, submissionID ID, submitter Actor, result Result) SubmitResultCommand {
	return SubmitResultCommand{
		CommandID: commandID, SubmissionID: submissionID, Actor: submitter,
		Result: result, OccurredAt: testTime(),
	}
}

func confirmCommand(commandID, confirmationID, verificationID ID, confirmer Actor) ConfirmResultCommand {
	return ConfirmResultCommand{
		CommandID: commandID, ConfirmationID: confirmationID, VerificationID: verificationID,
		SubmissionID: "submission", Actor: confirmer, OccurredAt: testTime().Add(time.Minute),
	}
}

func sideOneWins() Result {
	return Result{Outcome: OutcomeSideWin, WinningSideID: "side-a", Score: "2 & 1"}
}

func testTime() time.Time {
	return time.Date(2027, time.May, 10, 14, 0, 0, 0, time.UTC)
}
