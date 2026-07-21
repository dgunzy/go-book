// Package legacy exposes the immutable public snapshot imported from the retired
// Cabot Cup site. It intentionally does not synthesize individual match records
// from aggregate statistics.
package legacy

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	SnapshotLabel = "Legacy snapshot through 2024"
	SourceNote    = "These totals were imported from the former Cabot Cup site. The source contains aggregate player records, not verified match-by-match results."
)

// Player contains aggregate public competition statistics from the legacy site.
type Player struct {
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	Image         string `json:"image"`
	TeamWins      int    `json:"team_wins"`
	TeamLosses    int    `json:"team_losses"`
	SinglesWins   int    `json:"singles_wins"`
	SinglesLosses int    `json:"singles_losses"`
	SinglesTies   int    `json:"singles_ties"`
	DoublesWins   int    `json:"doubles_wins"`
	DoublesLosses int    `json:"doubles_losses"`
	DoublesTies   int    `json:"doubles_ties"`
}

func (p Player) CupsPlayed() int { return p.TeamWins + p.TeamLosses }

func (p Player) MatchWins() int { return p.SinglesWins + p.DoublesWins }

func (p Player) MatchLosses() int { return p.SinglesLosses + p.DoublesLosses }

func (p Player) MatchTies() int { return p.SinglesTies + p.DoublesTies }

func (p Player) MatchesPlayed() int {
	return p.MatchWins() + p.MatchLosses() + p.MatchTies()
}

// WinningPercentage follows the legacy site's convention: a tie is half a win
// and the result is rounded to the nearest whole percent.
func (p Player) WinningPercentage() int {
	matches := p.MatchesPlayed()
	if matches == 0 {
		return 0
	}
	pointsTimesTwo := 2*p.MatchWins() + p.MatchTies()
	return (pointsTimesTwo*50 + matches/2) / matches
}

type Photo struct {
	URL     string
	Alt     string
	Caption string
}

type Event struct {
	Year        int
	Winner      string
	RunnerUp    string
	Score       string
	Venue       string
	Summary     string
	Photos      []Photo
	Placeholder bool
}

type Snapshot struct {
	Label   string
	Note    string
	Players []Player
	Events  []Event
}

type sourcePlayer struct {
	Name        string `json:"name"`
	ImageSrc    string `json:"imageSrc"`
	TeamWins    int    `json:"teamWins"`
	TeamLoss    int    `json:"teamLoss"`
	SinglesWins int    `json:"singlesWins"`
	SinglesLoss int    `json:"singlesLoss"`
	SinglesTie  int    `json:"singlesTie"`
	DoublesWins int    `json:"doublesWins"`
	DoublesLoss int    `json:"doublesLoss"`
	DoublesTie  int    `json:"doublesTie"`
}

//go:embed data/players.json
var playersJSON []byte

// Load parses and validates the embedded legacy snapshot. Returned slices are
// independent values and may be sorted by a caller without mutating global data.
func Load() (Snapshot, error) {
	var source []sourcePlayer
	if err := json.Unmarshal(playersJSON, &source); err != nil {
		return Snapshot{}, fmt.Errorf("decode legacy players: %w", err)
	}

	players := make([]Player, 0, len(source))
	seen := make(map[string]struct{}, len(source))
	for _, raw := range source {
		name := strings.TrimSpace(raw.Name)
		slug := playerSlug(name)
		if name == "" || slug == "" {
			return Snapshot{}, fmt.Errorf("legacy player has no usable name")
		}
		if _, exists := seen[slug]; exists {
			return Snapshot{}, fmt.Errorf("duplicate legacy player slug %q", slug)
		}
		seen[slug] = struct{}{}
		players = append(players, Player{
			Slug: slug, Name: name, Image: "/assets/players/" + strings.TrimPrefix(raw.ImageSrc, "/res/"),
			TeamWins: raw.TeamWins, TeamLosses: raw.TeamLoss,
			SinglesWins: raw.SinglesWins, SinglesLosses: raw.SinglesLoss, SinglesTies: raw.SinglesTie,
			DoublesWins: raw.DoublesWins, DoublesLosses: raw.DoublesLoss, DoublesTies: raw.DoublesTie,
		})
	}
	sort.Slice(players, func(i, j int) bool { return players[i].Name < players[j].Name })

	return Snapshot{
		Label:   SnapshotLabel,
		Note:    SourceNote,
		Players: players,
		Events:  legacyEvents(),
	}, nil
}

func playerSlug(name string) string {
	var b strings.Builder
	separator := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if separator && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			separator = false
		default:
			separator = true
		}
	}
	return b.String()
}

