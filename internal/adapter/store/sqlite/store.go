// Package sqlite is the durable Store implementation backed by SQLite in
// WAL mode. It uses modernc.org/sqlite (pure-Go, no cgo) so the resulting
// binary cross-compiles cleanly to every platform Hearth targets.
//
// Concurrency model: a single *sql.DB is used with MaxOpenConns=1, so all
// SQL goes through one connection serially. SQLite WAL would allow
// concurrent readers, but mixing writes from multiple goroutines on a
// single file invites SQLITE_BUSY retries and tail-latency spikes; for
// home-network workloads, serialised access through one connection plus
// short transactions is faster and dramatically simpler. Reads and writes
// share that one slot — if read latency becomes a problem we add a second
// read-only *sql.DB pool, but the Store contract doesn't change.
//
// All time values are stored as Unix nanoseconds (int64). All retry-policy
// decisions are computed by the caller (coordinator) using the pure-domain
// helpers and passed in to Fail / ReclaimExpired so the Store remains free
// of policy.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/internal/domain/jobsm"
	"github.com/notpop/hearth/pkg/job"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ErrNotFound is returned when the requested job id does not exist.
var ErrNotFound = errors.New("sqlite: job not found")

// Store is a durable app.Store backed by a single SQLite file.
type Store struct {
	db   *sql.DB
	path string
}

// Open initialises (and migrates) the store at path.
func Open(path string) (*Store, error) {
	dsn := path + "?_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=temp_store(MEMORY)" +
		"&_pragma=foreign_keys(ON)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	// Serialise everything through one connection. See package doc.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	if _, err := db.ExecContext(context.Background(), schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite migrate: %w", err)
	}

	return &Store{db: db, path: path}, nil
}

// Close releases the underlying connection pool.
func (s *Store) Close() error { return s.db.Close() }

