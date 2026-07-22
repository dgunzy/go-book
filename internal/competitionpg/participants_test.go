package competitionpg

import (
	"errors"
	"testing"

	"github.com/dgunzy/go-book/internal/competition"
)

func TestValidateParticipantIDs(t *testing.T) {
	t.Parallel()
	playerOne := "11111111-1111-1111-1111-111111111111"
	playerTwo := "22222222-2222-2222-2222-222222222222"
	if err := validateParticipantIDs([]string{playerOne}, []string{playerTwo}); err != nil {
		t.Fatalf("valid participants error = %v", err)
	}
	for _, test := range []struct {
		name    string
		sideOne []string
		sideTwo []string
	}{
		{name: "malformed", sideOne: []string{"not-a-player"}, sideTwo: []string{playerTwo}},
		{name: "duplicate same side", sideOne: []string{playerOne, playerOne}, sideTwo: []string{playerTwo}},
		{name: "player on both sides", sideOne: []string{playerOne}, sideTwo: []string{playerOne}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateParticipantIDs(test.sideOne, test.sideTwo); !errors.Is(err, competition.ErrInvalid) {
				t.Fatalf("validateParticipantIDs() error = %v, want ErrInvalid", err)
			}
		})
	}
}
