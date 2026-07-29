package competitionpg

import "testing"

func TestFormatLabelNeverLeaksTheRawEnum(t *testing.T) {
	for _, test := range []struct{ format, want string }{
		{"fourball", "Best ball"},
		{"foursomes", "Alternate shot"},
		{"singles", "Singles"},
		{"", ""},
		{"skins_game", "Skins game"},
	} {
		if got := (PublicMatchRow{Format: test.format}).FormatLabel(); got != test.want {
			t.Errorf("FormatLabel(%q) = %q, want %q", test.format, got, test.want)
		}
	}
}

func TestDisplayScoreNormalisesSpacingWithoutReinterpreting(t *testing.T) {
	for _, test := range []struct{ score, want string }{
		{"3&2", "3 & 2"},
		{"3 & 2", "3 & 2"},
		{"  6&5 ", "6 & 5"},
		{"1 up", "1 up"},
		{"", ""},
	} {
		if got := (PublicMatchRow{Score: test.score}).DisplayScore(); got != test.want {
			t.Errorf("DisplayScore(%q) = %q, want %q", test.score, got, test.want)
		}
	}
}
