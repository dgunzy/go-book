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

	// CutoffYear is the last year covered by the retired site's export. Events
	// after it are editorial pages authored here; their competition records are
	// entered through the admin match workflow, not backfilled by the importer.
	CutoffYear = 2024
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
	Thumb   string
	Alt     string
	Caption string

	// Original, when set, points at the full-resolution file. Those objects are
	// stored with Content-Disposition: attachment, so following the link saves
	// the photograph rather than opening it. The HTML download attribute cannot
	// do this for us: it is ignored on a cross-origin href.
	Original string
}

// GridURL prefers the smaller derivative for gallery grids and falls back to the
// display image for older years that were imported without a thumbnail.
func (p Photo) GridURL() string {
	if p.Thumb != "" {
		return p.Thumb
	}
	return p.URL
}

// PhotoGroup is one titled run of photographs within an event gallery, normally
// a single day of play at a single course.
type PhotoGroup struct {
	Label  string
	Venue  string
	Note   string
	Photos []Photo
}

// Round records how one day of play was scored. It is optional; years imported
// from the former site carry their result only in Score and Summary.
type Round struct {
	Day    string
	Venue  string
	Format string
	Result string
	Points string
}

type Event struct {
	Year        int
	Winner      string
	RunnerUp    string
	Score       string
	Venue       string
	Summary     string
	Photos      []Photo
	Rounds      []Round
	Gallery     []PhotoGroup
	Placeholder bool
}

// GalleryCount reports how many photographs the full gallery holds.
func (e Event) GalleryCount() int {
	total := 0
	for _, group := range e.Gallery {
		total += len(group.Photos)
	}
	return total
}

// HasGallery reports whether the event has a dedicated photo page worth linking.
func (e Event) HasGallery() bool { return e.GalleryCount() > 0 }

// GridPhotos returns the featured photographs to show below the hero. An event
// with its own gallery page drops the first entry, which the hero already fills
// at full width; imported years keep every photograph on the year page.
func (e Event) GridPhotos() []Photo {
	if e.HasGallery() && len(e.Photos) > 1 {
		return e.Photos[1:]
	}
	return e.Photos
}

// LegacyImportable reports whether the event came from the retired site's export
// and may therefore be reconciled into PostgreSQL by the legacy importer. A
// placeholder has no source material, and a post-cutoff year is outside the
// import's scope.
func (e Event) LegacyImportable() bool { return !e.Placeholder && e.Year <= CutoffYear }

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
		event2026(),
	}
}

// media2026 is the CloudFront prefix holding the 2026 gallery. Display images sit
// at the prefix root, their grid derivatives under thumb/, and the camera
// originals under original/.
const media2026 = "https://d18fc2989jrcic.cloudfront.net/2026/"

func photo2026(file, alt, caption string) Photo {
	return Photo{
		URL:      media2026 + file,
		Thumb:    media2026 + "thumb/" + file,
		Original: media2026 + "original/" + file,
		Alt:      alt,
		Caption:  caption,
	}
}

