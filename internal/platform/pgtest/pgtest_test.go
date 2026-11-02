package pgtest

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewAppliesMigrations(t *testing.T) {
	pool := New(t)

	got := publicTables(t, pool)
	want := []string{
		"accounts", "balances", "currencies", "journal_transactions",
		"outbox_events", "postings", "schema_migrations", "transaction_status_history",
	}
	if !slices.Equal(got, want) {
		t.Errorf("tables = %v, want %v", got, want)
	}
}

func TestMigrationsRoundTrip(t *testing.T) {
	pool, dsn := emptyDatabase(t)

	if err := migrateUp(dsn); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := migrateDown(dsn); err != nil {
		t.Fatalf("down: %v", err)
	}

	if got := publicTables(t, pool); !slices.Equal(got, []string{"schema_migrations"}) {
		t.Errorf("tables after down = %v, want only schema_migrations", got)
	}
	var functions int
	err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'public'`).Scan(&functions)
	if err != nil {
		t.Fatal(err)
	}
	if functions != 0 {
		t.Errorf("functions after down = %d, want 0", functions)
	}

	if err := migrateUp(dsn); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

// Both subtests insert the same currency, which is unique. If they shared a
// database, one of them would fail.
func TestNewIsolatesDatabases(t *testing.T) {
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pool := New(t)

			_, err := pool.Exec(t.Context(), `INSERT INTO currencies (code, exponent) VALUES ('EUR', 2)`)
			if err != nil {
				t.Fatalf("insert: %v", err)
			}

			var count int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM currencies WHERE code = 'EUR'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Errorf("EUR rows = %d, want 1", count)
			}
		})
	}
}

func TestImageMatchesCompose(t *testing.T) {
	compose, err := os.ReadFile("../../../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}

	match := regexp.MustCompile(`(?m)^\s*image:\s*(\S+)`).FindSubmatch(compose)
	if match == nil {
		t.Fatal("no image in docker-compose.yml")
	}
	if got := string(match[1]); got != Image {
		t.Errorf("docker-compose.yml uses %q, pgtest uses %q", got, Image)
	}
}

func publicTables(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()

	rows, err := pool.Query(t.Context(), `
		SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return tables
}
