CREATE TABLE IF NOT EXISTS traces (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_slug                TEXT NOT NULL,
    prompt                   TEXT NOT NULL,
    title                    TEXT NOT NULL,
    affected_files           TEXT NOT NULL DEFAULT '[]',

    current_data             TEXT,
    current_snapshot_id      INTEGER REFERENCES graph_snapshots(id),
    current_generated_at     DATETIME,
    current_status           TEXT NOT NULL DEFAULT 'generating',
    current_error            TEXT,

    previous_data            TEXT,
    previous_snapshot_id     INTEGER REFERENCES graph_snapshots(id),
    previous_generated_at    DATETIME,
    previous_change_summary  TEXT,

    created_at               DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_traces_repo_slug ON traces(repo_slug);
