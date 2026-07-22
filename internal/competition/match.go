package competition

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

type MatchState string

const (
	MatchAwaitingResult       MatchState = "awaiting_result"
	MatchAwaitingConfirmation MatchState = "awaiting_confirmation"
	MatchDisputed             MatchState = "disputed"
	MatchVerified             MatchState = "verified"
)

type SubmissionState string

const (
	SubmissionPending    SubmissionState = "pending"
	SubmissionConfirmed  SubmissionState = "confirmed"
	SubmissionDisputed   SubmissionState = "disputed"
	SubmissionSuperseded SubmissionState = "superseded"
)

type ConfirmationDecision string

const (
	DecisionConfirmed ConfirmationDecision = "confirmed"
	DecisionDisputed  ConfirmationDecision = "disputed"
)

type VerificationMethod string

const (
	VerificationOpponent   VerificationMethod = "opponent_confirmation"
	VerificationAdmin      VerificationMethod = "admin_override"
	VerificationCorrection VerificationMethod = "admin_correction"
)

type ResultSubmission struct {
	ID              ID
	SubmitterUser   ID
	SubmitterPlayer ID
	SubmitterSide   ID
	Result          Result
	State           SubmissionState
	SubmittedAt     time.Time
}

type ResultConfirmation struct {
	ID           ID
	SubmissionID ID
	ActorUserID  ID
	ActorSideID  ID
	Decision     ConfirmationDecision
	Reason       string
	DecidedAt    time.Time
}

// VerifiedResult is append-only. A correction appends a new version and links
// it to the previous version instead of replacing the original result.
type VerifiedResult struct {
	ID           ID
	Version      int
	Result       Result
	Method       VerificationMethod
	ActorUserID  ID
	Reason       string
	SubmissionID ID
	SupersedesID ID
	VerifiedAt   time.Time
}

type MatchSnapshot struct {
	Spec            MatchSpec
	State           MatchState
	Submissions     []ResultSubmission
	Confirmations   []ResultConfirmation
	VerifiedResults []VerifiedResult
}

type commandRecord struct {
	fingerprint string
}

// Match is a concurrency-safe in-memory aggregate. Database repositories must
// still use row/version locking when loading and saving it across processes.
type Match struct {
	mu sync.RWMutex

	spec  MatchSpec
	teams [2]Team
	state MatchState

	submissions     []ResultSubmission
	confirmations   []ResultConfirmation
	verifiedResults []VerifiedResult
	commands        map[ID]commandRecord
}

func NewMatch(spec MatchSpec, sideOneTeam, sideTwoTeam Team) (*Match, error) {
	if !validID(spec.ID) || !validID(spec.EventID) || spec.Scheduled.IsZero() {
		return nil, invalidf("match requires IDs and a scheduled time")
	}
	rule, err := ParticipantRuleFor(spec.Format)
	if err != nil {
		return nil, err
	}
	if sideOneTeam.ID != spec.SideOne.TeamID || sideTwoTeam.ID != spec.SideTwo.TeamID || sideOneTeam.ID == sideTwoTeam.ID {
		return nil, invalidf("match sides must reference two distinct supplied teams")
	}
	if sideOneTeam.EventID != spec.EventID || sideTwoTeam.EventID != spec.EventID {
		return nil, invalidf("match teams must belong to the match event")
	}
	if !validID(spec.SideOne.ID) || !validID(spec.SideTwo.ID) || spec.SideOne.ID == spec.SideTwo.ID {
		return nil, invalidf("match requires two distinct side IDs")
	}
	if err := ValidateParticipantCounts(spec.Format, len(spec.SideOne.Participants), len(spec.SideTwo.Participants)); err != nil {
		return nil, err
	}
	seen := make(map[ID]struct{}, rule.MinPerSide*2)
	for _, check := range []struct {
		side MatchSide
		team Team
	}{{spec.SideOne, sideOneTeam}, {spec.SideTwo, sideTwoTeam}} {
		for _, participant := range check.side.Participants {
			if !check.team.hasMember(participant) {
				return nil, invalidf("participant %q is not on team %q", participant, check.team.ID)
			}
			if _, duplicate := seen[participant]; duplicate {
				return nil, invalidf("participant %q appears on both sides", participant)
			}
			seen[participant] = struct{}{}
		}
	}

	spec.Scheduled = spec.Scheduled.UTC()
	spec.SideOne.Participants = slices.Clone(spec.SideOne.Participants)
	spec.SideTwo.Participants = slices.Clone(spec.SideTwo.Participants)
	return &Match{
		spec: spec, teams: [2]Team{sideOneTeam, sideTwoTeam}, state: MatchAwaitingResult,
		commands: make(map[ID]commandRecord),
	}, nil
}

