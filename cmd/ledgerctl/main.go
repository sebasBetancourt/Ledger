// Command ledgerctl checks and repairs the ledger.
//
//	ledgerctl check-invariants
//	ledgerctl rebuild-balances --dry-run | --apply
//
// Exit codes: 0 all good (or --apply finished), 1 a problem was found,
// 2 usage or connection error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sebasBetancourt/Ledger/internal/ledger"
	"github.com/sebasBetancourt/Ledger/internal/platform/config"
)

const (
	exitOK      = 0
	exitProblem = 1
	exitError   = 2
)

const usage = `usage:
  ledgerctl check-invariants
  ledgerctl rebuild-balances --dry-run | --apply`

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return exitError
	}

	var command func(context.Context, *pgxpool.Pool) int
	switch args[0] {
	case "check-invariants":
		command = func(ctx context.Context, pool *pgxpool.Pool) int {
			return checkInvariants(ctx, pool, stdout, stderr)
		}
	case "rebuild-balances":
		flags := flag.NewFlagSet("rebuild-balances", flag.ContinueOnError)
		flags.SetOutput(stderr)
		dryRun := flags.Bool("dry-run", false, "list differences without changing anything")
		apply := flags.Bool("apply", false, "rewrite wrong balances under lock")
		if err := flags.Parse(args[1:]); err != nil {
			return exitError
		}
		if *dryRun == *apply {
			fmt.Fprintln(stderr, "rebuild-balances needs exactly one of --dry-run or --apply")
			return exitError
		}
		command = func(ctx context.Context, pool *pgxpool.Pool) int {
			return rebuildBalances(ctx, pool, *apply, stdout, stderr)
		}
	default:
		fmt.Fprintf(stderr, "unknown command %q\n%s\n", args[0], usage)
		return exitError
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(stderr, "connect:", err)
		return exitError
	}
	defer pool.Close()

	return command(ctx, pool)
}

func checkInvariants(ctx context.Context, pool *pgxpool.Pool, stdout, stderr io.Writer) int {
	violations, err := ledger.CheckInvariants(ctx, pool)
	if err != nil {
		fmt.Fprintln(stderr, "check invariants:", err)
		return exitError
	}
	if len(violations) == 0 {
		fmt.Fprintln(stdout, "ok: all 7 invariants hold")
		return exitOK
	}
	for _, v := range violations {
		fmt.Fprintln(stdout, v)
	}
	fmt.Fprintf(stdout, "%d violation(s)\n", len(violations))
	return exitProblem
}

func rebuildBalances(ctx context.Context, pool *pgxpool.Pool, apply bool, stdout, stderr io.Writer) int {
	// Each correction is logged as JSON on stderr, so it can be kept as a record.
	log := slog.New(slog.NewJSONHandler(stderr, nil))

	diffs, err := ledger.RebuildBalances(ctx, pool, apply, log)
	if err != nil {
		fmt.Fprintln(stderr, "rebuild balances:", err)
		return exitError
	}
	if len(diffs) == 0 {
		fmt.Fprintln(stdout, "ok: no differences between balances and postings")
		return exitOK
	}
	for _, d := range diffs {
		fmt.Fprintf(stdout, "%s: balance %d at seq %d, ledger %d at seq %d\n",
			d.AccountID, d.Projected, d.ProjectedSeq, d.Ledger, d.LedgerSeq)
	}
	if apply {
		fmt.Fprintf(stdout, "rebuilt %d balance(s)\n", len(diffs))
		return exitOK
	}
	fmt.Fprintf(stdout, "%d difference(s); run with --apply to fix\n", len(diffs))
	return exitProblem
}
