package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
)

func TestValidateEventsJournalBackend(t *testing.T) {
	for _, backend := range []string{"", configfile.BackendPostgres} {
		if err := validateEventsJournalBackend(backend, true); err != nil {
			t.Errorf("supported backend %q rejected: %v", backend, err)
		}
	}
	for _, backend := range []string{configfile.BackendMySQL, configfile.BackendSQLite} {
		if err := validateEventsJournalBackend(backend, true); err == nil {
			t.Errorf("unsupported backend %q accepted journal activation", backend)
		}
		if err := validateEventsJournalBackend(backend, false); err != nil {
			t.Errorf("disabled journal should not reject backend %q: %v", backend, err)
		}
	}
}

type recordingEventsJournalUseCase struct {
	rows []storage.EventsJournalRow

	readSince int64
	readLimit int

	pruneBefore     int64
	pruneRetainDays int
	pruneRetainRows int
	pruned          int64
}

func (u *recordingEventsJournalUseCase) Read(_ context.Context, since int64, limit int) ([]storage.EventsJournalRow, error) {
	u.readSince = since
	u.readLimit = limit
	return u.rows, nil
}

func (u *recordingEventsJournalUseCase) Prune(_ context.Context, before int64, retainDays, retainRows int) (int64, error) {
	u.pruneBefore = before
	u.pruneRetainDays = retainDays
	u.pruneRetainRows = retainRows
	return u.pruned, nil
}

type journalTestUOW struct {
	journal     domain.EventsJournalUseCase
	commitCount int
}

func (u *journalTestUOW) Close(context.Context)                {}
func (u *journalTestUOW) Commit(context.Context, string) error { u.commitCount++; return nil }
func (u *journalTestUOW) ConfigUseCase() domain.ConfigUseCase  { panic("not used") }
func (u *journalTestUOW) DoltRemoteUseCase() domain.DoltRemoteUseCase {
	panic("not used")
}
func (u *journalTestUOW) BootstrapUseCase() domain.BootstrapUseCase   { panic("not used") }
func (u *journalTestUOW) IssueUseCase() domain.IssueUseCase           { panic("not used") }
func (u *journalTestUOW) DependencyUseCase() domain.DependencyUseCase { panic("not used") }
func (u *journalTestUOW) LabelUseCase() domain.LabelUseCase           { panic("not used") }
func (u *journalTestUOW) CommentUseCase() domain.CommentUseCase       { panic("not used") }
func (u *journalTestUOW) EventsJournalUseCase() domain.EventsJournalUseCase {
	return u.journal
}

type journalTestProvider struct {
	uw *journalTestUOW
}

func (p *journalTestProvider) NewUOW(context.Context) (uow.UnitOfWork, error) { return p.uw, nil }
func (p *journalTestProvider) Close(context.Context) error                    { return nil }

func installProxiedJournalTestProvider(t *testing.T, p uow.UnitOfWorkProvider) {
	t.Helper()
	oldProvider := uowProvider
	oldProxied := proxiedServerMode
	oldCmdCtx := cmdCtx
	oldTestMode := testModeUseGlobals
	t.Cleanup(func() {
		uowProvider = oldProvider
		proxiedServerMode = oldProxied
		cmdCtx = oldCmdCtx
		testModeUseGlobals = oldTestMode
	})
	t.Setenv("BD_SPIKE_UOWSTORE", "")
	testModeUseGlobals = true
	cmdCtx = nil
	proxiedServerMode = true
	uowProvider = p
}

func TestReadJournalUsesProxiedUOWCursor(t *testing.T) {
	journal := &recordingEventsJournalUseCase{
		rows: []storage.EventsJournalRow{{
			Seq:       12,
			TS:        "2026-07-29T10:00:00Z",
			Op:        "update",
			IssueID:   "bd-12",
			IssueJSON: `{"id":"bd-12"}`,
		}},
	}
	provider := &journalTestProvider{uw: &journalTestUOW{journal: journal}}
	installProxiedJournalTestProvider(t, provider)

	rows, err := readJournal(context.Background(), 11, 25)
	if err != nil {
		t.Fatalf("readJournal: %v", err)
	}
	if journal.readSince != 11 || journal.readLimit != 25 {
		t.Fatalf("proxied cursor = (since=%d, limit=%d), want (11, 25)", journal.readSince, journal.readLimit)
	}
	if len(rows) != 1 || rows[0].Seq != 12 || rows[0].IssueID != "bd-12" {
		t.Fatalf("readJournal rows = %+v", rows)
	}
}

func TestPruneJournalUsesCommittedProxiedUOW(t *testing.T) {
	journal := &recordingEventsJournalUseCase{pruned: 7}
	uw := &journalTestUOW{journal: journal}
	installProxiedJournalTestProvider(t, &journalTestProvider{uw: uw})

	n, err := pruneJournal(context.Background(), 100, 14, 500)
	if err != nil {
		t.Fatalf("pruneJournal: %v", err)
	}
	if n != 7 {
		t.Fatalf("pruned = %d, want 7", n)
	}
	if journal.pruneBefore != 100 || journal.pruneRetainDays != 14 || journal.pruneRetainRows != 500 {
		t.Fatalf("proxied prune args = (before=%d, days=%d, rows=%d), want (100, 14, 500)",
			journal.pruneBefore, journal.pruneRetainDays, journal.pruneRetainRows)
	}
	if uw.commitCount != 1 {
		t.Fatalf("proxied prune commits = %d, want 1", uw.commitCount)
	}
}
