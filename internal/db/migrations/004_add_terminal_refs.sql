-- Store adapter-native terminal refs for session/window lifecycle tracking.
-- For tmux these are human-readable names; for cmux they're opaque workspace/surface refs.
ALTER TABLE claude_tasks ADD COLUMN terminal_target TEXT NOT NULL DEFAULT '';
ALTER TABLE repos ADD COLUMN session_ref TEXT NOT NULL DEFAULT '';