func event2026() Event {
	crowbush := []Photo{
		photo2026("20260726-Z52_1094.jpg", "Players warming up on the Crowbush practice range", "Both sides share the range before the opening session"),
		photo2026("20260726-DSC_1097.jpg", "A player finishing a swing beside a warm-up range sign", "Irons only, as instructed"),
		photo2026("20260726-Z52_1092.jpg", "A player in a bucket hat giving a thumbs up on the range", "Ready for the opening tee shot"),
		photo2026("20260726-Z52_1118.jpg", "A player following a tee shot across water at Crowbush", "Carrying the water on the opening day"),
		photo2026("20260726-Z52_1136.jpg", "A player at the top of the backswing in front of a dune", "Into the dunes at Crowbush Cove"),
		photo2026("20260726-Z52_1133.jpg", "A player watching a shot toward a hilltop flag", "Watching one settle"),
		photo2026("20260726-Z52_1109.jpg", "A green flag standing against the sky above a links green", "Crowbush in the late afternoon"),
		photo2026("20260726-Z52_1143.jpg", "A ball resting on a green below a grassy dune", "Safely on, below the dune"),
	}
	dundarave := []Photo{
		photo2026("20260727-Z52_1158.jpg", "Four players posing with drivers on a tee", "A best-ball pairing before the off"),
		photo2026("20260727-Z52_1163.jpg", "Four players standing behind their drivers on a tee box", "Another Saturday pairing at Dundarave"),
		photo2026("20260727-Z52_1167.jpg", "Two Flamingos and two Bears posing together on a tee", "Bears and Flamingos, side by side"),
		photo2026("20260727-Z52_1156.jpg", "A player smiling in sunglasses and a quarter-zip", "All smiles at the turn"),
		photo2026("20260727-Z52_1174.jpg", "A player in a bucket hat walking off with a putter", "Walking off after holing out"),
		photo2026("20260727-Z52_1176.jpg", "A player standing with hands on hips beside a tee marker", "Weighing up the tee shot"),
		photo2026("20260727-Z52_1155.jpg", "A player lying flat on his back on a green", "The Dundarave heat claims another"),
		photo2026("20260727-Z52_1148.jpg", "Players gathered on a distant tee framed by tall grass", "A group waiting on the tee"),
		photo2026("20260727-Z52_1160.jpg", "Two carts crossing a wide fairway lined with spruce", "Crossing the fairway at Dundarave"),
	}
	brudenell := []Photo{
		photo2026("20260728-Z52_1177.jpg", "Three Flamingos players putting out on a green", "Flamingos on the practice green before singles"),
		photo2026("20260728-Z52_1184.jpg", "A Flamingos player striking a tee shot over water", "A full swing over the water at Brudenell"),
		photo2026("20260728-Z52_1190.jpg", "A Bears player watching a tee shot fly over a pond", "The Bears answer over the same pond"),
		photo2026("20260728-Z52_1195.jpg", "A player playing a recovery shot from beside a tree", "Singles takes a detour into the trees"),
		photo2026("20260728-Z52_1203.jpg", "Three players around a green with a Canadian flag pin", "Working out the last few feet"),
		photo2026("20260728-Z52_1206.jpg", "A Flamingos player and a Bears player laughing together", "Opponents for the day"),
		photo2026("20260728-Z52_1208.jpg", "A line of Bears players watching a shot from the side of a fairway", "The Bears gallery watches the singles come in"),
		photo2026("20260728-Z52_1275.jpg", "The Cabot Cup trophy standing on the grass", "The Cabot Cup"),
		photo2026("20260728-Z52_1259.jpg", "The winning Bears team lined up with the Cabot Cup", "The 2026 Bears, winners of the Cabot Cup"),
		photo2026("20260728-Z52_1266.jpg", "The Flamingos team lined up in their black and pink kit", "The Flamingos"),
		photo2026("20260728-Z52_1274.jpg", "Both teams gathered together behind the Cabot Cup", "Bears and Flamingos together at the close"),
		photo2026("20260728-Z52_1210.jpg", "A Bears player holding the cup and drinking from it", "First taste of the cup"),
		photo2026("20260728-Z52_1213.jpg", "Two Bears players passing the cup between them beside a cart", "Passing it along"),
		photo2026("20260728-Z52_1216.jpg", "A Bears player raising the cup to drink from it", "It goes round again"),
		photo2026("20260728-Z52_1218.jpg", "A Bears player carrying the cup in front of the clubhouse", "Carrying it back to the clubhouse"),
		photo2026("20260728-Z52_1225.jpg", "A player tipping the cup back under the clubhouse canopy", "Drinking from the Cabot Cup"),
		photo2026("20260728-Z52_1230.jpg", "A player lifting the cup with both hands under the pavilion", "Two hands on it"),
		photo2026("20260728-Z52_1235.jpg", "A bearded player drinking from the cup with teammates behind", "The celebration works its way down the roster"),
		photo2026("20260728-Z52_1238.jpg", "A grinning player carrying the cup past the carts", "Not letting go of it"),
		photo2026("20260728-Z52_1243.jpg", "Three Bears players laughing as one lifts the cup", "The Bears enjoy the closing session"),
		photo2026("20260728-Z52_1251.jpg", "A player addressing the group holding cash and a golf ball", "Prizes and closing remarks"),
	}

	return Event{
		Year: 2026, Winner: "Bears", RunnerUp: "Flamingos", Score: "26 - 10",
		Venue:   "Prince Edward Island",
		Summary: "The cup moved to Prince Edward Island for three courses in three days. The Bears swept the opening best-ball session at Crowbush to lead 8 - 0, held the Flamingos to a 2 - 2 split at Dundarave, and then took the singles at Brudenell River to finish 26 - 10 — the largest winning margin the team era has recorded.",
		Rounds: []Round{
			{Day: "Sunday 26 July", Venue: "The Links at Crowbush Cove", Format: "Best ball, 2 points a match", Result: "Bears sweep", Points: "Bears 8 - 0"},
			{Day: "Monday 27 July", Venue: "Dundarave Golf Course", Format: "Best ball, 3 points a match", Result: "Session split 2 - 2", Points: "Bears 6 - 6"},
			{Day: "Tuesday 28 July", Venue: "Brudenell River Golf Course", Format: "Singles, 2 points a match", Result: "Bears take the singles", Points: "Bears 12 - 4"},
		},
		// The first entry is the page hero. The second leads the featured grid at
		// full width, where a 16:8 crop would cut a group photograph in half, so
		// a landscape frame takes that slot.
		Photos: []Photo{
			brudenell[10], // both teams with the cup
			crowbush[6],   // Crowbush in the late afternoon
			brudenell[8],  // the winning Bears
			brudenell[7],  // the trophy
			brudenell[9],  // the Flamingos
			brudenell[11], // first drink from the cup
			dundarave[2],  // Bears and Flamingos pairing
			dundarave[6],  // flat out on the green
		},
		Gallery: []PhotoGroup{
			{
				Label: "Day one", Venue: "The Links at Crowbush Cove",
				Note:   "Best ball, two points a match. The Bears won all four.",
				Photos: crowbush,
			},
			{
				Label: "Day two", Venue: "Dundarave Golf Course",
				Note:   "Best ball, three points a match. The session finished level.",
				Photos: dundarave,
			},
			{
				Label: "Day three", Venue: "Brudenell River Golf Course",
				Note:   "Singles, two points a match, and the presentation that followed.",
				Photos: brudenell,
			},
		},
	}
}
