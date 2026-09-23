-- Add go_subject + go_sample (Subject -> Sample -> Assay) — MySQL
-- See README.md in this folder for context.
-- Idempotent: safe to run when the new binary already ran AutoMigrate.
-- InnoDB does not roll back DDL, so take a backup first.

-- ---------------------------------------------------------------------------
-- go_subject
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS go_subject (
  id         BIGINT       NOT NULL,
  subject_id VARCHAR(255) NOT NULL,
  species    VARCHAR(128) NULL,
  strain     VARCHAR(128) NULL,
  sex        VARCHAR(32)  NULL,
  age        VARCHAR(64)  NULL,
  metadata   TEXT         NULL,
  created_at DATETIME(3)  NULL,
  updated_at DATETIME(3)  NULL,
  PRIMARY KEY (id),
  UNIQUE KEY idx_go_subject_subject_id (subject_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- ---------------------------------------------------------------------------
-- go_sample
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS go_sample (
  id              BIGINT       NOT NULL,
  sample_id       VARCHAR(255) NOT NULL,
  sample_name     VARCHAR(255) NULL,
  subject_id      BIGINT       NOT NULL,
  tissue          VARCHAR(128) NULL,
  cell_type       VARCHAR(128) NULL,
  collection_time DATETIME(3)  NULL,
  metadata        TEXT         NULL,
  description     TEXT         NULL,
  created_at      DATETIME(3)  NULL,
  updated_at      DATETIME(3)  NULL,
  PRIMARY KEY (id),
  UNIQUE KEY idx_go_sample_sample_id (sample_id),
  KEY idx_go_sample_subject_id (subject_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;
