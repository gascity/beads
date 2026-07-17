package beads

import (
	"errors"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
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

// alreadyClaimedMarker and notClaimableMarker are derived at package-init time
// from the storage layer's sentinel + format fragment, NOT hardcoded literals,
// so the producer (issueops/claim.go) and this parser cannot drift: both spell
// the conflict message as "<sentinel><fragment><token>". A change to either the
// sentinel text or the fragment moves both ends in lockstep, and the producer-
// tied round-trip test (claim_roundtrip_test.go / the dolt suite) is the
// tripwire if the producer stops using the fragment at all.
var (
	alreadyClaimedMarker = storage.ErrAlreadyClaimed.Error() + storage.ClaimedByFragment
	notClaimableMarker   = storage.ErrNotClaimable.Error() + storage.NotClaimableStatusFragment
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
//
// The recovered token (assignee/status) is the message's trailing run BY REPO
// CONVENTION: claim-error wraps PREPEND context ("caller ctx: %w"), never append
// it, so the fragment appears once near the end and its tail is the bare token.
// As a guard against a future appended wrap corrupting the token, if the tail
// still contains a known claim marker we treat it as ambiguous and return "" —
// a recovered-empty field is the documented best-effort failure mode, and the
// round-trip test would go red before any garbage token could reach a caller.
func tailAfter(s, marker string) string {
	i := strings.LastIndex(s, marker)
	if i < 0 {
		return ""
	}
	tail := s[i+len(marker):]
	if strings.Contains(tail, alreadyClaimedMarker) || strings.Contains(tail, notClaimableMarker) {
		return ""
	}
	return tail
}
