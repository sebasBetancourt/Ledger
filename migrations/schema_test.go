package migrations_test

import (
	"fmt"
	"testing"

	"github.com/sebasBetancourt/Ledger/internal/platform/pgtest"
)

func TestCurrenciesSeeded(t *testing.T) {
	pool := pgtest.New(t)

	for code, want := range map[string]int64{"COP": 2, "USD": 2} {
		got := queryInt(t, pool, fmt.Sprintf(`SELECT exponent FROM currencies WHERE code = '%s'`, code))
		if got != want {
			t.Errorf("%s exponent = %d, want %d", code, got, want)
		}
	}
}

func TestAccountConstraints(t *testing.T) {
	tests := []struct {
		name       string
		constraint string
		statement  string
	}{
		{"unknown currency", "accounts_currency_fk",
			`INSERT INTO accounts (id, type, currency) VALUES (gen_random_uuid(), 'user', 'EUR')`},
		{"user allowed negative", "accounts_only_system_negative",
			`INSERT INTO accounts (id, type, currency, allow_negative) VALUES (gen_random_uuid(), 'user', 'COP', true)`},
		{"system without code", "accounts_code_only_system",
			`INSERT INTO accounts (id, type, currency, allow_negative) VALUES (gen_random_uuid(), 'system', 'COP', true)`},
		{"user with code", "accounts_code_only_system",
			`INSERT INTO accounts (id, type, code, currency) VALUES (gen_random_uuid(), 'user', 'fee_revenue', 'COP')`},
		{"unknown type", "accounts_type_valid",
			`INSERT INTO accounts (id, type, currency) VALUES (gen_random_uuid(), 'bank', 'COP')`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rejected(t, pgtest.New(t), tt.constraint, tt.statement)
		})
	}
}

func TestSystemCodeUniquePerCurrency(t *testing.T) {
	pool := pgtest.New(t)
	systemAccount(t, pool, "fee_revenue", "COP")

	rejected(t, pool, "accounts_code_currency_unique",
		`INSERT INTO accounts (id, type, code, currency, allow_negative)
		 VALUES (gen_random_uuid(), 'system', 'fee_revenue', 'COP', true)`)
}

func TestBalanceConstraints(t *testing.T) {
	pool, clearing := fixture(t)

	t.Run("wallet cannot go negative", func(t *testing.T) {
		rejected(t, pool, "balances_available_non_negative",
			fmt.Sprintf(`UPDATE balances SET available = -1 WHERE account_id = '%s'`, beto))
	})
	t.Run("locked cannot go negative", func(t *testing.T) {
		rejected(t, pool, "balances_locked_non_negative",
			fmt.Sprintf(`UPDATE balances SET locked = -1 WHERE account_id = '%s'`, beto))
	})
	t.Run("negative accounts cannot lock", func(t *testing.T) {
		rejected(t, pool, "balances_negative_never_locked",
			fmt.Sprintf(`UPDATE balances SET locked = 1 WHERE account_id = '%s'`, clearing))
	})
	t.Run("copy of allow_negative must match the account", func(t *testing.T) {
		rejected(t, pool, "balances_account_fk",
			fmt.Sprintf(`UPDATE balances SET allow_negative = true WHERE account_id = '%s'`, beto))
	})
	t.Run("system account may go negative", func(t *testing.T) {
		got := queryInt(t, pool, fmt.Sprintf(`SELECT available FROM balances WHERE account_id = '%s'`, clearing))
		if got != -anaFunds {
			t.Errorf("clearing available = %d, want %d", got, -anaFunds)
		}
	})
}

func TestPostingConstraints(t *testing.T) {
	pool, _ := fixture(t)
	open := []string{newTransaction(txn, "transfer"), setStatus(txn, "pending")}

	tests := []struct {
		name       string
		constraint string
		statement  string
	}{
		{"zero amount", "postings_amount_non_zero", posting(txn, ana, 0, 2, anaFunds)},
		{"wrong arithmetic", "postings_balance_math", `
			INSERT INTO postings (id, transaction_id, account_id, currency, amount, seq, prev_seq, balance_before, balance_after)
			VALUES (gen_random_uuid(), '` + txn + `', '` + ana + `', 'COP', -5, 2, 1, 10000000, 10000000)`},
		{"broken chain", "postings_chain_fk", posting(txn, ana, -5, 2, 777)},
		{"repeated seq", "postings_account_seq_unique", posting(txn, ana, -5, 1, 0)},
		{"skipped seq", "postings_chain_start", `
			INSERT INTO postings (id, transaction_id, account_id, currency, amount, seq, prev_seq, balance_before, balance_after)
			VALUES (gen_random_uuid(), '` + txn + `', '` + ana + `', 'COP', -5, 5, 1, 10000000, 9999995)`},
		{"currency differs from account", "postings_account_currency_fk", `
			INSERT INTO postings (id, transaction_id, account_id, currency, amount, seq, prev_seq, balance_before, balance_after)
			VALUES (gen_random_uuid(), '` + txn + `', '` + ana + `', 'USD', -5, 2, 1, 10000000, 9999995)`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rejected(t, pool, tt.constraint, append(open, tt.statement)...)
		})
	}
}

