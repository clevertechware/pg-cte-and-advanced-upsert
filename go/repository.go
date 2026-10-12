package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrJobNotRunning = errors.New("job is not RUNNING with this lock token")

var ErrJobNotClaimable = errors.New("job is not PENDING")

type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type Repository struct {
	db         DBTX
	mergeQuery string
}

func NewRepository(db DBTX) *Repository {
	return &Repository{db: db, mergeQuery: mergeAndCompleteQuery}
}

// NewRepositoryGuardingEmptyArrays returns a repository whose requeue query
// wraps jsonb_agg in COALESCE, so that merging two empty id arrays yields [].
func NewRepositoryGuardingEmptyArrays(db DBTX) *Repository {
	return &Repository{db: db, mergeQuery: mergeAndCompleteGuardedQuery}
}

const upsertQuery = `
INSERT INTO jobs (type, status, config)
VALUES ('PRICE_REFRESH', 'PENDING',
        jsonb_build_object('catalogId', $1::text, 'productIds', $2::jsonb))
ON CONFLICT ((config->>'catalogId'))
    WHERE status = 'PENDING' AND type = 'PRICE_REFRESH'
DO UPDATE SET config = jsonb_set(jobs.config, '{productIds}', (
    SELECT jsonb_agg(elem) FROM (
        SELECT jsonb_array_elements_text(jobs.config->'productIds') AS elem
        UNION
        SELECT jsonb_array_elements_text(excluded.config->'productIds')
    ) merged
))`

const mergeAndCompleteQuery = `
WITH running AS (
    SELECT id, config FROM jobs
    WHERE id = $1 AND status = 'RUNNING' AND lock_token = $2
    FOR UPDATE
), merged AS (
    UPDATE jobs pending SET config = jsonb_set(pending.config, '{productIds}', (
        SELECT jsonb_agg(elem) FROM (
            SELECT jsonb_array_elements_text(pending.config->'productIds') AS elem
            UNION
            SELECT jsonb_array_elements_text(running.config->'productIds')
        ) merged_ids
    ))
    FROM running
    WHERE pending.status = 'PENDING' AND pending.type = 'PRICE_REFRESH'
      AND pending.config->>'catalogId' = running.config->>'catalogId'
    RETURNING pending.id
)
UPDATE jobs SET status = 'COMPLETED',
                updated_at = now(),
                finished_at = now(),
                duration_ms = (extract(epoch FROM now() - started_at) * 1000)::bigint,
                result = $3::jsonb || jsonb_build_object('mergedIntoJobId', merged.id)
FROM merged
WHERE jobs.id = (SELECT id FROM running)
RETURNING merged.id`

const mergeAndCompleteGuardedQuery = `
WITH running AS (
    SELECT id, config FROM jobs
    WHERE id = $1 AND status = 'RUNNING' AND lock_token = $2
    FOR UPDATE
), merged AS (
    UPDATE jobs pending SET config = jsonb_set(pending.config, '{productIds}', (
        SELECT COALESCE(jsonb_agg(elem), '[]'::jsonb) FROM (
            SELECT jsonb_array_elements_text(pending.config->'productIds') AS elem
            UNION
            SELECT jsonb_array_elements_text(running.config->'productIds')
        ) merged_ids
    ))
    FROM running
    WHERE pending.status = 'PENDING' AND pending.type = 'PRICE_REFRESH'
      AND pending.config->>'catalogId' = running.config->>'catalogId'
    RETURNING pending.id
)
UPDATE jobs SET status = 'COMPLETED',
                updated_at = now(),
                finished_at = now(),
                duration_ms = (extract(epoch FROM now() - started_at) * 1000)::bigint,
                result = $3::jsonb || jsonb_build_object('mergedIntoJobId', merged.id)
FROM merged
WHERE jobs.id = (SELECT id FROM running)
RETURNING merged.id`

const returnToPendingQuery = `
UPDATE jobs SET status = 'PENDING', lock_token = NULL, updated_at = now()
WHERE id = $1 AND status = 'RUNNING' AND lock_token = $2`

const claimQuery = `
UPDATE jobs SET status = 'RUNNING', lock_token = $2, started_at = now(), updated_at = now()
WHERE id = $1 AND status = 'PENDING'`

// EnqueuePriceRefresh enqueues a PENDING job for the catalog, or merges the
// product ids into the PENDING job that already exists for it.
func (r *Repository) EnqueuePriceRefresh(ctx context.Context, catalogID string, productIDs []string) error {
	ids, err := json.Marshal(productIDs)
	if err != nil {
		return fmt.Errorf("encode product ids: %w", err)
	}
	_, err = r.db.ExecContext(ctx, upsertQuery, catalogID, string(ids))
	if err != nil {
		return fmt.Errorf("enqueue price refresh job for catalog %s: %w", catalogID, err)
	}
	return nil
}

// Claim moves a PENDING job to RUNNING and records the lock token that guards
// every later write on the job.
func (r *Repository) Claim(ctx context.Context, jobID int64, lockToken string) error {
	res, err := r.db.ExecContext(ctx, claimQuery, jobID, lockToken)
	if err != nil {
		return fmt.Errorf("claim job %d: %w", jobID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("claim job %d: %w", jobID, err)
	}
	if affected == 0 {
		return fmt.Errorf("claim job %d: %w", jobID, ErrJobNotClaimable)
	}
	return nil
}

// RequeuePriceRefresh puts a RUNNING job back in the queue. When a PENDING job
// exists for the same catalog, the product ids are merged into it and the
// RUNNING job is COMPLETED: the id of the absorbing job is returned. Otherwise
// the job returns to PENDING and the returned id is 0.
func (r *Repository) RequeuePriceRefresh(
	ctx context.Context, jobID int64, lockToken string, result json.RawMessage,
) (int64, error) {
	mergedIntoJobID, err := r.mergeIntoPending(ctx, jobID, lockToken, result)
	switch {
	case err == nil:
		return mergedIntoJobID, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("merge job %d into the pending job of its catalog: %w", jobID, err)
	}
	if err := r.returnToPending(ctx, jobID, lockToken); err != nil {
		return 0, fmt.Errorf("requeue job %d: %w", jobID, err)
	}
	return 0, nil
}

func (r *Repository) mergeIntoPending(
	ctx context.Context, jobID int64, lockToken string, result json.RawMessage,
) (int64, error) {
	var mergedIntoJobID int64
	err := r.db.QueryRowContext(ctx, r.mergeQuery, jobID, lockToken, string(result)).Scan(&mergedIntoJobID)
	return mergedIntoJobID, err
}

func (r *Repository) returnToPending(ctx context.Context, jobID int64, lockToken string) error {
	res, err := r.db.ExecContext(ctx, returnToPendingQuery, jobID, lockToken)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrJobNotRunning
	}
	return nil
}
