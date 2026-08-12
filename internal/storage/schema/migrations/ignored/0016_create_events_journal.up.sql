-- Materialize the clone-local events journal without changing the durable
-- schema version. This integration line already uses durable migrations
-- through 0059, so the journal occupies the next clone-local ignored slot.
--
-- bd_events_journal is dolt_ignored (seeded by MigrateUp's doltIgnorePatterns
-- before this migration runs): operational, never versioned or federated, so its
-- seq stays monotonic with no merge conflict. seq is NOT AUTO_INCREMENT — it is
-- drawn from the single-row bd_events_seq counter inside the mutation's own
-- transaction so concurrent allocators conflict and the surviving seqs are
-- gapless and commit-ordered (see issueops.nextEventSeq).
-- Same __temp__ + conditional RENAME pattern for both tables: create only when
-- absent, never touch an existing table. See issueops.RecordEventInTx for the writer.
INSERT IGNORE INTO dolt_ignore VALUES ('bd_events_journal', true);
INSERT IGNORE INTO dolt_ignore VALUES ('bd_events_seq', true);

DROP TABLE IF EXISTS __temp__bd_events_journal;
CREATE TABLE __temp__bd_events_journal (
    seq BIGINT NOT NULL PRIMARY KEY,
    ts DATETIME NOT NULL,
    op VARCHAR(32) NOT NULL,
    issue_id VARCHAR(255) NOT NULL,
    issue_json LONGTEXT,
    dep_json TEXT,
    comment_json TEXT,
    INDEX idx_bd_events_journal_issue (issue_id)
);
SET @exists = (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bd_events_journal');
SET @sql = IF(@exists = 0, 'RENAME TABLE __temp__bd_events_journal TO bd_events_journal', 'DROP TABLE __temp__bd_events_journal');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

DROP TABLE IF EXISTS __temp__bd_events_seq;
CREATE TABLE __temp__bd_events_seq (
    id INT NOT NULL PRIMARY KEY,
    next_seq BIGINT NOT NULL
);
SET @seq_exists = (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bd_events_seq');
SET @seq_sql = IF(@seq_exists = 0, 'RENAME TABLE __temp__bd_events_seq TO bd_events_seq', 'DROP TABLE __temp__bd_events_seq');
PREPARE stmt FROM @seq_sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
-- Seed (idempotent), then raise to the journal high-water mark. This is
-- VALUES + GREATEST rather than INSERT ... SELECT so the empty-journal case
-- always materializes the counter row.
INSERT IGNORE INTO bd_events_seq (id, next_seq) VALUES (0, 0);
UPDATE bd_events_seq
    SET next_seq = GREATEST(next_seq, COALESCE((SELECT MAX(seq) FROM bd_events_journal), 0))
    WHERE id = 0;
