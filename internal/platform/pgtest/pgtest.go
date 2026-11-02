// Package pgtest gives each test its own migrated PostgreSQL database.
//
// One container runs per test binary. Migrations are applied once to a
// template database, and every test gets a fresh copy of it. Copies cannot be
// cleaned with TRUNCATE because postings forbid it, and they let tests run in
// parallel without seeing each other's data.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"sync"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/sebasBetancourt/Ledger/migrations"
)

// Image must match docker-compose.yml; TestImageMatchesCompose checks it.
const Image = "postgres:16.15"

const templateDB = "ledger_template"

var (
	startOnce sync.Once
	startErr  error
	admin     *pgxpool.Pool
	baseURL   *url.URL

	// CREATE DATABASE fails if another session is copying the same template.
	cloneMu sync.Mutex
)

// New returns a pool connected to a fresh, migrated database that is dropped
// when the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()

	pool, _ := create(t, "TEMPLATE "+templateDB)
	return pool
}

// emptyDatabase returns a database with no migrations applied, and its URL.
func emptyDatabase(t testing.TB) (*pgxpool.Pool, string) {
	t.Helper()

	return create(t, "")
}

func create(t testing.TB, options string) (*pgxpool.Pool, string) {
	t.Helper()

	startOnce.Do(func() { startErr = start() })
	if startErr != nil {
		t.Fatalf("pgtest: start postgres: %v", startErr)
	}

	name := "test_" + randomHex()
	cloneMu.Lock()
	_, err := admin.Exec(context.Background(), "CREATE DATABASE "+name+" "+options)
	cloneMu.Unlock()
	if err != nil {
		t.Fatalf("pgtest: create database: %v", err)
	}

	dsn := urlFor(name)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgtest: connect: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("pgtest: drop database: %v", err)
		}
	})
	return pool, dsn
}

// start runs the container and migrates the template. The container is never
// terminated here; testcontainers removes it when the test binary exits.
func start() error {
	ctx := context.Background()

	container, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("ledger"),
		postgres.WithPassword("ledger"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return err
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return err
	}
	if baseURL, err = url.Parse(dsn); err != nil {
		return err
	}
	if admin, err = pgxpool.New(ctx, dsn); err != nil {
		return err
	}

	if _, err := admin.Exec(ctx, "CREATE DATABASE "+templateDB); err != nil {
		return err
	}
	return migrateUp(urlFor(templateDB))
}

func migrateUp(dsn string) error {
	return runMigrations(dsn, (*migrate.Migrate).Up)
}

func migrateDown(dsn string) error {
	return runMigrations(dsn, (*migrate.Migrate).Down)
}

func runMigrations(dsn string, step func(*migrate.Migrate) error) (err error) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return err
	}

	u, err := url.Parse(dsn)
	if err != nil {
		return err
	}
	u.Scheme = "pgx5"

	m, err := migrate.NewWithSourceInstance("iofs", source, u.String())
	if err != nil {
		return err
	}
	// Closing releases the connection, which a template must not have when it
	// is copied.
	defer func() {
		sourceErr, dbErr := m.Close()
		err = errors.Join(err, sourceErr, dbErr)
	}()

	if err := step(m); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func urlFor(database string) string {
	u := *baseURL
	u.Path = "/" + database
	return u.String()
}

func randomHex() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
