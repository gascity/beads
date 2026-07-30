//go:build cgo

package beads

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

func TestNewIssueOperationsBuildsEmbeddedAdapter(t *testing.T) {
	operations, err := NewIssueOperations(&embeddeddolt.EmbeddedDoltStore{})
	if err != nil {
		t.Fatalf("NewIssueOperations() error = %v", err)
	}
	if operations == nil {
		t.Fatal("NewIssueOperations() returned nil operations")
	}
}

func TestNewIssueOperationsRejectsNilEmbeddedStore(t *testing.T) {
	operations, err := NewIssueOperations((*embeddeddolt.EmbeddedDoltStore)(nil))
	if operations != nil {
		t.Fatalf("NewIssueOperations() operations = %T, want nil", operations)
	}
	var unsupported *storage.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("NewIssueOperations() error = %v, want *storage.ErrUnsupported", err)
	}
}
