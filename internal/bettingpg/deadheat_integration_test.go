package bettingpg

import (
	"context"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/betting"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The 2026 cup ended with three members tied for most points. Grading all
// three as outright winners would have paid three full prices out of a pool
// that was only ever priced for one, so a tie is settled as a dead heat: each
// winning wager rides its share of the stake and loses the rest.

// addSelection hangs a third runner off the fixture market, so a genuine
// three-way tie can be graded rather than a two-way one.
func addSelection(t *testing.T, ctx context.Context, pool *pgxpool.Pool, marketID, key, terms string, odds int) string {
	t.Helper()
	return mustScanID(t, ctx, pool, `
		INSERT INTO selections (market_id, selection_key, display_terms, offered_american_odds, active)
		VALUES ($1::uuid, $2, $3, $4, true) RETURNING id::text`, marketID, key, terms, odds)
}

func settlementRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wagerID string) (result string, stake, profit, returned int64, divisor int) {
	t.Helper()
	if err := pool.QueryRow(ctx, `
		SELECT result, stake_cents, profit_cents, returned_cents, dead_heat_divisor
		FROM wager_settlements WHERE wager_id = $1::uuid
		ORDER BY settled_at DESC LIMIT 1`, wagerID).
		Scan(&result, &stake, &profit, &returned, &divisor); err != nil {
		t.Fatalf("read wager settlement for %s: %v", wagerID, err)
	}
	return result, stake, profit, returned, divisor
}

// TestSettleMarketDeadHeatPaysEachTiedWinnerItsShare grades the real shape of
// the tie: three selections level, two of them carrying a member's money.
func TestSettleMarketDeadHeatPaysEachTiedWinnerItsShare(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 200_000)
	selectionC := addSelection(t, ctx, pool, f.MarketID, "side-c", "Player C most points", 150)

	// $300 at the fixture's -110 on A, and $300 on B.
	wagerA := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 30_000, 1)
	wagerB := placeAndAccept(t, ctx, store, f, f.UserB, f.SelectionBID, 30_000, 1)

	report, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "three-way tie on points",
		Outcome: map[string]betting.SettlementResult{
			f.SelectionAID: betting.ResultWin,
			f.SelectionBID: betting.ResultWin,
			selectionC:     betting.ResultWin,
		},
		DeadHeat: true,
	})
	if err != nil {
		t.Fatalf("SettleMarket() as a dead heat error = %v", err)
	}
	if report.WinCount != 2 {
		t.Fatalf("win count = %d, want 2", report.WinCount)
	}

	// $300 with three tied: $100 rides at -110 for $90.91 profit, so $190.91
	// comes back and $200 of stake is lost.
	result, stake, profit, returned, divisor := settlementRow(t, ctx, pool, string(wagerA.ID))
	if result != string(betting.ResultWin) || divisor != 3 {
		t.Fatalf("settlement result/divisor = %s/%d, want win/3", result, divisor)
	}
	if stake != 30_000 || profit != 9_091 || returned != 19_091 {
		t.Fatalf("dead heat settlement = stake %d profit %d returned %d, want 30000/9091/19091", stake, profit, returned)
	}
	// The stake stays whole in the record even though only a third of it rode,
	// because that is what the member actually put up.
	if _, stakeB, _, returnedB, divisorB := settlementRow(t, ctx, pool, string(wagerB.ID)); stakeB != 30_000 || returnedB != 19_091 || divisorB != 3 {
		t.Fatalf("second tied winner = stake %d returned %d divisor %d, want the same share", stakeB, returnedB, divisorB)
	}
}

