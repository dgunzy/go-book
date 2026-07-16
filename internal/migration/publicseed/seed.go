// Package publicseed imports the immutable public Cabot Cup snapshot into
// PostgreSQL. It preserves aggregate history without fabricating matches.
package publicseed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"path"
	"strings"

	"github.com/dgunzy/go-book/internal/legacy"
	publicassets "github.com/dgunzy/go-book/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	sourceSystem  = "cabot-cup-public-snapshot"
	sourceVersion = "2024-v1"
	sourceName    = "legacy-cabot-cup-public-json"
	actorEmail    = "legacy-public-import@cabotcup.invalid"
)

// DB is the subset of pgx used by Apply. Both *pgx.Conn and pgx.Tx satisfy it.
// Callers that require atomicity should pass a transaction.
type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Report describes the rows reconciled by an Apply call. Counts are stable on
// reruns: existing rows are updated in place through their natural keys.
type Report struct {
	Players            int `json:"players"`
	Events             int `json:"events"`
	StatSnapshots      int `json:"stat_snapshots"`
	MediaAssets        int `json:"media_assets"`
	MediaPlayerLinks   int `json:"media_player_links"`
	SkippedEventPhotos int `json:"skipped_event_photos"`
}

type seedData struct {
	snapshot legacy.Snapshot
	media    []mediaSeed
}

type mediaSeed struct {
	ObjectKey   string
	Filename    string
	ContentType string
	ByteSize    int64
	Checksum    string
	Width       int
	Height      int
	PlayerSlugs []string
}