type SubmitResultCommand struct {
	CommandID    ID
	SubmissionID ID
	Actor        Actor
	Result       Result
	OccurredAt   time.Time
}

type ConfirmResultCommand struct {
	CommandID      ID
	ConfirmationID ID
	VerificationID ID
	SubmissionID   ID
	Actor          Actor
	OccurredAt     time.Time
}

type DisputeResultCommand struct {
	CommandID      ID
	ConfirmationID ID
	SubmissionID   ID
	Actor          Actor
	Reason         string
	OccurredAt     time.Time
}

type AdminOverrideCommand struct {
	CommandID      ID
	VerificationID ID
	Actor          Actor
	Result         Result
	Reason         string
	OccurredAt     time.Time
}

type CorrectResultCommand struct {
	CommandID      ID
	VerificationID ID
	Actor          Actor
	Result         Result
	Reason         string
	OccurredAt     time.Time
}

func (m *Match) SubmitResult(command SubmitResultCommand) ([]DomainEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fingerprint := fmt.Sprintf("submit\x00%s\x00%s\x00%s\x00%s\x00%s", command.SubmissionID, command.Actor.UserID, command.Result.Outcome, command.Result.WinningSideID, strings.TrimSpace(command.Result.Score))
	if replay, err := m.checkCommand(command.CommandID, fingerprint); replay || err != nil {
		return nil, err
	}
	if m.state != MatchAwaitingResult && m.state != MatchAwaitingConfirmation {
		return nil, transition("submit result", m.state)
	}
	if err := validateCommand(command.Actor, command.OccurredAt, command.SubmissionID); err != nil {
		return nil, err
	}
	if err := command.Result.validate(m.spec.SideOne.ID, m.spec.SideTwo.ID); err != nil {
		return nil, err
	}
	sideID, eligible := m.actingSide(command.Actor)
	if !eligible && !command.Actor.Role.privileged() {
		return nil, ErrUnauthorized
	}
	if m.submissionByID(command.SubmissionID) != nil {
		return nil, ErrAlreadyExists
	}

	result := command.Result.normalized()
	if m.state == MatchAwaitingConfirmation {
		pending := m.pendingSubmission()
		if pending == nil {
			return nil, transition("submit result", m.state)
		}
		if isSubmitter(command.Actor, pending) {
			return nil, ErrOwnSubmission
		}
		if !eligible || !validID(pending.SubmitterSide) || sideID == pending.SubmitterSide {
			return nil, ErrNotOpposingSide
		}
		if pending.Result.equal(result) {
			return nil, ErrAlreadyExists
		}

		at := command.OccurredAt.UTC()
		pending.State = SubmissionDisputed
		m.submissions = append(m.submissions, ResultSubmission{
			ID: command.SubmissionID, SubmitterUser: command.Actor.UserID, SubmitterPlayer: command.Actor.PlayerID, SubmitterSide: sideID,
			Result: result, State: SubmissionDisputed, SubmittedAt: at,
		})
		m.state = MatchDisputed
		m.recordCommand(command.CommandID, fingerprint)
		return []DomainEvent{
			{
				Type: EventMatchResultSubmitted, MatchID: m.spec.ID, CompetitionEventID: m.spec.EventID,
				SubmissionID: command.SubmissionID, ActorUserID: command.Actor.UserID,
				Result: result, OccurredAt: at,
			},
			{
				Type: EventMatchResultDisputed, MatchID: m.spec.ID, CompetitionEventID: m.spec.EventID,
				SubmissionID: command.SubmissionID, ActorUserID: command.Actor.UserID,
				Result: result, Reason: "conflicting result submissions", OccurredAt: at,
			},
		}, nil
	}

	m.submissions = append(m.submissions, ResultSubmission{
		ID: command.SubmissionID, SubmitterUser: command.Actor.UserID, SubmitterPlayer: command.Actor.PlayerID, SubmitterSide: sideID,
		Result: result, State: SubmissionPending, SubmittedAt: command.OccurredAt.UTC(),
	})
	m.state = MatchAwaitingConfirmation
	m.recordCommand(command.CommandID, fingerprint)
	return []DomainEvent{{
		Type: EventMatchResultSubmitted, MatchID: m.spec.ID, CompetitionEventID: m.spec.EventID,
		SubmissionID: command.SubmissionID, ActorUserID: command.Actor.UserID,
		Result: result, OccurredAt: command.OccurredAt.UTC(),
	}}, nil
}

