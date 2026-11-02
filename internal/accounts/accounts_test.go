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

	id, owner := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	_, err := pool.Exec(t.Context(), `
		INSERT INTO accounts (id, kind, currency, owner_id, alias)
		VALUES ($1, 'user', 'COP', $2, 'ana')`, id, owner)
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
	if !got.OwnerID.Valid || got.OwnerID.UUID != owner {
		t.Errorf("OwnerID = %v, want %v", got.OwnerID, owner)
	}
	if got.Kind != "user" || got.Status != "active" || got.Currency != "COP" {
		t.Errorf("kind, status, currency = %q, %q, %q, want user, active, COP", got.Kind, got.Status, got.Currency)
	}
	if got.Alias.String != "ana" {
		t.Errorf("Alias = %q, want ana", got.Alias.String)
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
