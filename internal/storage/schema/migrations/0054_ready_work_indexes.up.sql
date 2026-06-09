-- Migration 0054: add ready-work hot-path indexes.
--
-- These indexes support bd ready / list --ready page queries used by
-- dispatchers and hook probes. The migration number sits past the 0050-0053
-- range now occupied on main so long-lived databases that have already recorded
-- those versions still apply these indexes (earlier drafts used 0027, then 0050).

SET @needs_index = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'dependencies'
      AND INDEX_NAME = 'idx_dependencies_type_issue_target'
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_dependencies_type_issue_target ON dependencies (type, issue_id, depends_on_issue_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_index = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND INDEX_NAME = 'idx_issues_ready_assignee'
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_issues_ready_assignee ON issues (assignee, status, priority, created_at, id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_index = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND INDEX_NAME = 'idx_issues_ready_status'
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_issues_ready_status ON issues (status, priority, created_at, id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_index = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND INDEX_NAME = 'idx_issues_defer_until'
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_issues_defer_until ON issues (defer_until)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_index = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND INDEX_NAME = 'idx_wisp_dependencies_type_issue_target'
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_wisp_dependencies_type_issue_target ON wisp_dependencies (type, issue_id, depends_on_issue_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_index = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND INDEX_NAME = 'idx_wisps_ready_assignee'
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_wisps_ready_assignee ON wisps (assignee, status, priority, created_at, id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_index = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND INDEX_NAME = 'idx_wisps_ready_status'
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_wisps_ready_status ON wisps (status, priority, created_at, id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_index = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND INDEX_NAME = 'idx_wisps_defer_until'
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_wisps_defer_until ON wisps (defer_until)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