// Apply loads the embedded legacy snapshot and reconciles it with PostgreSQL.
// It does not commit or roll back db; the caller owns the transaction boundary.
func Apply(ctx context.Context, db DB) (Report, error) {
	data, err := loadSeedData()
	if err != nil {
		return Report{}, err
	}

	report := Report{
		Players:            len(data.snapshot.Players),
		Events:             len(data.snapshot.Events),
		StatSnapshots:      len(data.snapshot.Players),
		MediaAssets:        len(data.media),
		SkippedEventPhotos: countEventPhotos(data.snapshot),
	}

	counts, err := json.Marshal(map[string]int{
		"players":              report.Players,
		"events":               report.Events,
		"stat_snapshots":       report.StatSnapshots,
		"media_assets":         report.MediaAssets,
		"skipped_event_photos": report.SkippedEventPhotos,
	})
	if err != nil {
		return Report{}, fmt.Errorf("encode public seed counts: %w", err)
	}

	var batchID string
	err = db.QueryRow(ctx, `
		INSERT INTO migration_batches (source_system, source_version, state, source_counts)
		VALUES ($1, $2, 'staged', $3::jsonb)
		ON CONFLICT (source_system, source_version) DO UPDATE
		SET source_counts = EXCLUDED.source_counts
		RETURNING id::text`, sourceSystem, sourceVersion, string(counts)).Scan(&batchID)
	if err != nil {
		return Report{}, fmt.Errorf("upsert public migration batch: %w", err)
	}

	var actorID string
	err = db.QueryRow(ctx, `
		INSERT INTO users (display_name, email, status)
		VALUES ('Legacy public import', $1, 'disabled')
		ON CONFLICT (lower(email)) WHERE email IS NOT NULL DO UPDATE
		SET display_name = EXCLUDED.display_name,
		    status = 'disabled',
		    updated_at = now()
		RETURNING id::text`, actorEmail).Scan(&actorID)
	if err != nil {
		return Report{}, fmt.Errorf("upsert public migration actor: %w", err)
	}

	playerIDs := make(map[string]string, len(data.snapshot.Players))
	for _, player := range data.snapshot.Players {
		var playerID string
		err = db.QueryRow(ctx, `
			INSERT INTO players (slug, display_name, active)
			VALUES ($1, $2, true)
			ON CONFLICT (slug) DO UPDATE
			SET display_name = EXCLUDED.display_name,
			    updated_at = now()
			RETURNING id::text`, player.Slug, player.Name).Scan(&playerID)
		if err != nil {
			return Report{}, fmt.Errorf("upsert player %q: %w", player.Slug, err)
		}
		playerIDs[player.Slug] = playerID

		statistics, marshalErr := json.Marshal(playerStatistics(player))
		if marshalErr != nil {
			return Report{}, fmt.Errorf("encode statistics for player %q: %w", player.Slug, marshalErr)
		}
		var statID string
		err = db.QueryRow(ctx, `
			INSERT INTO legacy_stat_snapshots
			    (migration_batch_id, player_id, as_of_label, source, statistics)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5::jsonb)
			ON CONFLICT (migration_batch_id, player_id, as_of_label) DO UPDATE
			SET source = EXCLUDED.source,
			    statistics = EXCLUDED.statistics,
			    imported_at = now()
			RETURNING id::text`, batchID, playerID, data.snapshot.Label, sourceName, string(statistics)).Scan(&statID)
		if err != nil {
			return Report{}, fmt.Errorf("upsert statistics for player %q: %w", player.Slug, err)
		}

		playerChecksum, checksumErr := checksumJSON(player)
		if checksumErr != nil {
			return Report{}, fmt.Errorf("checksum player %q: %w", player.Slug, checksumErr)
		}
		if err = recordImport(ctx, db, batchID, "public_players", player.Slug, "players", playerID, playerChecksum); err != nil {
			return Report{}, err
		}
		if err = recordImport(ctx, db, batchID, "public_player_statistics", player.Slug, "legacy_stat_snapshots", statID, checksumBytes(statistics)); err != nil {
			return Report{}, err
		}
	}

	for _, event := range data.snapshot.Events {
		slug := fmt.Sprintf("cabot-cup-%d", event.Year)
		name := fmt.Sprintf("Cabot Cup %d", event.Year)
		var eventID string
		err = db.QueryRow(ctx, `
			INSERT INTO events
			    (slug, name, season_year, venue, narrative, state, created_by)
			VALUES ($1, $2, $3, $4, $5, 'completed', $6::uuid)
			ON CONFLICT (slug) DO UPDATE
			SET name = EXCLUDED.name,
			    season_year = EXCLUDED.season_year,
			    venue = EXCLUDED.venue,
			    narrative = EXCLUDED.narrative,
			    state = EXCLUDED.state,
			    updated_at = now()
			RETURNING id::text`, slug, name, event.Year, event.Venue, legacyNarrative(event), actorID).Scan(&eventID)
		if err != nil {
			return Report{}, fmt.Errorf("upsert event %d: %w", event.Year, err)
		}
		eventChecksum, checksumErr := checksumJSON(event)
		if checksumErr != nil {
			return Report{}, fmt.Errorf("checksum event %d: %w", event.Year, checksumErr)
		}
		if err = recordImport(ctx, db, batchID, "public_events", fmt.Sprint(event.Year), "events", eventID, eventChecksum); err != nil {
			return Report{}, err
		}
	}

	for _, media := range data.media {
		var mediaID string
		err = db.QueryRow(ctx, `
			INSERT INTO media_assets
			    (object_key, content_type, byte_size, checksum_sha256, width, height, alt_text, state, uploaded_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8::uuid)
			ON CONFLICT (object_key) DO UPDATE
			SET content_type = EXCLUDED.content_type,
			    byte_size = EXCLUDED.byte_size,
			    checksum_sha256 = EXCLUDED.checksum_sha256,
			    width = EXCLUDED.width,
			    height = EXCLUDED.height,
			    alt_text = EXCLUDED.alt_text
			RETURNING id::text`, media.ObjectKey, media.ContentType, media.ByteSize, media.Checksum,
			media.Width, media.Height, "Legacy profile photograph", actorID).Scan(&mediaID)
		if err != nil {
			return Report{}, fmt.Errorf("upsert media %q: %w", media.Filename, err)
		}
		if err = recordImport(ctx, db, batchID, "bundled_player_media", media.Filename, "media_assets", mediaID, media.Checksum); err != nil {
			return Report{}, err
		}

		for _, playerSlug := range media.PlayerSlugs {
			playerID, ok := playerIDs[playerSlug]
			if !ok {
				return Report{}, fmt.Errorf("media %q references unknown player %q", media.Filename, playerSlug)
			}
			_, err = db.Exec(ctx, `
				INSERT INTO media_player_links (media_asset_id, player_id, display_order)
				VALUES ($1::uuid, $2::uuid, 0)
				ON CONFLICT (media_asset_id, player_id) DO NOTHING`, mediaID, playerID)
			if err != nil {
				return Report{}, fmt.Errorf("link media %q to player %q: %w", media.Filename, playerSlug, err)
			}
			report.MediaPlayerLinks++
		}
	}

	reconciliation, err := json.Marshal(map[string]any{
		"status":                "reconciled",
		"source_note":           data.snapshot.Note,
		"remote_photos_skipped": report.SkippedEventPhotos,
		"media_state":           "pending_s3_upload",
	})
	if err != nil {
		return Report{}, fmt.Errorf("encode public seed reconciliation: %w", err)
	}
	_, err = db.Exec(ctx, `
		UPDATE migration_batches
		SET state = 'promoted',
		    reconciliation = $2::jsonb,
		    completed_at = now()
		WHERE id = $1::uuid`, batchID, string(reconciliation))
	if err != nil {
		return Report{}, fmt.Errorf("complete public migration batch: %w", err)
	}

	return report, nil
}