func (m *Match) ConfirmResult(command ConfirmResultCommand) ([]DomainEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fingerprint := fmt.Sprintf("confirm\x00%s\x00%s\x00%s\x00%s", command.ConfirmationID, command.VerificationID, command.SubmissionID, command.Actor.UserID)
	if replay, err := m.checkCommand(command.CommandID, fingerprint); replay || err != nil {
		return nil, err
	}
	if m.state != MatchAwaitingConfirmation {
		return nil, transition("confirm result", m.state)
	}
	if err := validateCommand(command.Actor, command.OccurredAt, command.ConfirmationID, command.VerificationID, command.SubmissionID); err != nil {
		return nil, err
	}
	submission := m.submissionByID(command.SubmissionID)
	if submission == nil {
		return nil, ErrNotFound
	}
	if isSubmitter(command.Actor, submission) {
		return nil, ErrOwnSubmission
	}
	actorSide, eligible := m.actingSide(command.Actor)
	if !eligible {
		return nil, ErrUnauthorized
	}
	if !validID(submission.SubmitterSide) || actorSide == submission.SubmitterSide {
		return nil, ErrNotOpposingSide
	}
	if m.confirmationByID(command.ConfirmationID) != nil || m.verificationByID(command.VerificationID) != nil {
		return nil, ErrAlreadyExists
	}

	at := command.OccurredAt.UTC()
	m.confirmations = append(m.confirmations, ResultConfirmation{
		ID: command.ConfirmationID, SubmissionID: submission.ID, ActorUserID: command.Actor.UserID,
		ActorSideID: actorSide, Decision: DecisionConfirmed, DecidedAt: at,
	})
	submission.State = SubmissionConfirmed
	m.verifiedResults = append(m.verifiedResults, VerifiedResult{
		ID: command.VerificationID, Version: 1, Result: submission.Result,
		Method: VerificationOpponent, ActorUserID: command.Actor.UserID,
		SubmissionID: submission.ID, VerifiedAt: at,
	})
	m.state = MatchVerified
	m.recordCommand(command.CommandID, fingerprint)
	return []DomainEvent{{
		Type: EventMatchResultVerified, MatchID: m.spec.ID, CompetitionEventID: m.spec.EventID,
		SubmissionID: submission.ID, VerificationID: command.VerificationID,
		ActorUserID: command.Actor.UserID, Result: submission.Result, OccurredAt: at,
	}}, nil
}