// Enqueue persists a freshly-built Job. It overwrites by id so callers that
// want strict insert semantics should generate fresh ids.
func (s *Store) Enqueue(ctx context.Context, j job.Job) error {
	if j.State == job.StateUnknown {
		j.State = job.StateQueued
	}
	blobs, err := encodeBlobs(j.Spec.Blobs)
	if err != nil {
		return err
	}
	resBlobs := ""
	var resPayload []byte
	if j.Result != nil {
		resPayload = j.Result.Payload
		if resBlobs, err = encodeBlobs(j.Result.Blobs); err != nil {
			return err
		}
	}

	var leasedNs, expiresNs sql.NullInt64
	var workerID sql.NullString
	if j.Lease != nil {
		leasedNs = sql.NullInt64{Int64: timeNs(j.Lease.LeasedAt), Valid: true}
		expiresNs = sql.NullInt64{Int64: timeNs(j.Lease.ExpiresAt), Valid: true}
		workerID = sql.NullString{String: j.Lease.WorkerID, Valid: true}
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO jobs (
		  id, kind, state, attempt, max_attempts, payload, blobs_json,
		  lease_ttl_ns, backoff_initial_ns, backoff_max_ns, backoff_multiplier, backoff_jitter,
		  worker_id, leased_at_ns, expires_at_ns,
		  result_payload, result_blobs_json,
		  last_error, next_run_at_ns, created_at_ns, updated_at_ns
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
		  kind=excluded.kind, state=excluded.state, attempt=excluded.attempt,
		  max_attempts=excluded.max_attempts, payload=excluded.payload, blobs_json=excluded.blobs_json,
		  lease_ttl_ns=excluded.lease_ttl_ns,
		  backoff_initial_ns=excluded.backoff_initial_ns, backoff_max_ns=excluded.backoff_max_ns,
		  backoff_multiplier=excluded.backoff_multiplier, backoff_jitter=excluded.backoff_jitter,
		  worker_id=excluded.worker_id, leased_at_ns=excluded.leased_at_ns, expires_at_ns=excluded.expires_at_ns,
		  result_payload=excluded.result_payload, result_blobs_json=excluded.result_blobs_json,
		  last_error=excluded.last_error, next_run_at_ns=excluded.next_run_at_ns,
		  updated_at_ns=excluded.updated_at_ns
	`,
		string(j.ID), j.Spec.Kind, int64(j.State), int64(j.Attempt), int64(j.Spec.MaxAttempts),
		j.Spec.Payload, nullableStr(blobs),
		int64(j.Spec.LeaseTTL),
		int64(j.Spec.Backoff.Initial), int64(j.Spec.Backoff.Max),
		j.Spec.Backoff.Multiplier, j.Spec.Backoff.Jitter,
		workerID, leasedNs, expiresNs,
		resPayload, nullableStr(resBlobs),
		nullableStr(j.LastError),
		timeNs(j.NextRunAt), timeNs(j.CreatedAt), timeNs(j.UpdatedAt),
	)
	return err
}

// Get fetches a job by id.
func (s *Store) Get(ctx context.Context, id job.ID) (job.Job, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectJobCols+` FROM jobs WHERE id = ?`, string(id))
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return job.Job{}, ErrNotFound
	}
	return j, err
}

// LeaseNext atomically picks the oldest eligible queued job and transitions
// it to Leased. Eligibility = state==Queued AND (kind in kinds OR kinds empty)
// AND (next_run_at_ns == 0 OR next_run_at_ns <= now).
func (s *Store) LeaseNext(ctx context.Context, kinds []string, workerID string, ttl time.Duration, now time.Time) (job.Job, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return job.Job{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	args := []any{int64(job.StateQueued), timeNs(now)}
	kindFilter := ""
	if len(kinds) > 0 {
		placeholders := strings.Repeat("?,", len(kinds))
		placeholders = placeholders[:len(placeholders)-1]
		kindFilter = " AND kind IN (" + placeholders + ")"
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	q := `SELECT ` + selectJobCols + ` FROM jobs
	      WHERE state = ?
	        AND (next_run_at_ns = 0 OR next_run_at_ns <= ?)` + kindFilter + `
	      ORDER BY created_at_ns ASC
	      LIMIT 1`

	row := tx.QueryRowContext(ctx, q, args...)
	candidate, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return job.Job{}, false, nil
	}
	if err != nil {
		return job.Job{}, false, err
	}

	leased, err := jobsm.Lease(candidate, jobsm.LeaseInput{WorkerID: workerID, Now: now, TTL: ttl})
	if err != nil {
		return job.Job{}, false, err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE jobs SET state=?, attempt=?, worker_id=?, leased_at_ns=?, expires_at_ns=?, updated_at_ns=?
		 WHERE id = ? AND state = ?`,
		int64(leased.State), int64(leased.Attempt), leased.Lease.WorkerID,
		timeNs(leased.Lease.LeasedAt), timeNs(leased.Lease.ExpiresAt), timeNs(leased.UpdatedAt),
		string(leased.ID), int64(job.StateQueued),
	)
	if err != nil {
		return job.Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return job.Job{}, false, err
	}
	return leased, true, nil
}

// Heartbeat extends the lease on (id, workerID).
func (s *Store) Heartbeat(ctx context.Context, id job.ID, workerID string, expiresAt time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET expires_at_ns = ?, updated_at_ns = ?
		 WHERE id = ? AND state = ? AND worker_id = ?`,
		timeNs(expiresAt), timeNs(expiresAt),
		string(id), int64(job.StateLeased), workerID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Either the id is wrong (then Get would have failed earlier) or
		// the lease was lost / never held. Both are programming/concurrency
		// errors from the caller's perspective.
		if _, gerr := s.Get(ctx, id); errors.Is(gerr, ErrNotFound) {
			return ErrNotFound
		}
		return jobsm.ErrInvalidTransition
	}
	return nil
}

// Complete marks the job Succeeded and writes the result.
func (s *Store) Complete(ctx context.Context, id job.ID, workerID string, res job.Result, now time.Time) error {
	resBlobs, err := encodeBlobs(res.Blobs)
	if err != nil {
		return err
	}
	r, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET state = ?, result_payload = ?, result_blobs_json = ?,
		    worker_id = NULL, leased_at_ns = NULL, expires_at_ns = NULL,
		    last_error = NULL, updated_at_ns = ?
		 WHERE id = ? AND state = ? AND worker_id = ?`,
		int64(job.StateSucceeded), res.Payload, nullableStr(resBlobs), timeNs(now),
		string(id), int64(job.StateLeased), workerID,
	)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		if _, gerr := s.Get(ctx, id); errors.Is(gerr, ErrNotFound) {
			return ErrNotFound
		}
		return jobsm.ErrInvalidTransition
	}
	return nil
}

