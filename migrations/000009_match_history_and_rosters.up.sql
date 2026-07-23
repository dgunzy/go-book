ALTER TABLE teams
    ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version > 0);

ALTER TABLE verified_results
    ADD COLUMN display_score text
        CHECK (display_score IS NULL OR length(btrim(display_score)) BETWEEN 1 AND 120);

ALTER TABLE player_stat_projections
    ADD COLUMN singles_played integer NOT NULL DEFAULT 0 CHECK (singles_played >= 0),
    ADD COLUMN singles_wins integer NOT NULL DEFAULT 0 CHECK (singles_wins >= 0),
    ADD COLUMN singles_losses integer NOT NULL DEFAULT 0 CHECK (singles_losses >= 0),
    ADD COLUMN singles_ties integer NOT NULL DEFAULT 0 CHECK (singles_ties >= 0),
    ADD COLUMN singles_points numeric(10,2) NOT NULL DEFAULT 0 CHECK (singles_points >= 0),
    ADD COLUMN team_played integer NOT NULL DEFAULT 0 CHECK (team_played >= 0),
    ADD COLUMN team_wins integer NOT NULL DEFAULT 0 CHECK (team_wins >= 0),
    ADD COLUMN team_losses integer NOT NULL DEFAULT 0 CHECK (team_losses >= 0),
    ADD COLUMN team_ties integer NOT NULL DEFAULT 0 CHECK (team_ties >= 0),
    ADD COLUMN team_points numeric(10,2) NOT NULL DEFAULT 0 CHECK (team_points >= 0),
    ADD CONSTRAINT player_stat_projection_format_totals CHECK (
        singles_played + team_played = matches_played
        AND singles_wins + team_wins = wins
        AND singles_losses + team_losses = losses
        AND singles_ties + team_ties = ties
        AND singles_points + team_points = points_won
    ) NOT VALID;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM match_participants mp
        JOIN match_sides ms ON ms.id = mp.match_side_id
        GROUP BY ms.event_id, mp.player_id
        HAVING count(DISTINCT ms.team_id) > 1
    ) THEN
        RAISE EXCEPTION 'cannot backfill event rosters: a player appears for multiple teams in one event';
    END IF;
END;
$$;

INSERT INTO event_team_memberships (event_id, team_id, player_id, is_captain)
SELECT DISTINCT ms.event_id, ms.team_id, mp.player_id, false
FROM match_participants mp
JOIN match_sides ms ON ms.id = mp.match_side_id
ON CONFLICT (event_id, player_id) DO NOTHING;

WITH latest_results AS (
    SELECT DISTINCT ON (vr.match_id)
           vr.match_id, vr.outcome
    FROM verified_results vr
    ORDER BY vr.match_id, vr.version DESC
), contributions AS (
    SELECT mp.player_id,
           m.event_id,
           count(*) FILTER (WHERE m.format = 'singles')::integer AS singles_played,
           count(*) FILTER (
               WHERE m.format = 'singles'
                 AND ((lr.outcome = 'side_1' AND ms.side_number = 1)
                   OR (lr.outcome = 'side_2' AND ms.side_number = 2))
           )::integer AS singles_wins,
           count(*) FILTER (
               WHERE m.format = 'singles'
                 AND ((lr.outcome = 'side_1' AND ms.side_number = 2)
                   OR (lr.outcome = 'side_2' AND ms.side_number = 1))
           )::integer AS singles_losses,
           count(*) FILTER (WHERE m.format = 'singles' AND lr.outcome = 'tie')::integer AS singles_ties,
           sum(CASE WHEN m.format = 'singles' THEN
               CASE
                   WHEN lr.outcome = 'tie' THEN 0.5
                   WHEN (lr.outcome = 'side_1' AND ms.side_number = 1)
                     OR (lr.outcome = 'side_2' AND ms.side_number = 2) THEN 1
                   ELSE 0
               END ELSE 0 END)::numeric(10,2) AS singles_points,
           count(*) FILTER (WHERE m.format <> 'singles')::integer AS team_played,
           count(*) FILTER (
               WHERE m.format <> 'singles'
                 AND ((lr.outcome = 'side_1' AND ms.side_number = 1)
                   OR (lr.outcome = 'side_2' AND ms.side_number = 2))
           )::integer AS team_wins,
           count(*) FILTER (
               WHERE m.format <> 'singles'
                 AND ((lr.outcome = 'side_1' AND ms.side_number = 2)
                   OR (lr.outcome = 'side_2' AND ms.side_number = 1))
           )::integer AS team_losses,
           count(*) FILTER (WHERE m.format <> 'singles' AND lr.outcome = 'tie')::integer AS team_ties,
           sum(CASE WHEN m.format <> 'singles' THEN
               CASE
                   WHEN lr.outcome = 'tie' THEN 0.5
                   WHEN (lr.outcome = 'side_1' AND ms.side_number = 1)
                     OR (lr.outcome = 'side_2' AND ms.side_number = 2) THEN 1
                   ELSE 0
               END ELSE 0 END)::numeric(10,2) AS team_points
    FROM latest_results lr
    JOIN matches m ON m.id = lr.match_id
    JOIN match_participants mp ON mp.match_id = m.id
    JOIN match_sides ms ON ms.id = mp.match_side_id
    WHERE lr.outcome IN ('side_1', 'side_2', 'tie')
    GROUP BY mp.player_id, m.event_id
)
UPDATE player_stat_projections sp
SET singles_played = c.singles_played,
    singles_wins = c.singles_wins,
    singles_losses = c.singles_losses,
    singles_ties = c.singles_ties,
    singles_points = c.singles_points,
    team_played = c.team_played,
    team_wins = c.team_wins,
    team_losses = c.team_losses,
    team_ties = c.team_ties,
    team_points = c.team_points
FROM contributions c
WHERE c.player_id = sp.player_id AND c.event_id = sp.event_id;

ALTER TABLE player_stat_projections
    VALIDATE CONSTRAINT player_stat_projection_format_totals;
