package postgres

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestVerifyExistingSchema(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(sqlmock.Sqlmock)
		want    string
		repair  bool
	}{
		{
			name: "provisioned schema succeeds with reads only",
			prepare: func(mock sqlmock.Sqlmock) {
				expectExistingSchema(mock, true, true)
				mock.ExpectQuery(regexp.QuoteMeta(existingSchemaVersionQuery)).
					WithArgs(schemaVersionKey).
					WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(schemaVersion))
				expectExistingCapabilities(mock, true, true)
			},
		},
		{
			name: "missing schema fails without provisioning it",
			prepare: func(mock sqlmock.Sqlmock) {
				expectExistingSchema(mock, false, false)
			},
			want: "does not exist",
		},
		{
			name: "missing metadata fails without repair classification",
			prepare: func(mock sqlmock.Sqlmock) {
				expectExistingSchema(mock, true, false)
			},
			want: "missing metadata table",
		},
		{
			name: "missing version fails without repair classification",
			prepare: func(mock sqlmock.Sqlmock) {
				expectExistingSchema(mock, true, true)
				mock.ExpectQuery(regexp.QuoteMeta(existingSchemaVersionQuery)).
					WithArgs(schemaVersionKey).
					WillReturnRows(sqlmock.NewRows([]string{"value"}))
			},
			want: "missing schema version",
		},
		{
			name: "wrong version fails without migration",
			prepare: func(mock sqlmock.Sqlmock) {
				expectExistingSchema(mock, true, true)
				mock.ExpectQuery(regexp.QuoteMeta(existingSchemaVersionQuery)).
					WithArgs(schemaVersionKey).
					WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("wrong-version"))
			},
			want: "this binary requires " + schemaVersion,
		},
		{
			name: "missing journal payload requests known legacy repair",
			prepare: func(mock sqlmock.Sqlmock) {
				expectExistingSchema(mock, true, true)
				mock.ExpectQuery(regexp.QuoteMeta(existingSchemaVersionQuery)).
					WithArgs(schemaVersionKey).
					WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(schemaVersion))
				expectExistingCapabilities(mock, false, true)
			},
			want:   "bd_events_journal.comment_json",
			repair: true,
		},
		{
			name: "missing event sequence requests known legacy repair",
			prepare: func(mock sqlmock.Sqlmock) {
				expectExistingSchema(mock, true, true)
				mock.ExpectQuery(regexp.QuoteMeta(existingSchemaVersionQuery)).
					WithArgs(schemaVersionKey).
					WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(schemaVersion))
				expectExistingCapabilities(mock, true, false)
			},
			want:   "bd_events_seq",
			repair: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			tt.prepare(mock)
			err = verifyExistingSchema(t.Context(), db, "workspace")
			if tt.want == "" {
				if err != nil {
					t.Fatalf("verifyExistingSchema: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("verifyExistingSchema error = %v, want %q", err, tt.want)
			}
			if got := errors.Is(err, ErrExistingSchemaNeedsRepair); got != tt.repair {
				t.Fatalf("errors.Is(ErrExistingSchemaNeedsRepair) = %v, want %v (error %v)", got, tt.repair, err)
			}
			// No Exec expectation is registered. Any CREATE, ALTER, INSERT, or
			// other DDL/DML issued by verification fails this recorder assertion.
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func expectExistingCapabilities(mock sqlmock.Sqlmock, journalPayload, eventSequence bool) {
	mock.ExpectQuery(regexp.QuoteMeta(existingSchemaCapabilitiesQuery)).
		WithArgs("workspace", "workspace").
		WillReturnRows(sqlmock.NewRows([]string{"journal_payload", "event_sequence"}).AddRow(journalPayload, eventSequence))
}

func expectExistingSchema(mock sqlmock.Sqlmock, schemaExists, metadataExists bool) {
	mock.ExpectQuery(regexp.QuoteMeta(existingSchemaQuery)).
		WithArgs("workspace").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(schemaExists))
	if schemaExists {
		mock.ExpectQuery(regexp.QuoteMeta(existingMetadataTableQuery)).
			WithArgs("workspace").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(metadataExists))
	}
}
