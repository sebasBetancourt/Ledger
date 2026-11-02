package ledger

import "github.com/google/uuid"

// IDGenerator creates the IDs of new rows. Tests pass a fixed sequence so they
// know every ID in advance.
type IDGenerator interface {
	New() uuid.UUID
}

// UUIDv7 generates time-ordered IDs, which keep primary key indexes compact.
type UUIDv7 struct{}

// New panics only if the system's random source fails.
func (UUIDv7) New() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}
