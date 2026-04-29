CREATE TABLE IF NOT EXISTS graph_snapshots (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_path        TEXT NOT NULL,
    repo_slug        TEXT NOT NULL,
    commit_sha       TEXT NOT NULL,
    built_at         DATETIME NOT NULL,
    status           TEXT NOT NULL,
    node_count       INTEGER,
    edge_count       INTEGER,
    community_count  INTEGER,
    duration_ms      INTEGER,
    error            TEXT NOT NULL DEFAULT '',
    UNIQUE(repo_slug, commit_sha)
);

CREATE INDEX IF NOT EXISTS idx_graph_snapshots_repo ON graph_snapshots(repo_slug, built_at DESC);
