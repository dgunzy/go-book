// Package competition contains the pure domain model for Cabot Cup events,
// teams, matches, and verified results. It deliberately has no SQL, HTTP, or
// identity-provider dependencies.
package competition

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

type ID string

func validID(id ID) bool { return strings.TrimSpace(string(id)) != "" }

type Role string

const (
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
	RoleOwner  Role = "owner"
)

func (r Role) valid() bool      { return r == RoleMember || r == RoleAdmin || r == RoleOwner }
func (r Role) privileged() bool { return r == RoleAdmin || r == RoleOwner }

// Actor is the already-authenticated domain identity supplied by the
// application service. PlayerID is optional for administrators acting only in
// their privileged role.
type Actor struct {
	UserID   ID
	PlayerID ID
	Role     Role
}

func (a Actor) validate() error {
	if !validID(a.UserID) || !a.Role.valid() {
		return invalidf("actor must have a user ID and valid role")
	}
	return nil
}

type Player struct {
	ID          ID
	LinkedUser  ID
	DisplayName string
}

func NewPlayer(id ID, displayName string, linkedUser ID) (Player, error) {
	displayName = strings.TrimSpace(displayName)
	if !validID(id) || displayName == "" {
		return Player{}, invalidf("player must have an ID and display name")
	}
	return Player{ID: id, LinkedUser: linkedUser, DisplayName: displayName}, nil
}

type EventState string

const (
	EventScheduled EventState = "scheduled"
	EventActive    EventState = "active"
	EventCompleted EventState = "completed"
)

// CompetitionEvent represents one edition of the Cabot Cup.
type CompetitionEvent struct {
	ID       ID
	Name     string
	Venue    string
	StartsAt time.Time
	EndsAt   time.Time
	State    EventState
}

func NewCompetitionEvent(id ID, name, venue string, startsAt, endsAt time.Time) (CompetitionEvent, error) {
	name, venue = strings.TrimSpace(name), strings.TrimSpace(venue)
	if !validID(id) || name == "" || venue == "" || startsAt.IsZero() || endsAt.IsZero() || endsAt.Before(startsAt) {
		return CompetitionEvent{}, invalidf("event requires an ID, name, venue, and valid date range")
	}
	return CompetitionEvent{
		ID: id, Name: name, Venue: venue,
		StartsAt: startsAt.UTC(), EndsAt: endsAt.UTC(), State: EventScheduled,
	}, nil
}

func (e CompetitionEvent) Transition(to EventState) (CompetitionEvent, error) {
	allowed := (e.State == EventScheduled && to == EventActive) ||
		(e.State == EventActive && to == EventCompleted)
	if !allowed {
		return e, &TransitionError{Operation: fmt.Sprintf("transition event to %s", to), State: string(e.State)}
	}
	e.State = to
	return e, nil
}

type Team struct {
	ID         ID
	EventID    ID
	Name       string
	Members    []ID
	CaptainIDs []ID
}

func NewTeam(id, eventID ID, name string, members, captains []ID) (Team, error) {
	name = strings.TrimSpace(name)
	if !validID(id) || !validID(eventID) || name == "" || len(members) == 0 {
		return Team{}, invalidf("team requires IDs, a name, and at least one member")
	}
	memberSet := make(map[ID]struct{}, len(members))
	for _, member := range members {
		if !validID(member) {
			return Team{}, invalidf("team member ID cannot be empty")
		}
		if _, exists := memberSet[member]; exists {
			return Team{}, invalidf("player %q appears more than once on team", member)
		}
		memberSet[member] = struct{}{}
	}
	if len(captains) == 0 {
		return Team{}, invalidf("team requires at least one captain")
	}
	captainSet := make(map[ID]struct{}, len(captains))
	for _, captain := range captains {
		if _, exists := memberSet[captain]; !exists {
			return Team{}, invalidf("captain %q is not a team member", captain)
		}
		if _, exists := captainSet[captain]; exists {
			return Team{}, invalidf("captain %q appears more than once", captain)
		}
		captainSet[captain] = struct{}{}
	}
	return Team{
		ID: id, EventID: eventID, Name: name,
		Members: slices.Clone(members), CaptainIDs: slices.Clone(captains),
	}, nil
}

func (t Team) hasMember(playerID ID) bool  { return slices.Contains(t.Members, playerID) }
func (t Team) hasCaptain(playerID ID) bool { return slices.Contains(t.CaptainIDs, playerID) }

type MatchFormat string

const (
	FormatSingles MatchFormat = "singles"
	FormatDoubles MatchFormat = "doubles"
)

func (f MatchFormat) participantsPerSide() int {
	switch f {
	case FormatSingles:
		return 1
	case FormatDoubles:
		return 2
	default:
		return 0
	}
}

type MatchSide struct {
	ID           ID
	TeamID       ID
	Participants []ID
}

type MatchSpec struct {
	ID        ID
	EventID   ID
	Format    MatchFormat
	SideOne   MatchSide
	SideTwo   MatchSide
	Scheduled time.Time
}

type ResultOutcome string

const (
	OutcomeSideWin ResultOutcome = "side_win"
	OutcomeTie     ResultOutcome = "tie"
)

// Result is the immutable semantic outcome proposed for a match. Score is a
// display value (for example "2 & 1") and is compared after whitespace trim.
type Result struct {
	Outcome       ResultOutcome
	WinningSideID ID
	Score         string
}

func (r Result) normalized() Result {
	r.Score = strings.TrimSpace(r.Score)
	return r
}

func (r Result) validate(sideOne, sideTwo ID) error {
	r = r.normalized()
	switch r.Outcome {
	case OutcomeSideWin:
		if r.WinningSideID != sideOne && r.WinningSideID != sideTwo {
			return invalidf("winning side must belong to the match")
		}
	case OutcomeTie:
		if validID(r.WinningSideID) {
			return invalidf("a tied result cannot have a winning side")
		}
	default:
		return invalidf("result outcome %q is not supported", r.Outcome)
	}
	return nil
}

func (r Result) equal(other Result) bool {
	r, other = r.normalized(), other.normalized()
	return r == other
}
