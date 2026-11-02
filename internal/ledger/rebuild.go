package ledger

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sebasBetancourt/Ledger/internal/ledger/ledgerdb"
)

// BalanceDiff is a balance that disagrees with the postings, which are the
// source of truth. Projected is the stored total (available + locked).
type BalanceDiff struct {
	AccountID    uuid.UUID
	Projected    Amount
	ProjectedSeq int64
	Ledger       Amount
	LedgerSeq    int64
}

// RebuildBalances compares every balance with the sum of its postings and
// returns the differences. With apply, it rewrites the wrong ones in one
// transaction, under the same row locks Post uses, and logs each correction.
//
// An account without a balance row cannot be fixed by an update; it fails with
// ErrAccountNotFound so a human looks at it.
func RebuildBalances(ctx context.Context, pool *pgxpool.Pool, apply bool, log *slog.Logger) ([]BalanceDiff, error) {
	mismatches, err := ledgerdb.New(pool).BalanceMismatches(ctx)
	if err != nil {
		return nil, fmt.Errorf("find mismatches: %w", err)
	}

	diffs := make([]BalanceDiff, 0, len(mismatches))
	ids := make([]uuid.UUID, 0, len(mismatches))
	for _, m := range mismatches {
		if !m.HasBalance {
			return nil, fmt.Errorf("%w: account %s has no balance row", ErrAccountNotFound, m.AccountID)
		}
		diffs = append(diffs, BalanceDiff{
			AccountID:    m.AccountID,
			Projected:    Amount(m.ProjectedTotal),
			ProjectedSeq: m.ProjectedSeq,
			Ledger:       Amount(m.LedgerTotal),
			LedgerSeq:    m.LedgerSeq,
		})
		ids = append(ids, m.AccountID)
	}
	if !apply || len(diffs) == 0 {
		return diffs, nil
	}

	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := ledgerdb.New(tx)

		balances, err := q.LockBalances(ctx, ids)
		if err != nil {
			return fmt.Errorf("lock balances: %w", err)
		}
		// Recompute under the lock: a Post may have run between the first
		// read and the lock, and its postings are now part of the truth.
		totals, err := q.LedgerTotals(ctx, ids)
		if err != nil {
			return fmt.Errorf("recompute totals: %w", err)
		}
		ledger := make(map[uuid.UUID]ledgerdb.LedgerTotalsRow, len(totals))
		for _, t := range totals {
			ledger[t.AccountID] = t
		}

		for _, b := range balances {
			want := ledger[b.AccountID]
			// Holds stay as they are; available absorbs the difference.
			available := want.Total - b.Locked
			if available == b.Available && want.LastSeq == b.LastSeq {
				continue
			}
			_, err := q.UpdateBalance(ctx, ledgerdb.UpdateBalanceParams{
				AccountID: b.AccountID,
				Available: available,
				Locked:    b.Locked,
				LastSeq:   want.LastSeq,
			})
			if err != nil {
				return fmt.Errorf("rewrite balance of %s: %w", b.AccountID, err)
			}
			log.Info("balance rebuilt",
				"account_id", b.AccountID,
				"old_available", b.Available,
				"new_available", available,
				"old_last_seq", b.LastSeq,
				"new_last_seq", want.LastSeq,
			)
		}
		return nil
	})
	if err != nil {
		return nil, mapPgError(err)
	}
	return diffs, nil
}