func legacyEvents() []Event {
	return []Event{
		{
			Year: 2019, Winner: "Sharks", RunnerUp: "Flamingos", Score: "10.5 - 9.5", Venue: "Cabot Cape Breton",
			Summary: "The inaugural team cup introduced a Ryder Cup-style exhibition. Hum captained the Sharks against DC's Flamingos. After trailing 7 - 5, the Sharks won five singles matches and halved another to complete the first Cabot Cup comeback.",
			Photos: []Photo{
				{URL: "https://d18fc2989jrcic.cloudfront.net/2019_cup.jpg", Alt: "The 2019 Cabot Cup teams", Caption: "The teams of the 2019 Cabot Cup"},
				{URL: "https://d18fc2989jrcic.cloudfront.net/2019_cup_looking.JPEG", Alt: "Flamingos players reading the 14th hole", Caption: "Flamingos players plan their approach on the 14th hole"},
				{URL: "https://d18fc2989jrcic.cloudfront.net/2019_cup_ivan.JPG", Alt: "Ivan drinking champagne from the cup", Caption: "Ivan celebrates with the cup"},
			},
		},
		{
			Year: 2020, Winner: "Hummingbirds", RunnerUp: "Sharks", Score: "15 - 10", Venue: "Cabot Cape Breton",
			Summary: "The short-field 'Covid Cup' featured twelve players. Alex's Hummingbirds trailed Ivan's Sharks 10 - 5 after alternate shot and best ball, then swept every singles match to win 15 - 10.",
			Photos: []Photo{
				{URL: "https://d18fc2989jrcic.cloudfront.net/2020_cup.jpg", Alt: "The 2020 Cabot Cup teams", Caption: "The teams of the 2020 Cabot Cup"},
				{URL: "https://d18fc2989jrcic.cloudfront.net/2020_cup_captains.JPEG", Alt: "The 2020 captains facing off", Caption: "The captains face off before the cup"},
				{URL: "https://d18fc2989jrcic.cloudfront.net/2020_cup_winners.JPG", Alt: "The winning Hummingbirds team", Caption: "The winning Hummingbirds"},
			},
		},
		{
			Year: 2021, Winner: "Panthers", RunnerUp: "Parrots", Score: "19.5 - 16.5", Venue: "Cabot Cape Breton",
			Summary: "A full twenty-player field returned. Ryan T's Panthers and Dan G's Parrots split alternate shot before the Panthers moved ahead in best ball and held the lead through singles.",
			Photos: []Photo{
				{URL: "https://d18fc2989jrcic.cloudfront.net/2021_cup_pairing.JPG", Alt: "Parrots and Panthers before a match", Caption: "Parrots and Panthers before a match"},
				{URL: "https://d18fc2989jrcic.cloudfront.net/2021_cup.JPEG", Alt: "Players celebrating with the cup", Caption: "The 2021 cup celebration"},
			},
		},
		{
			Year: 2022, Winner: "Turtles", RunnerUp: "Moose", Score: "23 - 13", Venue: "Fox Harb'r Resort",
			Summary: "Mau's Turtles faced the Moose, captained by Dan McNeil. The Moose led after alternate shot, but the Turtles reversed the result in best ball and went 6 - 2 in singles for a record winning margin.",
			Photos: []Photo{
				{URL: "https://d18fc2989jrcic.cloudfront.net/2022_cup.JPG", Alt: "The 2022 Cabot Cup teams", Caption: "The teams of the 2022 Cabot Cup"},
				{URL: "https://d18fc2989jrcic.cloudfront.net/2022_cup_fox.jpg", Alt: "A fox crossing the course", Caption: "A local crosses the course at Fox Harb'r"},
				{URL: "https://d18fc2989jrcic.cloudfront.net/2022_cup_winners.JPG", Alt: "The winning Turtles team", Caption: "The winning Turtles"},
			},
		},
		{
			Year: 2023, Winner: "Bears", RunnerUp: "Roosters", Score: "30.5 - 9.5", Venue: "Cabot Cape Breton",
			Summary: "Ramy's Bears met Retallick's Roosters in the spring. The Bears went 3 - 0 - 1 on the first day and swept the next four matches, clinching the cup before singles and setting another scoring record.",
			Photos: []Photo{
				{URL: "https://d18fc2989jrcic.cloudfront.net/2023_cup_range.JPG", Alt: "The 2023 teams warming up", Caption: "The teams warm up before play"},
				{URL: "https://d18fc2989jrcic.cloudfront.net/2023_cup_stuck.JPEG", Alt: "A golf cart stopped on a mound", Caption: "An off-course detour during the 2023 cup"},
				{URL: "https://d18fc2989jrcic.cloudfront.net/2023_cup_winners.JPG", Alt: "The winning Bears team", Caption: "The winning Bears"},
			},
		},
		{
			Year: 2024, Winner: "Lumberjacks", RunnerUp: "Cabanas", Score: "8 - 7 - 1 match record", Venue: "Fox Harb'r Resort",
			Summary: "The Cabanas led after alternate shot before the Lumberjacks won three of four best-ball matches. Singles finished level, leaving the Lumberjacks ahead after a closely contested week.",
			Photos: []Photo{
				{URL: "https://d18fc2989jrcic.cloudfront.net/2024_cup.jpg", Alt: "The 2024 Cabot Cup teams", Caption: "The teams of the 2024 Cabot Cup"},
				{URL: "https://d18fc2989jrcic.cloudfront.net/2024_cup_ryan_w.jpg", Alt: "Ryan W celebrating", Caption: "Captain Ryan W celebrates"},
				{URL: "https://d18fc2989jrcic.cloudfront.net/2024_cup_mau_baby.JPG", Alt: "Mau accepting an award", Caption: "Mau accepts the biggest baby award"},
				{URL: "https://d18fc2989jrcic.cloudfront.net/2024_cup_tee.jpg", Alt: "Lumberjacks and Cabanas on a tee", Caption: "Lumberjacks and Cabanas prepare to face off"},
			},
		},
		{
			Year: 2025, Venue: "Details to be added", Placeholder: true,
			Summary: "This page is reserved for the 2025 Cabot Cup write-up, photographs, and verified match statistics. No winner, score, teams, or individual match results have been entered yet.",
		},
	}
}
