ALTER TABLE claude_tasks ADD COLUMN parent_id INTEGER REFERENCES claude_tasks(id);

-- Migrate in-flight tasks to simplified status model
UPDATE claude_tasks SET status = 'running' WHERE status IN ('implementing', 'refactoring');
UPDATE claude_tasks SET status = 'completed' WHERE status IN ('resolved', 'done');
