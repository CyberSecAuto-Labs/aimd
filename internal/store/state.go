package store

import "fmt"

// SyncState describes the relationship between local and remote HEAD.
type SyncState int

const (
	// StateUpToDate means local and remote HEADs are identical.
	StateUpToDate SyncState = iota
	// StateBehind means remote has commits that local does not.
	StateBehind
	// StateAhead means local has commits that remote does not.
	StateAhead
	// StateDiverged means both local and remote have commits the other lacks.
	StateDiverged
	// StateConflict means a rebase conflict was encountered mid-sync.
	StateConflict
)

// ConflictError is returned when a rebase conflict is encountered.
// It implements the error interface and carries the list of conflicted files.
type ConflictError struct {
	Files []string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("rebase conflict in %d file(s): %v", len(e.Files), e.Files)
}
