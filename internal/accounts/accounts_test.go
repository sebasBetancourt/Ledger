package accounts_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sebasBetancourt/Ledger/internal/accounts/accountsdb"
	"github.com/sebasBetancourt/Ledger/internal/platform/pgtest"
)

func TestGetAccount(t *testing.T) {
	pool := pgtest.New(t)
	queries := accountsdb.New(pool)

	id := uuid.Must(uuid.NewV7())
	_, err := pool.Exec(t.Context(), `
		INSERT INTO accounts (id, type, currency) VALUES ($1, 'user', 'COP')`, id)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := queries.GetAccount(t.Context(), id)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}

	if got.ID != id {
		t.Errorf("ID = %v, want %v", got.ID, id)
	}
	if got.Type != "user" || got.Status != "active" || got.Currency != "COP" {
		t.Errorf("type, status, currency = %q, %q, %q, want user, active, COP", got.Type, got.Status, got.Currency)
	}
	if got.Code.Valid || got.AllowNegative {
		t.Errorf("code, allow_negative = %v, %v, want NULL, false", got.Code, got.AllowNegative)
	}
	if time.Since(got.CreatedAt) > time.Minute || got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt = %v, want about now", got.CreatedAt)
	}
}

func TestGetAccountNotFound(t *testing.T) {
	queries := accountsdb.New(pgtest.New(t))

	_, err := queries.GetAccount(t.Context(), uuid.Must(uuid.NewV7()))
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetAccount error = %v, want pgx.ErrNoRows", err)
	}
}
