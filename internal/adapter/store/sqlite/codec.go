package sqlite

// Job <-> SQL row codec. Kept separate from the Store methods so the SQL
// stays readable and the conversions can be unit-tested in isolation if
// the row layout grows.

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/notpop/hearth/pkg/job"
)

// nsTime converts an int64 Unix-nanosecond value back to a time.Time.
// Zero is preserved as the zero time so callers can distinguish "unset".
func nsTime(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}

// timeNs converts a time.Time to Unix-nanoseconds; zero -> 0.
func timeNs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func encodeBlobs(refs []job.BlobRef) (string, error) {
	if len(refs) == 0 {
		return "", nil
	}
	b, err := json.Marshal(refs)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeBlobs(s sql.NullString) ([]job.BlobRef, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	var refs []job.BlobRef
	if err := json.Unmarshal([]byte(s.String), &refs); err != nil {
		return nil, err
	}
	return refs, nil
}

// scanJob reads one row produced by selectJobCols into a Job.
type jobRow struct {
	ID                                                                  string
	Kind                                                                string
	State                                                               int64
	Attempt                                                             int64
	MaxAttempts                                                         int64
	Payload                                                             []byte
	BlobsJSON                                                           sql.NullString
	LeaseTTLNs                                                          int64
	BackoffInitialNs, BackoffMaxNs                                      int64
	BackoffMultiplier, BackoffJitter                                    float64
	WorkerID                                                            sql.NullString
	LeasedAtNs, ExpiresAtNs                                             sql.NullInt64
	ResultPayload                                                       []byte
	ResultBlobsJSON                                                     sql.NullString
	LastError                                                           sql.NullString
	NextRunAtNs                                                         int64
	ProgressPercent                                                     sql.NullFloat64
	ProgressMessage                                                     sql.NullString
	ProgressReportedAtNs                                                sql.NullInt64
	CreatedAtNs, UpdatedAtNs                                            int64
}

const selectJobCols = `
id, kind, state, attempt, max_attempts, payload, blobs_json,
lease_ttl_ns, backoff_initial_ns, backoff_max_ns, backoff_multiplier, backoff_jitter,
worker_id, leased_at_ns, expires_at_ns,
result_payload, result_blobs_json,
last_error, next_run_at_ns,
progress_percent, progress_message, progress_reported_at_ns,
created_at_ns, updated_at_ns
`

func scanJob(scanner interface {
	Scan(dest ...any) error
}) (job.Job, error) {
	var r jobRow
	err := scanner.Scan(
		&r.ID, &r.Kind, &r.State, &r.Attempt, &r.MaxAttempts, &r.Payload, &r.BlobsJSON,
		&r.LeaseTTLNs, &r.BackoffInitialNs, &r.BackoffMaxNs, &r.BackoffMultiplier, &r.BackoffJitter,
		&r.WorkerID, &r.LeasedAtNs, &r.ExpiresAtNs,
		&r.ResultPayload, &r.ResultBlobsJSON,
		&r.LastError, &r.NextRunAtNs,
		&r.ProgressPercent, &r.ProgressMessage, &r.ProgressReportedAtNs,
		&r.CreatedAtNs, &r.UpdatedAtNs,
	)
	if err != nil {
		return job.Job{}, err
	}

	blobs, err := decodeBlobs(r.BlobsJSON)
	if err != nil {
		return job.Job{}, err
	}
	resBlobs, err := decodeBlobs(r.ResultBlobsJSON)
	if err != nil {
		return job.Job{}, err
	}

	j := job.Job{
		ID:    job.ID(r.ID),
		State: job.State(r.State),
		Spec: job.Spec{
			Kind:        r.Kind,
			Payload:     r.Payload,
			Blobs:       blobs,
			MaxAttempts: int(r.MaxAttempts),
			LeaseTTL:    time.Duration(r.LeaseTTLNs),
			Backoff: job.BackoffPolicy{
				Initial:    time.Duration(r.BackoffInitialNs),
				Max:        time.Duration(r.BackoffMaxNs),
				Multiplier: r.BackoffMultiplier,
				Jitter:     r.BackoffJitter,
			},
		},
		Attempt:   int(r.Attempt),
		LastError: nullStr(r.LastError),
		NextRunAt: nsTime(r.NextRunAtNs),
		CreatedAt: nsTime(r.CreatedAtNs),
		UpdatedAt: nsTime(r.UpdatedAtNs),
	}

	if r.WorkerID.Valid && r.LeasedAtNs.Valid && r.ExpiresAtNs.Valid {
		j.Lease = &job.Lease{
			WorkerID:  r.WorkerID.String,
			LeasedAt:  nsTime(r.LeasedAtNs.Int64),
			ExpiresAt: nsTime(r.ExpiresAtNs.Int64),
		}
	}

	if len(r.ResultPayload) > 0 || len(resBlobs) > 0 {
		j.Result = &job.Result{Payload: r.ResultPayload, Blobs: resBlobs}
	}
	if r.ProgressPercent.Valid || r.ProgressMessage.Valid {
		j.Progress = &job.Progress{
			Percent:    r.ProgressPercent.Float64,
			Message:    nullStr(r.ProgressMessage),
			ReportedAt: nsTime(r.ProgressReportedAtNs.Int64),
		}
	}
	return j, nil
}

func nullStr(s sql.NullString) string {
	if s.Valid {
		return s.String
	}
	return ""
}
