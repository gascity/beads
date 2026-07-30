package beads

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

func TestNewIssueOperationsBuildsRawAdapters(t *testing.T) {
	tests := []struct {
		name  string
		store Storage
	}{
		{name: "dolt", store: &dolt.DoltStore{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations, err := NewIssueOperations(test.store)
			if err != nil {
				t.Fatalf("NewIssueOperations() error = %v", err)
			}
			if operations == nil {
				t.Fatal("NewIssueOperations() returned nil operations")
			}
		})
	}
}

func TestNewIssueOperationsRejectsUnsupportedStorage(t *testing.T) {
	tests := []struct {
		name  string
		store Storage
	}{
		{name: "nil interface"},
		{name: "nil dolt", store: (*dolt.DoltStore)(nil)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations, err := NewIssueOperations(test.store)
			if operations != nil {
				t.Fatalf("NewIssueOperations() operations = %T, want nil", operations)
			}
			var unsupported *storage.ErrUnsupported
			if !errors.As(err, &unsupported) {
				t.Fatalf("NewIssueOperations() error = %v, want *storage.ErrUnsupported", err)
			}
			if unsupported.Op != "NewIssueOperations" {
				t.Errorf("unsupported operation = %q, want NewIssueOperations", unsupported.Op)
			}
		})
	}
}