// Fail records the error and applies the caller-supplied retry decision.
func (s *Store) Fail(ctx context.Context, id job.ID, workerID string, errMsg string, nextState job.State, retryAt time.Time, now time.Time) error {
	r, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET state = ?, last_error = ?,
		    worker_id = NULL, leased_at_ns = NULL, expires_at_ns = NULL,
		    next_run_at_ns = ?, updated_at_ns = ?
		 WHERE id = ? AND state = ? AND worker_id = ?`,
		int64(nextState), errMsg, timeNs(retryAt), timeNs(now),
		string(id), int64(job.StateLeased), workerID,
	)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		if _, gerr := s.Get(ctx, id); errors.Is(gerr, ErrNotFound) {
			return ErrNotFound
		}
		return jobsm.ErrInvalidTransition
	}
	return nil
}

// ReclaimExpired sweeps stale leases. Each reclaimed job is run through the
// pure jobsm.ReclaimExpired so retry policy stays in one place.
func (s *Store) ReclaimExpired(ctx context.Context, now time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT `+selectJobCols+` FROM jobs
		 WHERE state = ? AND expires_at_ns IS NOT NULL AND expires_at_ns < ?`,
		int64(job.StateLeased), timeNs(now))
	if err != nil {
		return 0, err
	}

	var toUpdate []job.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		out, _, ok := jobsm.ReclaimExpired(j, now, 0)
		if !ok {
			continue
		}
		toUpdate = append(toUpdate, out)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()

	for _, j := range toUpdate {
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs SET state = ?, last_error = ?,
			    worker_id = NULL, leased_at_ns = NULL, expires_at_ns = NULL,
			    next_run_at_ns = ?, updated_at_ns = ?
			 WHERE id = ?`,
			int64(j.State), j.LastError, timeNs(j.NextRunAt), timeNs(j.UpdatedAt), string(j.ID))
		if err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(toUpdate), nil
}

// Cancel transitions a non-terminal job to Cancelled.
func (s *Store) Cancel(ctx context.Context, id job.ID, now time.Time) error {
	r, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET state = ?, worker_id = NULL, leased_at_ns = NULL,
		    expires_at_ns = NULL, updated_at_ns = ?
		 WHERE id = ? AND state IN (?, ?)`,
		int64(job.StateCancelled), timeNs(now),
		string(id), int64(job.StateQueued), int64(job.StateLeased),
	)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Either id is unknown, or state is already terminal.
		if _, gerr := s.Get(ctx, id); errors.Is(gerr, ErrNotFound) {
			return ErrNotFound
		}
		return jobsm.ErrInvalidTransition
	}
	return nil
}

// List returns jobs matching filter, newest UpdatedAt first.
func (s *Store) List(ctx context.Context, filter app.ListFilter) ([]job.Job, error) {
	conds := []string{}
	args := []any{}
	if len(filter.States) > 0 {
		ph := strings.Repeat("?,", len(filter.States))
		ph = ph[:len(ph)-1]
		conds = append(conds, "state IN ("+ph+")")
		for _, st := range filter.States {
			args = append(args, int64(st))
		}
	}
	if len(filter.Kinds) > 0 {
		ph := strings.Repeat("?,", len(filter.Kinds))
		ph = ph[:len(ph)-1]
		conds = append(conds, "kind IN ("+ph+")")
		for _, k := range filter.Kinds {
			args = append(args, k)
		}
	}
	q := `SELECT ` + selectJobCols + ` FROM jobs`
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += ` ORDER BY updated_at_ns DESC`
	if filter.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []job.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
