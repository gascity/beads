//go:build cgo

package beads

import (
	"reflect"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/issueops"
)

func newEmbeddedIssueOperations(store any) (issueops.Operations, error) {
	if backend, ok := store.(*embeddeddolt.EmbeddedDoltStore); ok {
		return embeddeddolt.NewIssueOperations(backend)
	}
	return nil, &storage.ErrUnsupported{Op: "NewIssueOperations", Backend: reflect.TypeOf(store).String()}
}