func loadSeedData() (seedData, error) {
	snapshot, err := legacy.Load()
	if err != nil {
		return seedData{}, fmt.Errorf("load legacy public snapshot: %w", err)
	}

	byFilename := make(map[string]*mediaSeed, len(snapshot.Players))
	for _, player := range snapshot.Players {
		filename := path.Base(player.Image)
		if filename == "." || filename == "/" || filename == "" {
			return seedData{}, fmt.Errorf("player %q has invalid image path %q", player.Slug, player.Image)
		}
		if existing := byFilename[filename]; existing != nil {
			existing.PlayerSlugs = append(existing.PlayerSlugs, player.Slug)
			continue
		}

		assetPath := path.Join("players", filename)
		contents, readErr := fs.ReadFile(publicassets.Files, assetPath)
		if readErr != nil {
			return seedData{}, fmt.Errorf("read player media %q: %w", assetPath, readErr)
		}
		config, _, decodeErr := image.DecodeConfig(bytes.NewReader(contents))
		if decodeErr != nil {
			return seedData{}, fmt.Errorf("decode player media %q: %w", assetPath, decodeErr)
		}
		media := &mediaSeed{
			ObjectKey:   path.Join("legacy", "players", filename),
			Filename:    filename,
			ContentType: contentType(filename),
			ByteSize:    int64(len(contents)),
			Checksum:    checksumBytes(contents),
			Width:       config.Width,
			Height:      config.Height,
			PlayerSlugs: []string{player.Slug},
		}
		byFilename[filename] = media
	}

	media := make([]mediaSeed, 0, len(byFilename))
	for _, player := range snapshot.Players {
		filename := path.Base(player.Image)
		item := byFilename[filename]
		if item == nil {
			continue
		}
		media = append(media, *item)
		delete(byFilename, filename)
	}
	return seedData{snapshot: snapshot, media: media}, nil
}

func playerStatistics(player legacy.Player) map[string]any {
	return map[string]any{
		"team": map[string]int{
			"wins": player.TeamWins, "losses": player.TeamLosses,
		},
		"singles": map[string]int{
			"wins": player.SinglesWins, "losses": player.SinglesLosses, "ties": player.SinglesTies,
		},
		"doubles": map[string]int{
			"wins": player.DoublesWins, "losses": player.DoublesLosses, "ties": player.DoublesTies,
		},
		"totals": map[string]int{
			"cups_played": player.CupsPlayed(), "matches_played": player.MatchesPlayed(),
			"wins": player.MatchWins(), "losses": player.MatchLosses(), "ties": player.MatchTies(),
		},
	}
}

func recordImport(ctx context.Context, db DB, batchID, sourceTable, sourceKey, targetTable, targetID, checksum string) error {
	_, err := db.Exec(ctx, `
		INSERT INTO legacy_import_records
		    (migration_batch_id, source_table, source_primary_key, target_table, target_id,
		     source_checksum, import_state, imported_at)
		VALUES ($1::uuid, $2, $3, $4, $5::uuid, $6, 'imported', now())
		ON CONFLICT (migration_batch_id, source_table, source_primary_key) DO UPDATE
		SET target_table = EXCLUDED.target_table,
		    target_id = EXCLUDED.target_id,
		    source_checksum = EXCLUDED.source_checksum,
		    import_state = 'imported',
		    error_message = NULL,
		    imported_at = now()`, batchID, sourceTable, sourceKey, targetTable, targetID, checksum)
	if err != nil {
		return fmt.Errorf("record import %s/%s: %w", sourceTable, sourceKey, err)
	}
	return nil
}

func legacyNarrative(event legacy.Event) string {
	return fmt.Sprintf("Legacy final: %s defeated %s; %s\n\n%s", event.Winner, event.RunnerUp, event.Score, event.Summary)
}

func countEventPhotos(snapshot legacy.Snapshot) int {
	count := 0
	for _, event := range snapshot.Events {
		count += len(event.Photos)
	}
	return count
}

func checksumJSON(value any) (string, error) {
	contents, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return checksumBytes(contents), nil
}

func checksumBytes(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func contentType(filename string) string {
	switch strings.ToLower(path.Ext(filename)) {
	case ".png":
		return "image/png"
	default:
		return "image/jpeg"
	}
}
