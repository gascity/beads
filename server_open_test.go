package beads

import (
	"context"
	"strings"
	"testing"
)

// TestOpenServerRequiresSchemaOwnership pins the fail-loud half of the contract:
// a ServerConfig that says nothing about who owns the schema is refused before
// any dial, rather than being read as "this process may migrate the database".
//
// The addresses below are deliberately unroutable. If the guard ever regresses
// into a default, these calls reach the network instead of returning
// immediately, and the assertions on the message catch it.
func TestOpenServerRequiresSchemaOwnership(t *testing.T) {
	ctx := context.Background()

	t.Run("unset is refused", func(t *testing.T) {
		_, err := OpenServer(ctx, ServerConfig{Host: "192.0.2.1", Port: 3307, Database: "bd_tenant"})
		if err == nil {
			t.Fatal("OpenServer with no SchemaOwnership returned nil error; an undeclared caller must not inherit DDL-on-open")
		}
		for _, want := range []string{"SchemaOwnership", "SchemaOwnedHere", "SchemaOwnedElsewhere", "bd_tenant"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %q, want it to mention %q", err, want)
			}
		}
	})

	t.Run("unknown value is refused", func(t *testing.T) {
		_, err := OpenServer(ctx, ServerConfig{Host: "192.0.2.1", Port: 3307, Database: "bd_tenant", SchemaOwnership: SchemaOwnership(42)})
		if err == nil {
			t.Fatal("OpenServer with an unknown SchemaOwnership returned nil error")
		}
		if !strings.Contains(err.Error(), "unknown SchemaOwnership") {
			t.Fatalf("error = %q, want it to name the unknown value", err)
		}
	})

	t.Run("CreateIfMissing needs ownership", func(t *testing.T) {
		_, err := OpenServer(ctx, ServerConfig{
			Host: "192.0.2.1", Port: 3307, Database: "bd_tenant",
			CreateIfMissing: true, SchemaOwnership: SchemaOwnedElsewhere,
		})
		if err == nil {
			t.Fatal("OpenServer accepted CreateIfMissing without schema ownership; that creates a database nothing may then populate")
		}
		if !strings.Contains(err.Error(), "CreateIfMissing") {
			t.Fatalf("error = %q, want it to name CreateIfMissing", err)
		}
	})
}

// TestSchemaOwnershipZeroValueIsUnset guards the one property the fail-loud
// guard rests on: the zero value must be the invalid one, so a struct literal
// that omits the field is refused rather than silently permitted.
func TestSchemaOwnershipZeroValueIsUnset(t *testing.T) {
	var zero SchemaOwnership
	if zero != SchemaOwnershipUnset {
		t.Fatalf("zero SchemaOwnership = %d, want SchemaOwnershipUnset (%d)", zero, SchemaOwnershipUnset)
	}
	if SchemaOwnedHere == SchemaOwnershipUnset || SchemaOwnedElsewhere == SchemaOwnershipUnset {
		t.Fatal("a usable SchemaOwnership shares the zero value; omitting the field would then be valid")
	}
}
