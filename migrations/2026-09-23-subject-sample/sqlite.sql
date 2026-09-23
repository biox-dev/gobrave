-- Add go_subject + go_sample (Subject -> Sample -> Assay) — SQLite
-- See README.md in this folder for context.
-- Idempotent: safe to run when the new binary already ran AutoMigrate.

CREATE TABLE IF NOT EXISTS go_subject (
  id         INTEGER      NOT NULL,
  subject_id VARCHAR(255) NOT NULL,
  species    VARCHAR(128) NULL,
  strain     VARCHAR(128) NULL,
  sex        VARCHAR(32)  NULL,
  age        VARCHAR(64)  NULL,
  metadata   TEXT         NULL,
  created_at DATETIME     NULL,
  updated_at DATETIME     NULL,
  PRIMARY KEY (id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_go_subject_subject_id ON go_subject (subject_id);

CREATE TABLE IF NOT EXISTS go_sample (
  id              INTEGER      NOT NULL,
  sample_id       VARCHAR(255) NOT NULL,
  sample_name     VARCHAR(255) NULL,
  subject_id      INTEGER      NOT NULL,
  tissue          VARCHAR(128) NULL,
  cell_type       VARCHAR(128) NULL,
  collection_time DATETIME     NULL,
  metadata        TEXT         NULL,
  description     TEXT         NULL,
  created_at      DATETIME     NULL,
  updated_at      DATETIME     NULL,
  PRIMARY KEY (id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_go_sample_sample_id ON go_sample (sample_id);
CREATE INDEX IF NOT EXISTS idx_go_sample_subject_id ON go_sample (subject_id);