func (m *Match) DisputeResult(command DisputeResultCommand) ([]DomainEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	reason := strings.TrimSpace(command.Reason)
	fingerprint := fmt.Sprintf("dispute\x00%s\x00%s\x00%s\x00%s", command.ConfirmationID, command.SubmissionID, command.Actor.UserID, reason)
	if replay, err := m.checkCommand(command.CommandID, fingerprint); replay || err != nil {
		return nil, err
	}
	if m.state != MatchAwaitingConfirmation {
		return nil, transition("dispute result", m.state)
	}
	if err := validateCommand(command.Actor, command.OccurredAt, command.ConfirmationID, command.SubmissionID); err != nil {
		return nil, err
	}
	if reason == "" {
		return nil, ErrReasonRequired
	}
	submission := m.submissionByID(command.SubmissionID)
	if submission == nil {
		return nil, ErrNotFound
	}
	if isSubmitter(command.Actor, submission) {
		return nil, ErrOwnSubmission
	}
	actorSide, eligible := m.actingSide(command.Actor)
	if !eligible {
		return nil, ErrUnauthorized
	}
	if !validID(submission.SubmitterSide) || actorSide == submission.SubmitterSide {
		return nil, ErrNotOpposingSide
	}
	if m.confirmationByID(command.ConfirmationID) != nil {
		return nil, ErrAlreadyExists
	}

	at := command.OccurredAt.UTC()
	m.confirmations = append(m.confirmations, ResultConfirmation{
		ID: command.ConfirmationID, SubmissionID: submission.ID, ActorUserID: command.Actor.UserID,
		ActorSideID: actorSide, Decision: DecisionDisputed, Reason: reason, DecidedAt: at,
	})
	submission.State = SubmissionDisputed
	m.state = MatchDisputed
	m.recordCommand(command.CommandID, fingerprint)
	return []DomainEvent{{
		Type: EventMatchResultDisputed, MatchID: m.spec.ID, CompetitionEventID: m.spec.EventID,
		SubmissionID: submission.ID, ActorUserID: command.Actor.UserID,
		Result: submission.Result, Reason: reason, OccurredAt: at,
	}}, nil
}

func (m *Match) AdminOverride(command AdminOverrideCommand) ([]DomainEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	reason := strings.TrimSpace(command.Reason)
	fingerprint := fmt.Sprintf("override\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", command.VerificationID, command.Actor.UserID, command.Result.Outcome, command.Result.WinningSideID, strings.TrimSpace(command.Result.Score), reason)
	if replay, err := m.checkCommand(command.CommandID, fingerprint); replay || err != nil {
		return nil, err
	}
	if m.state != MatchAwaitingResult && m.state != MatchAwaitingConfirmation && m.state != MatchDisputed {
		return nil, transition("override result", m.state)
	}
	if err := validateCommand(command.Actor, command.OccurredAt, command.VerificationID); err != nil {
		return nil, err
	}
	if !command.Actor.Role.privileged() {
		return nil, ErrUnauthorized
	}
	if reason == "" {
		return nil, ErrReasonRequired
	}
	if err := command.Result.validate(m.spec.SideOne.ID, m.spec.SideTwo.ID); err != nil {
		return nil, err
	}
	if m.verificationByID(command.VerificationID) != nil {
		return nil, ErrAlreadyExists
	}

	for index := range m.submissions {
		if m.submissions[index].State == SubmissionPending {
			m.submissions[index].State = SubmissionSuperseded
		}
	}
	at, result := command.OccurredAt.UTC(), command.Result.normalized()
	m.verifiedResults = append(m.verifiedResults, VerifiedResult{
		ID: command.VerificationID, Version: 1, Result: result, Method: VerificationAdmin,
		ActorUserID: command.Actor.UserID, Reason: reason, VerifiedAt: at,
	})
	m.state = MatchVerified
	m.recordCommand(command.CommandID, fingerprint)
	return []DomainEvent{{
		Type: EventMatchResultVerified, MatchID: m.spec.ID, CompetitionEventID: m.spec.EventID,
		VerificationID: command.VerificationID, ActorUserID: command.Actor.UserID,
		Result: result, Reason: reason, OccurredAt: at,
	}}, nil
}

