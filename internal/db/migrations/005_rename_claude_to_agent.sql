-- Rename claude-specific task names to agent-agnostic names.
-- ensureSchema() may have already created an empty agent_tasks table;
-- drop it so we can rename claude_tasks (which has the real data).
DROP TABLE IF EXISTS agent_tasks;
ALTER TABLE claude_tasks RENAME TO agent_tasks;
ALTER TABLE agent_tasks RENAME COLUMN claude_session_id TO agent_session_id;
ALTER TABLE agent_tasks ADD COLUMN agent TEXT NOT NULL DEFAULT 'claude';
