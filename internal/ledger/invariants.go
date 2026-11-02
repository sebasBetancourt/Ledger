package ledger

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sebasBetancourt/Ledger/internal/ledger/ledgerdb"
)

// Violation is one broken invariant, numbered as in docs/design/invariants.md.
type Violation struct {
	Invariant int
	Detail    string
}

func (v Violation) String() string {
	return fmt.Sprintf("invariant %d: %s", v.Invariant, v.Detail)
}

// CheckInvariants reads the whole ledger and reports every broken invariant.
//
// The database already refuses most of these on write, but an owner can
// disable triggers and a restore can bring old data, so this trusts nothing
// and reads what is actually stored. All checks share one REPEATABLE READ
// snapshot, so concurrent writes cannot produce a false alarm.
//
// Invariant 6 covers references and reversals; idempotency keys join it in
// phase 2.
func CheckInvariants(ctx context.Context, pool *pgxpool.Pool) ([]Violation, error) {
	var violations []Violation
	add := func(invariant int, format string, args ...any) {
		violations = append(violations, Violation{Invariant: invariant, Detail: fmt.Sprintf(format, args...)})
	}

	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	err := pgx.BeginTxFunc(ctx, pool, opts, func(tx pgx.Tx) error {
		q := ledgerdb.New(tx)

		unbalanced, err := q.UnbalancedTransactions(ctx)
		if err != nil {
			return fmt.Errorf("invariant 1: %w", err)
		}
		for _, r := range unbalanced {
			add(1, "transaction %s (%s) does not have 2+ postings summing to 0", r.ID, r.Status)
		}

		sums, err := q.SumPostingsByCurrency(ctx)
		if err != nil {
			return fmt.Errorf("invariant 2: %w", err)
		}
		for _, s := range sums {
			if s.Total != 0 {
				add(2, "postings in %s sum to %d", s.Currency, s.Total)
			}
		}

		mismatches, err := q.BalanceMismatches(ctx)
		if err != nil {
			return fmt.Errorf("invariant 3: %w", err)
		}
		for _, m := range mismatches {
			if !m.HasBalance {
				add(3, "account %s has no balance row", m.AccountID)
				continue
			}
			add(3, "account %s balance is %d at seq %d, ledger says %d at seq %d",
				m.AccountID, m.ProjectedTotal, m.ProjectedSeq, m.LedgerTotal, m.LedgerSeq)
		}

		broken, err := q.BrokenChains(ctx)
		if err != nil {
			return fmt.Errorf("invariant 4: %w", err)
		}
		for _, b := range broken {
			add(4, "account %s chain breaks at seq %d", b.AccountID, b.Seq)
		}

		negative, err := q.NegativeBalances(ctx)
		if err != nil {
			return fmt.Errorf("invariant 5: %w", err)
		}
		for _, n := range negative {
			add(5, "account %s has available %d, locked %d", n.AccountID, n.Available, n.Locked)
		}

		duplicates, err := q.DuplicateEffects(ctx)
		if err != nil {
			return fmt.Errorf("invariant 6: %w", err)
		}
		for _, d := range duplicates {
			add(6, "%s %q used %d times", d.Field, d.Value, d.Uses)
		}

		outbox, err := q.OutboxMismatches(ctx)
		if err != nil {
			return fmt.Errorf("invariant 7: %w", err)
		}
		for _, o := range outbox {
			add(7, "transaction %s has %d outbox events, want 1", o.ID, o.Events)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return violations, nil
}
