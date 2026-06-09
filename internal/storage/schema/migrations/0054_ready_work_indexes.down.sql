SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND INDEX_NAME = 'idx_wisps_defer_until'
);
SET @sql = IF(@needs_drop = 1,
    'DROP INDEX idx_wisps_defer_until ON wisps',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND INDEX_NAME = 'idx_wisps_ready_status'
);
SET @sql = IF(@needs_drop = 1,
    'DROP INDEX idx_wisps_ready_status ON wisps',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND INDEX_NAME = 'idx_wisps_ready_assignee'
);
SET @sql = IF(@needs_drop = 1,
    'DROP INDEX idx_wisps_ready_assignee ON wisps',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND INDEX_NAME = 'idx_wisp_dependencies_type_issue_target'
);
SET @sql = IF(@needs_drop = 1,
    'DROP INDEX idx_wisp_dependencies_type_issue_target ON wisp_dependencies',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND INDEX_NAME = 'idx_issues_defer_until'
);
SET @sql = IF(@needs_drop = 1,
    'DROP INDEX idx_issues_defer_until ON issues',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND INDEX_NAME = 'idx_issues_ready_status'
);
SET @sql = IF(@needs_drop = 1,
    'DROP INDEX idx_issues_ready_status ON issues',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND INDEX_NAME = 'idx_issues_ready_assignee'
);
SET @sql = IF(@needs_drop = 1,
    'DROP INDEX idx_issues_ready_assignee ON issues',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'dependencies'
      AND INDEX_NAME = 'idx_dependencies_type_issue_target'
);
SET @sql = IF(@needs_drop = 1,
    'DROP INDEX idx_dependencies_type_issue_target ON dependencies',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