func TestJournalConstraints(t *testing.T) {
	pool, _ := fixture(t)

	t.Run("duplicate reference", func(t *testing.T) {
		rejected(t, pool, "journal_transactions_reference_unique", fmt.Sprintf(`
			INSERT INTO journal_transactions (id, reference, kind, correlation_id, initiated_by)
			VALUES (gen_random_uuid(), 'ref:%s', 'transfer', '%s', 'test')`, funding, correlation))
	})
	t.Run("unknown kind", func(t *testing.T) {
		rejected(t, pool, "journal_transactions_kind_valid", newTransaction(txn, "gift"))
	})
	t.Run("missing initiator", func(t *testing.T) {
		rejected(t, pool, "journal_transactions_initiated_by_present", fmt.Sprintf(`
			INSERT INTO journal_transactions (id, reference, kind, correlation_id, initiated_by)
			VALUES (gen_random_uuid(), 'ref:x', 'transfer', '%s', '')`, correlation))
	})
	t.Run("reversal without target", func(t *testing.T) {
		rejected(t, pool, "journal_transactions_reversal_target", newTransaction(txn, "reversal"))
	})
	t.Run("reverse twice", func(t *testing.T) {
		reversal := func(id string) string {
			return fmt.Sprintf(`
				INSERT INTO journal_transactions (id, reference, kind, correlation_id, initiated_by, reverses_id)
				VALUES ('%s', 'ref:%[1]s', 'reversal', '%s', 'test', '%s')`, id, correlation, funding)
		}
		rejected(t, pool, "journal_transactions_reverses_once",
			reversal("0190a000-0000-7000-8000-000000000201"),
			reversal("0190a000-0000-7000-8000-000000000202"))
	})
}

func TestImmutability(t *testing.T) {
	pool, _ := fixture(t)

	tests := []struct {
		name       string
		constraint string
		statement  string
	}{
		{"update a posting", "postings_immutable", `UPDATE postings SET amount = 1`},
		{"delete a posting", "postings_immutable", `DELETE FROM postings`},
		{"truncate postings", "postings_no_truncate", `TRUNCATE postings CASCADE`},
		{"delete a transaction", "journal_transactions_no_delete", `DELETE FROM journal_transactions`},
		{"delete a balance", "balances_no_delete", `DELETE FROM balances`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rejected(t, pool, tt.constraint, tt.statement)
		})
	}

	t.Run("edit status history", func(t *testing.T) {
		rejected(t, pool, "transaction_status_history_immutable",
			fmt.Sprintf(`INSERT INTO transaction_status_history (transaction_id, to_status) VALUES ('%s', 'created')`, funding),
			`UPDATE transaction_status_history SET reason = 'edited'`)
	})
}

func TestOutboxOneEventPerTransaction(t *testing.T) {
	pool, _ := fixture(t)

	rejected(t, pool, "outbox_events_one_per_transaction", outboxEvent(funding))
}

func TestValidTransferCommits(t *testing.T) {
	pool, _ := fixture(t)

	commit(t, pool, transfer(txn, ana, beto, 5_000_000, 2, anaFunds, 1, 0)...)

	if got := queryInt(t, pool, `SELECT coalesce(sum(amount), 0) FROM postings WHERE currency = 'COP'`); got != 0 {
		t.Errorf("sum of all COP postings = %d, want 0", got)
	}
	if got := queryInt(t, pool, fmt.Sprintf(`SELECT available FROM balances WHERE account_id = '%s'`, beto)); got != 5_000_000 {
		t.Errorf("beto available = %d, want 5000000", got)
	}
}