// The divisor has to reach market_settlement_outcomes, and only on the
// winners: it is what a later regrade reads back.
func TestSettleMarketDeadHeatRecordsTheDivisorOnWinnersOnly(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 200_000)
	selectionC := addSelection(t, ctx, pool, f.MarketID, "side-c", "Player C most points", 150)
	placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 30_000, 1)

	if _, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "A and B tied, C behind",
		Outcome: map[string]betting.SettlementResult{
			f.SelectionAID: betting.ResultWin,
			f.SelectionBID: betting.ResultWin,
			selectionC:     betting.ResultLoss,
		},
		DeadHeat: true,
	}); err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT outcome, dead_heat_divisor FROM market_settlement_outcomes
		WHERE market_id = $1::uuid`, f.MarketID)
	if err != nil {
		t.Fatalf("read recorded outcomes: %v", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var outcome string
		var divisor int
		if err := rows.Scan(&outcome, &divisor); err != nil {
			t.Fatalf("scan recorded outcome: %v", err)
		}
		seen++
		want := 1
		if outcome == string(betting.ResultWin) {
			want = 2
		}
		if divisor != want {
			t.Fatalf("recorded %s carries divisor %d, want %d", outcome, divisor, want)
		}
	}
	if seen != 3 {
		t.Fatalf("recorded outcomes = %d, want 3", seen)
	}
}

// A wager the settlement missed must be paid the same fraction its peers were
// paid. If the regrade re-counted winners instead of replaying the recorded
// divisor it would still reach 2 here, so the divisor is deliberately read
// back and asserted on the settlement row rather than inferred from the money.
func TestRegradeReplaysARecordedDeadHeat(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 200_000)
	selectionC := addSelection(t, ctx, pool, f.MarketID, "side-c", "Player C most points", 150)
	graded := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 30_000, 1)
	stranded := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 30_000, 2)

	strandWager(t, ctx, pool, string(stranded.ID), func() {
		if _, err := store.SettleMarket(ctx, SettleMarketRequest{
			MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "three-way tie on points",
			Outcome: map[string]betting.SettlementResult{
				f.SelectionAID: betting.ResultWin,
				f.SelectionBID: betting.ResultWin,
				selectionC:     betting.ResultWin,
			},
			DeadHeat: true,
		}); err != nil {
			t.Fatalf("SettleMarket() error = %v", err)
		}
	})

	if _, err := store.RegradeStrandedWagers(ctx, f.MarketID, f.UserB, "grading the bet settlement missed"); err != nil {
		t.Fatalf("RegradeStrandedWagers() error = %v", err)
	}

	_, _, gradedProfit, gradedReturned, gradedDivisor := settlementRow(t, ctx, pool, string(graded.ID))
	_, _, profit, returned, divisor := settlementRow(t, ctx, pool, string(stranded.ID))
	if divisor != gradedDivisor || divisor != 3 {
		t.Fatalf("regraded divisor = %d, want the recorded 3 that its peer got (%d)", divisor, gradedDivisor)
	}
	if profit != gradedProfit || returned != gradedReturned {
		t.Fatalf("regraded settlement = profit %d returned %d, want its peer's %d/%d",
			profit, returned, gradedProfit, gradedReturned)
	}
}

// Without the flag nothing changes: several winners each collect in full, the
// way a prop with more than one true outcome always has.
func TestSettleMarketWithoutDeadHeatStillPaysWinnersInFull(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := Store{DB: pool}

	f := buildFixture(t, ctx, pool, 200_000)
	wager := placeAndAccept(t, ctx, store, f, f.UserA, f.SelectionAID, 30_000, 1)

	if _, err := store.SettleMarket(ctx, SettleMarketRequest{
		MarketID: f.MarketID, ActorUserID: f.UserB, Reason: "both selections came in",
		Outcome: map[string]betting.SettlementResult{
			f.SelectionAID: betting.ResultWin,
			f.SelectionBID: betting.ResultWin,
		},
	}); err != nil {
		t.Fatalf("SettleMarket() error = %v", err)
	}

	// $300 at -110 in full: $272.73 profit, $572.73 back.
	_, _, profit, returned, divisor := settlementRow(t, ctx, pool, string(wager.ID))
	if divisor != 1 || profit != 27_273 || returned != 57_273 {
		t.Fatalf("undeclared settlement = profit %d returned %d divisor %d, want 27273/57273/1", profit, returned, divisor)
	}
}
