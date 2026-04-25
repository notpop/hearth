package sqlite

// schemaSQL is the canonical schema. It's applied on first open and is
// idempotent. Future changes will be appended as additional statements
// guarded by version checks; for now the design is fluid enough that we
// just rebuild the file when shapes change.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS jobs (
  id                  TEXT PRIMARY KEY,
  kind                TEXT NOT NULL,
  state               INTEGER NOT NULL,
  attempt             INTEGER NOT NULL DEFAULT 0,
  max_attempts        INTEGER NOT NULL,
  payload             BLOB,
  blobs_json          TEXT,
  lease_ttl_ns        INTEGER NOT NULL,
  backoff_initial_ns  INTEGER NOT NULL DEFAULT 0,
  backoff_max_ns      INTEGER NOT NULL DEFAULT 0,
  backoff_multiplier  REAL    NOT NULL DEFAULT 0,
  backoff_jitter      REAL    NOT NULL DEFAULT 0,
  worker_id           TEXT,
  leased_at_ns        INTEGER,
  expires_at_ns       INTEGER,
  result_payload      BLOB,
  result_blobs_json   TEXT,
  last_error          TEXT,
  next_run_at_ns      INTEGER NOT NULL DEFAULT 0,
  created_at_ns       INTEGER NOT NULL,
  updated_at_ns       INTEGER NOT NULL
);

-- Lookup index for the LeaseNext path (queued jobs, oldest first within a kind).
CREATE INDEX IF NOT EXISTS jobs_lease_idx
  ON jobs (state, kind, next_run_at_ns, created_at_ns);

-- Lookup index for the ReclaimExpired sweep (leased jobs ordered by expiry).
CREATE INDEX IF NOT EXISTS jobs_reclaim_idx
  ON jobs (state, expires_at_ns);

-- Updated-at index for List queries.
CREATE INDEX IF NOT EXISTS jobs_updated_idx
  ON jobs (updated_at_ns DESC);
`
