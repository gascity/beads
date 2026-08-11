package postgres

import (
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
	}{
		{
			name: "provisioned schema succeeds with reads only",
			prepare: func(mock sqlmock.Sqlmock) {
				expectExistingSchema(mock, true, true)
				mock.ExpectQuery(regexp.QuoteMeta(existingSchemaVersionQuery)).
					WithArgs(schemaVersionKey).
					WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(schemaVersion))
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
			name: "wrong version fails without migration",
			prepare: func(mock sqlmock.Sqlmock) {
				expectExistingSchema(mock, true, true)
				mock.ExpectQuery(regexp.QuoteMeta(existingSchemaVersionQuery)).
					WithArgs(schemaVersionKey).
					WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("wrong-version"))
			},
			want: "this binary requires " + schemaVersion,
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
			// No Exec expectation is registered. Any CREATE, ALTER, INSERT, or
			// other DDL/DML issued by verification fails this recorder assertion.
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
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
