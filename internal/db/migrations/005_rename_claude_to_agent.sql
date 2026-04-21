-- Rename claude-specific task names to agent-agnostic names
ALTER TABLE claude_tasks RENAME TO agent_tasks;
ALTER TABLE agent_tasks RENAME COLUMN claude_session_id TO agent_session_id;
ALTER TABLE agent_tasks ADD COLUMN agent TEXT NOT NULL DEFAULT 'claude';
