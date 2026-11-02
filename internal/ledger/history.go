package ledger

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sebasBetancourt/Ledger/internal/ledger/ledgerdb"
)

const (
	defaultHistoryLimit = 25
	maxHistoryLimit     = 100
	cursorPrefix        = "seq:"
)

// HistoryEntry is one posting as an account's owner sees it.
type HistoryEntry struct {
	ID            uuid.UUID
	TransactionID uuid.UUID
	Amount        Amount
	Currency      Currency
	Seq           int64
	BalanceBefore Amount
	BalanceAfter  Amount
	CreatedAt     time.Time
}

// HistoryPage holds entries newest first. NextCursor is empty on the last page.
type HistoryPage struct {
	Entries    []HistoryEntry
	NextCursor string
}

// History returns an account's postings newest first, limit at a time (0 means
// 25, at most 100). Pass a page's NextCursor to read the next one.
//
// It pages by seq, never by OFFSET, which rereads every skipped row and
// repeats rows when new ones arrive. Nor by created_at: that is the start time
// of a transaction that may commit later, so a date cursor can skip rows.
func (s *Service) History(ctx context.Context, accountID uuid.UUID, cursor string, limit int) (HistoryPage, error) {
	if limit == 0 {
		limit = defaultHistoryLimit
	}
	if limit < 1 || limit > maxHistoryLimit {
		return HistoryPage{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidRequest, maxHistoryLimit)
	}
	beforeSeq, err := decodeCursor(cursor)
	if err != nil {
		return HistoryPage{}, err
	}

	q := ledgerdb.New(s.pool)
	if _, err := q.GetAccount(ctx, accountID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return HistoryPage{}, fmt.Errorf("%w: %s", ErrAccountNotFound, accountID)
		}
		return HistoryPage{}, err
	}

	// One extra row tells whether another page exists.
	rows, err := q.ListPostingsByAccount(ctx, ledgerdb.ListPostingsByAccountParams{
		AccountID: accountID,
		BeforeSeq: beforeSeq,
		PageSize:  int32(limit + 1), //nolint:gosec // limit is at most maxHistoryLimit
	})
	if err != nil {
		return HistoryPage{}, fmt.Errorf("list postings: %w", err)
	}

	var page HistoryPage
	if len(rows) > limit {
		rows = rows[:limit]
		page.NextCursor = encodeCursor(rows[len(rows)-1].Seq)
	}
	page.Entries = make([]HistoryEntry, len(rows))
	for i, p := range rows {
		page.Entries[i] = HistoryEntry{
			ID:            p.ID,
			TransactionID: p.TransactionID,
			Amount:        Amount(p.Amount),
			Currency:      Currency(p.Currency),
			Seq:           p.Seq,
			BalanceBefore: Amount(p.BalanceBefore),
			BalanceAfter:  Amount(p.BalanceAfter),
			CreatedAt:     p.CreatedAt,
		}
	}
	return page, nil
}

// The cursor is opaque to clients, so how paging works can change without
// breaking them.
func encodeCursor(seq int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(cursorPrefix + strconv.FormatInt(seq, 10)))
}

func decodeCursor(cursor string) (*int64, error) {
	if cursor == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err == nil {
		if rest, ok := strings.CutPrefix(string(raw), cursorPrefix); ok {
			if seq, err := strconv.ParseInt(rest, 10, 64); err == nil && seq > 0 {
				return &seq, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: invalid cursor", ErrInvalidRequest)
}
