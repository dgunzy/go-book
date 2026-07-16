package competition

import (
	"errors"
	"testing"
	"time"
)

func TestCompetitionEventTransitions(t *testing.T) {
	t.Parallel()

	starts := time.Date(2027, time.May, 10, 12, 0, 0, 0, time.FixedZone("ADT", -3*60*60))
	event, err := NewCompetitionEvent("event-2027", "2027 Cabot Cup", "Cabot Cape Breton", starts, starts.Add(72*time.Hour))
	if err != nil {
		t.Fatalf("NewCompetitionEvent() error = %v", err)
	}
	if event.State != EventScheduled || event.StartsAt.Location() != time.UTC {
		t.Fatalf("new event = %#v, want scheduled UTC event", event)
	}

	tests := []struct {
		name    string
		from    CompetitionEvent
		to      EventState
		want    EventState
		wantErr bool
	}{
		{name: "activate scheduled", from: event, to: EventActive, want: EventActive},
		{name: "cannot complete scheduled", from: event, to: EventCompleted, wantErr: true},
		{name: "cannot remain scheduled", from: event, to: EventScheduled, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.from.Transition(test.to)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("Transition() error = %v, want ErrInvalidTransition", err)
				}
				return
			}
			if err != nil || got.State != test.want {
				t.Fatalf("Transition() = (%s, %v), want (%s, nil)", got.State, err, test.want)
			}
		})
	}

	active, err := event.Transition(EventActive)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := active.Transition(EventCompleted)
	if err != nil || completed.State != EventCompleted {
		t.Fatalf("complete active event = (%s, %v)", completed.State, err)
	}
}

func TestNewTeamValidatesCaptainsAndRoster(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		members  []ID
		captains []ID
		wantErr  bool
	}{
		{name: "captain is member", members: []ID{"p1", "p2"}, captains: []ID{"p2"}},
		{name: "captain outside roster", members: []ID{"p1"}, captains: []ID{"p2"}, wantErr: true},
		{name: "duplicate member", members: []ID{"p1", "p1"}, captains: []ID{"p1"}, wantErr: true},
		{name: "no captain", members: []ID{"p1"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewTeam("team", "event", "Team", test.members, test.captains)
			if (err != nil) != test.wantErr {
				t.Fatalf("NewTeam() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestNewMatchValidatesFormatAndParticipants(t *testing.T) {
	t.Parallel()

	teamOne, teamTwo := testTeams(t)
	base := MatchSpec{
		ID: "match", EventID: "event", Format: FormatSingles,
		SideOne:   MatchSide{ID: "side-a", TeamID: teamOne.ID, Participants: []ID{"a1"}},
		SideTwo:   MatchSide{ID: "side-b", TeamID: teamTwo.ID, Participants: []ID{"b1"}},
		Scheduled: testTime(),
	}
	tests := []struct {
		name    string
		mutate  func(*MatchSpec)
		wantErr bool
	}{
		{name: "valid singles"},
		{name: "valid doubles", mutate: func(spec *MatchSpec) {
			spec.Format = FormatDoubles
			spec.SideOne.Participants = []ID{"a1", "a2"}
			spec.SideTwo.Participants = []ID{"b1", "b2"}
		}},
		{name: "singles with two players", wantErr: true, mutate: func(spec *MatchSpec) {
			spec.SideOne.Participants = []ID{"a1", "a2"}
		}},
		{name: "player outside roster", wantErr: true, mutate: func(spec *MatchSpec) {
			spec.SideOne.Participants = []ID{"outsider"}
		}},
		{name: "player on both sides", wantErr: true, mutate: func(spec *MatchSpec) {
			spec.SideTwo.Participants = []ID{"a1"}
			teamTwo.Members = append(teamTwo.Members, "a1")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := base
			spec.SideOne.Participants = append([]ID(nil), base.SideOne.Participants...)
			spec.SideTwo.Participants = append([]ID(nil), base.SideTwo.Participants...)
			if test.mutate != nil {
				test.mutate(&spec)
			}
			_, err := NewMatch(spec, teamOne, teamTwo)
			if (err != nil) != test.wantErr {
				t.Fatalf("NewMatch() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
