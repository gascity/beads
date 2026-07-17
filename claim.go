package beads

import (
	"errors"
	"strings"
)

// ClaimConflict describes why a claim failed, recovered from the claim error.
//
// The engine's conditional-UPDATE claim path returns no typed conflict result:
// on ErrAlreadyClaimed it embeds the current assignee in the message
// ("issue already claimed by <assignee>"), and on ErrNotClaimable it embeds the
// current status ("issue not claimable: status <status>"). ClaimConflict
// carries whichever of those was recoverable; the other stays empty. This is a
// deliberately string-coupled shim — a typed conflict result would require
// changing the internal claim signature, which v1 does not.
type ClaimConflict struct {
	// CurrentAssignee is the actor currently holding the issue. Set when the
	// error wraps ErrAlreadyClaimed and the assignee was parseable.
	CurrentAssignee string
	// CurrentStatus is the issue's status that made it unclaimable. Set when
	// the error wraps ErrNotClaimable and the status was parseable.
	CurrentStatus string
}

const (
	alreadyClaimedMarker = "issue already claimed by "
	notClaimableMarker   = "issue not claimable: status "
)

// ParseClaimConflict inspects a claim error and, when it wraps ErrAlreadyClaimed
// or ErrNotClaimable, returns the recovered conflict detail and true. For any
// other error (including nil) it returns the zero ClaimConflict and false.
//
// Parsing keys on the message fragment the engine appends after the sentinel,
// located with LastIndex so that outer "context: %w" wrapping (which prepends)
// does not defeat it. Fields are best-effort: an Is-match with an unparseable
// message still returns true with the corresponding field empty.
func ParseClaimConflict(err error) (ClaimConflict, bool) {
	switch {
	case errors.Is(err, ErrAlreadyClaimed):
		return ClaimConflict{CurrentAssignee: tailAfter(err.Error(), alreadyClaimedMarker)}, true
	case errors.Is(err, ErrNotClaimable):
		return ClaimConflict{CurrentStatus: tailAfter(err.Error(), notClaimableMarker)}, true
	default:
		return ClaimConflict{}, false
	}
}

// tailAfter returns the substring following the last occurrence of marker in s,
// or "" when marker is absent.
func tailAfter(s, marker string) string {
	if i := strings.LastIndex(s, marker); i >= 0 {
		return s[i+len(marker):]
	}
	return ""
}
