ALTER TABLE player_stat_projections
    DROP CONSTRAINT player_stat_projection_format_totals,
    DROP COLUMN team_points,
    DROP COLUMN team_ties,
    DROP COLUMN team_losses,
    DROP COLUMN team_wins,
    DROP COLUMN team_played,
    DROP COLUMN singles_points,
    DROP COLUMN singles_ties,
    DROP COLUMN singles_losses,
    DROP COLUMN singles_wins,
    DROP COLUMN singles_played;

ALTER TABLE verified_results DROP COLUMN display_score;
ALTER TABLE teams DROP COLUMN version;
