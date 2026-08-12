-- The journal is clone-local, so add the replayable comment payload column
-- through the ignored migration stream. Existing clones already carrying the
-- journal table receive this additive upgrade; fresh clones get it from 0016.
SET @comment_json_exists = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'bd_events_journal'
      AND COLUMN_NAME = 'comment_json'
);
SET @comment_json_sql = IF(
    @comment_json_exists = 0,
    'ALTER TABLE bd_events_journal ADD COLUMN comment_json TEXT',
    'SELECT 1'
);
PREPARE stmt FROM @comment_json_sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
