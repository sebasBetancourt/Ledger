package ledger_test

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/sebasBetancourt/Ledger/internal/ledger"
)

// New postings arrive between pages. Each posting that existed when paging
// started must appear exactly once, and none may be skipped.
func TestHistoryNeitherRepeatsNorSkips(t *testing.T) {
	t.Parallel()
	_, svc, ana, _ := funded(t, 1_000)
	for i := range 9 {
		fund(t, svc, ana, fmt.Sprintf("more-%d", i), 1_000)
	}

	first, err := svc.History(t.Context(), ana, "", 3)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	seqs := seqsOf(first.Entries)

	for i := range 3 {
		fund(t, svc, ana, fmt.Sprintf("while-paging-%d", i), 1_000)
	}

	cursor := first.NextCursor
	for cursor != "" {
		page, err := svc.History(t.Context(), ana, cursor, 3)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		seqs = append(seqs, seqsOf(page.Entries)...)
		cursor = page.NextCursor
	}

	want := []int64{10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	if !slices.Equal(seqs, want) {
		t.Errorf("paged seqs = %v, want %v", seqs, want)
	}

	// A fresh read starts with the newest postings, each continuing the chain.
	latest, err := svc.History(t.Context(), ana, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest.Entries) != 13 || latest.Entries[0].Seq != 13 || latest.NextCursor != "" {
		t.Fatalf("latest = %d entries starting at %d, cursor %q, want 13 from 13 and no cursor",
			len(latest.Entries), latest.Entries[0].Seq, latest.NextCursor)
	}
	for i := 0; i+1 < len(latest.Entries); i++ {
		newer, older := latest.Entries[i], latest.Entries[i+1]
		if newer.BalanceBefore != older.BalanceAfter {
			t.Errorf("seq %d starts at %d, but seq %d ended at %d", newer.Seq, newer.BalanceBefore, older.Seq, older.BalanceAfter)
		}
	}
}

func TestHistoryRejects(t *testing.T) {
	t.Parallel()
	_, svc, ana, _ := funded(t, 1_000)

	tests := []struct {
		name    string
		account uuid.UUID
		cursor  string
		limit   int
		want    error
	}{
		{"limit above 100", ana, "", 101, ledger.ErrInvalidRequest},
		{"negative limit", ana, "", -1, ledger.ErrInvalidRequest},
		{"cursor that is not ours", ana, "not-a-cursor", 10, ledger.ErrInvalidRequest},
		{"unknown account", uuid.New(), "", 10, ledger.ErrAccountNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := svc.History(t.Context(), tt.account, tt.cursor, tt.limit); !errors.Is(err, tt.want) {
				t.Errorf("History = %v, want %v", err, tt.want)
			}
		})
	}
}

func fund(t *testing.T, svc *ledger.Service, to uuid.UUID, reference string, amount ledger.Amount) {
	t.Helper()
	if _, err := svc.Post(t.Context(), request(reference, ledger.KindCashIn, cashInCOP, -amount, to, amount)); err != nil {
		t.Fatalf("fund %s: %v", reference, err)
	}
}

func seqsOf(entries []ledger.HistoryEntry) []int64 {
	seqs := make([]int64, len(entries))
	for i, e := range entries {
		seqs[i] = e.Seq
	}
	return seqs
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