func (m *Match) CorrectResult(command CorrectResultCommand) ([]DomainEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	reason := strings.TrimSpace(command.Reason)
	fingerprint := fmt.Sprintf("correct\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", command.VerificationID, command.Actor.UserID, command.Result.Outcome, command.Result.WinningSideID, strings.TrimSpace(command.Result.Score), reason)
	if replay, err := m.checkCommand(command.CommandID, fingerprint); replay || err != nil {
		return nil, err
	}
	if m.state != MatchVerified {
		return nil, transition("correct result", m.state)
	}
	if err := validateCommand(command.Actor, command.OccurredAt, command.VerificationID); err != nil {
		return nil, err
	}
	if !command.Actor.Role.privileged() {
		return nil, ErrUnauthorized
	}
	if reason == "" {
		return nil, ErrReasonRequired
	}
	if err := command.Result.validate(m.spec.SideOne.ID, m.spec.SideTwo.ID); err != nil {
		return nil, err
	}
	if m.verificationByID(command.VerificationID) != nil {
		return nil, ErrAlreadyExists
	}
	previous := m.verifiedResults[len(m.verifiedResults)-1]
	result := command.Result.normalized()
	if previous.Result.equal(result) {
		return nil, ErrResultUnchanged
	}

	at := command.OccurredAt.UTC()
	m.verifiedResults = append(m.verifiedResults, VerifiedResult{
		ID: command.VerificationID, Version: previous.Version + 1, Result: result,
		Method: VerificationCorrection, ActorUserID: command.Actor.UserID, Reason: reason,
		SupersedesID: previous.ID, VerifiedAt: at,
	})
	m.recordCommand(command.CommandID, fingerprint)
	return []DomainEvent{{
		Type: EventMatchResultCorrected, MatchID: m.spec.ID, CompetitionEventID: m.spec.EventID,
		VerificationID: command.VerificationID, PreviousVerificationID: previous.ID,
		ActorUserID: command.Actor.UserID, Result: result, Reason: reason, OccurredAt: at,
	}}, nil
}

func (m *Match) Snapshot() MatchSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	spec := m.spec
	spec.SideOne.Participants = slices.Clone(spec.SideOne.Participants)
	spec.SideTwo.Participants = slices.Clone(spec.SideTwo.Participants)
	return MatchSnapshot{
		Spec: spec, State: m.state,
		Submissions: slices.Clone(m.submissions), Confirmations: slices.Clone(m.confirmations),
		VerifiedResults: slices.Clone(m.verifiedResults),
	}
}

func validateCommand(actor Actor, occurredAt time.Time, ids ...ID) error {
	if err := actor.validate(); err != nil {
		return err
	}
	if occurredAt.IsZero() {
		return invalidf("command occurrence time is required")
	}
	for _, id := range ids {
		if !validID(id) {
			return invalidf("command record IDs cannot be empty")
		}
	}
	return nil
}

func transition(operation string, state MatchState) error {
	return &TransitionError{Operation: operation, State: string(state)}
}

func isSubmitter(actor Actor, submission *ResultSubmission) bool {
	return actor.UserID == submission.SubmitterUser ||
		(validID(actor.PlayerID) && actor.PlayerID == submission.SubmitterPlayer)
}

func (m *Match) actingSide(actor Actor) (ID, bool) {
	for index, side := range []MatchSide{m.spec.SideOne, m.spec.SideTwo} {
		if slices.Contains(side.Participants, actor.PlayerID) || m.teams[index].hasCaptain(actor.PlayerID) {
			return side.ID, true
		}
	}
	return "", false
}

func (m *Match) submissionByID(id ID) *ResultSubmission {
	for index := range m.submissions {
		if m.submissions[index].ID == id {
			return &m.submissions[index]
		}
	}
	return nil
}

func (m *Match) pendingSubmission() *ResultSubmission {
	for index := range m.submissions {
		if m.submissions[index].State == SubmissionPending {
			return &m.submissions[index]
		}
	}
	return nil
}

func (m *Match) confirmationByID(id ID) *ResultConfirmation {
	for index := range m.confirmations {
		if m.confirmations[index].ID == id {
			return &m.confirmations[index]
		}
	}
	return nil
}

func (m *Match) verificationByID(id ID) *VerifiedResult {
	for index := range m.verifiedResults {
		if m.verifiedResults[index].ID == id {
			return &m.verifiedResults[index]
		}
	}
	return nil
}

func (m *Match) checkCommand(id ID, fingerprint string) (bool, error) {
	if !validID(id) {
		return false, invalidf("command ID is required")
	}
	previous, exists := m.commands[id]
	if !exists {
		return false, nil
	}
	if previous.fingerprint != fingerprint {
		return false, ErrIdempotencyConflict
	}
	return true, nil
}

func (m *Match) recordCommand(id ID, fingerprint string) {
	m.commands[id] = commandRecord{fingerprint: fingerprint}
}
