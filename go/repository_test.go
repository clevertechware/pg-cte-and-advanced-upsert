package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	uniqueViolation  = "23505"
	notNullViolation = "23502"
	abortedTx        = "25P02"
	lockToken        = "token-a"
)

var testDB *sql.DB

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:18",
		tcpostgres.WithDatabase("jobs"),
		tcpostgres.WithUsername("jobs"),
		tcpostgres.WithPassword("jobs"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start postgres container:", err)
		return 1
	}
	defer func() { _ = testcontainers.TerminateContainer(container) }()

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, "container connection string:", err)
		return 1
	}
	testDB, err = sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open database:", err)
		return 1
	}
	defer testDB.Close()

	if _, err := testDB.ExecContext(ctx, Schema); err != nil {
		fmt.Fprintln(os.Stderr, "apply schema:", err)
		return 1
	}
	return m.Run()
}

func resetJobs(t *testing.T) {
	t.Helper()
	_, err := testDB.ExecContext(context.Background(), "TRUNCATE jobs RESTART IDENTITY")
	require.NoError(t, err)
}

func insertJob(t *testing.T, status, catalog string, ids []string) int64 {
	t.Helper()
	config, err := json.Marshal(map[string]any{"catalogId": catalog, "productIds": ids})
	require.NoError(t, err)
	var id int64
	err = testDB.QueryRowContext(context.Background(), `
		INSERT INTO jobs (type, status, config, lock_token, started_at)
		VALUES ('PRICE_REFRESH', $1, $2::jsonb,
		        CASE WHEN $1 = 'RUNNING' THEN $3 END,
		        CASE WHEN $1 = 'RUNNING' THEN now() - interval '2 seconds' END)
		RETURNING id`, status, string(config), lockToken).Scan(&id)
	require.NoError(t, err)
	return id
}

func pendingJobID(t *testing.T, catalog string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, testDB.QueryRowContext(context.Background(), `
		SELECT id FROM jobs
		WHERE status = 'PENDING' AND type = 'PRICE_REFRESH' AND config->>'catalogId' = $1`, catalog).Scan(&id))
	return id
}

func jobLockToken(t *testing.T, id int64) string {
	t.Helper()
	var token string
	require.NoError(t, testDB.QueryRowContext(context.Background(),
		"SELECT lock_token FROM jobs WHERE id = $1", id).Scan(&token))
	return token
}

func countJobs(t *testing.T) int {
	t.Helper()
	var count int
	require.NoError(t, testDB.QueryRowContext(context.Background(), "SELECT count(*) FROM jobs").Scan(&count))
	return count
}

func jobStatus(t *testing.T, id int64) string {
	t.Helper()
	var status string
	require.NoError(t, testDB.QueryRowContext(context.Background(),
		"SELECT status FROM jobs WHERE id = $1", id).Scan(&status))
	return status
}

func jobProductIDs(t *testing.T, id int64) []string {
	t.Helper()
	var raw []byte
	require.NoError(t, testDB.QueryRowContext(context.Background(),
		"SELECT config->'productIds' FROM jobs WHERE id = $1", id).Scan(&raw))
	var ids []string
	require.NoError(t, json.Unmarshal(raw, &ids))
	sort.Strings(ids)
	return ids
}

func jobResult(t *testing.T, id int64) map[string]any {
	t.Helper()
	var raw []byte
	require.NoError(t, testDB.QueryRowContext(context.Background(),
		"SELECT result FROM jobs WHERE id = $1", id).Scan(&raw))
	var result map[string]any
	require.NoError(t, json.Unmarshal(raw, &result))
	return result
}

func requirePgCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "expected a PostgreSQL error, got %v", err)
	require.Equal(t, code, pgErr.Code, pgErr.Message)
}

var note = json.RawMessage(`{"note":"merged into the pending job of the same catalog"}`)

func TestNaiveRequeue_ViolatesPartialUniqueIndex_WhenPendingJobExistsForSameCatalog(t *testing.T) {
	resetJobs(t)
	running := insertJob(t, "RUNNING", "catalog-a", []string{"product-a1"})
	insertJob(t, "PENDING", "catalog-a", []string{"product-b1"})

	_, err := testDB.ExecContext(context.Background(),
		"UPDATE jobs SET status = 'PENDING' WHERE id = $1", running)

	requirePgCode(t, err, uniqueViolation)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr))
	require.Equal(t, "jobs_price_refresh_pending_catalog_uq", pgErr.ConstraintName)
}

func TestRequeue_MergesIdsIntoPendingJobAndCompletesRunningJob_WhenPendingJobIsNewerOnSameCatalog(t *testing.T) {
	resetJobs(t)
	a := insertJob(t, "RUNNING", "catalog-a", []string{"product-a1", "product-a2", "product-shared"})
	b := insertJob(t, "PENDING", "catalog-a", []string{"product-b1", "product-shared"})

	mergedInto, err := NewRepository(testDB).RequeuePriceRefresh(context.Background(), a, lockToken, note)

	require.NoError(t, err)
	require.Equal(t, b, mergedInto)
	require.Equal(t, []string{"product-a1", "product-a2", "product-b1", "product-shared"}, jobProductIDs(t, b))
	require.Equal(t, "PENDING", jobStatus(t, b))
	require.Equal(t, "COMPLETED", jobStatus(t, a))
	result := jobResult(t, a)
	require.EqualValues(t, b, result["mergedIntoJobId"])
	require.Equal(t, "merged into the pending job of the same catalog", result["note"])
}

func TestRequeue_ReturnsRunningJobToPending_WhenNoConcurrentJobExists(t *testing.T) {
	resetJobs(t)
	a := insertJob(t, "RUNNING", "catalog-a", []string{"product-a1"})

	mergedInto, err := NewRepository(testDB).RequeuePriceRefresh(context.Background(), a, lockToken, note)

	require.NoError(t, err)
	require.Zero(t, mergedInto)
	require.Equal(t, "PENDING", jobStatus(t, a))
	require.Equal(t, []string{"product-a1"}, jobProductIDs(t, a))
}

func TestRequeue_LeavesPendingJobOfAnotherCatalogUntouched_WhenCatalogsDiffer(t *testing.T) {
	resetJobs(t)
	a := insertJob(t, "RUNNING", "catalog-a", []string{"product-a1"})
	other := insertJob(t, "PENDING", "catalog-b", []string{"product-o1"})

	mergedInto, err := NewRepository(testDB).RequeuePriceRefresh(context.Background(), a, lockToken, note)

	require.NoError(t, err)
	require.Zero(t, mergedInto)
	require.Equal(t, "PENDING", jobStatus(t, a))
	require.Equal(t, "PENDING", jobStatus(t, other))
	require.Equal(t, []string{"product-o1"}, jobProductIDs(t, other))
	require.Empty(t, jobResult(t, other))
}

func TestRequeue_ModifiesNothing_WhenLockTokenIsNotTheOneOfTheClaim(t *testing.T) {
	resetJobs(t)
	a := insertJob(t, "RUNNING", "catalog-a", []string{"product-a1"})
	b := insertJob(t, "PENDING", "catalog-a", []string{"product-b1"})

	_, err := NewRepository(testDB).RequeuePriceRefresh(context.Background(), a, "stolen-token", note)

	require.ErrorIs(t, err, ErrJobNotRunning)
	require.Equal(t, "RUNNING", jobStatus(t, a))
	require.Equal(t, "PENDING", jobStatus(t, b))
	require.Equal(t, []string{"product-b1"}, jobProductIDs(t, b))
	require.Empty(t, jobResult(t, a))
}

func TestRequeue_FailsWithNotNullViolation_WhenBothIdArraysAreEmpty(t *testing.T) {
	resetJobs(t)
	a := insertJob(t, "RUNNING", "catalog-a", []string{})
	b := insertJob(t, "PENDING", "catalog-a", []string{})

	_, err := NewRepository(testDB).RequeuePriceRefresh(context.Background(), a, lockToken, note)

	requirePgCode(t, err, notNullViolation)
	require.Equal(t, "RUNNING", jobStatus(t, a))
	require.Equal(t, "PENDING", jobStatus(t, b))
	require.Equal(t, []string{}, jobProductIDs(t, b))
}

func TestRequeue_KeepsEmptyArrayAndCompletesRunningJob_WhenBothIdArraysAreEmptyWithCoalesce(t *testing.T) {
	resetJobs(t)
	a := insertJob(t, "RUNNING", "catalog-a", []string{})
	b := insertJob(t, "PENDING", "catalog-a", []string{})

	mergedInto, err := NewRepositoryGuardingEmptyArrays(testDB).
		RequeuePriceRefresh(context.Background(), a, lockToken, note)

	require.NoError(t, err)
	require.Equal(t, b, mergedInto)
	require.Equal(t, []string{}, jobProductIDs(t, b))
	require.Equal(t, "COMPLETED", jobStatus(t, a))
}

func TestClaim_ReturnsErrJobNotClaimable_WhenJobIsNotPending(t *testing.T) {
	resetJobs(t)
	a := insertJob(t, "RUNNING", "catalog-a", []string{"product-a1"})

	err := NewRepository(testDB).Claim(context.Background(), a, "other-token")

	require.ErrorIs(t, err, ErrJobNotClaimable)
}

func TestClaim_SetsRunningStatusAndLockToken_WhenJobIsPending(t *testing.T) {
	resetJobs(t)
	repo := NewRepository(testDB)
	require.NoError(t, repo.EnqueuePriceRefresh(context.Background(), "catalog-a", []string{"product-a1"}))

	a := pendingJobID(t, "catalog-a")

	require.NoError(t, repo.Claim(context.Background(), a, lockToken))

	require.Equal(t, "RUNNING", jobStatus(t, a))
	require.Equal(t, lockToken, jobLockToken(t, a))
}

func TestEnqueue_MergesIdsIntoExistingPendingJob_WhenCatalogAlreadyHasOne(t *testing.T) {
	resetJobs(t)
	repo := NewRepository(testDB)
	require.NoError(t, repo.EnqueuePriceRefresh(context.Background(), "catalog-a", []string{"product-a", "product-b"}))

	require.NoError(t, repo.EnqueuePriceRefresh(context.Background(), "catalog-a", []string{"product-b", "product-c"}))

	require.Equal(t, 1, countJobs(t))
	require.Equal(t, []string{"product-a", "product-b", "product-c"},
		jobProductIDs(t, pendingJobID(t, "catalog-a")))
}

func TestRequeue_AbortsTransactionWithUniqueViolation_WhenPendingJobCommitsBetweenMergeAndFallback(t *testing.T) {
	resetJobs(t)
	ctx := context.Background()
	a := insertJob(t, "RUNNING", "catalog-a", []string{"product-a1"})
	tx, err := testDB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	repo := NewRepository(tx)

	_, err = repo.mergeIntoPending(ctx, a, lockToken, note)
	require.ErrorIs(t, err, sql.ErrNoRows)

	require.NoError(t, NewRepository(testDB).
		EnqueuePriceRefresh(ctx, "catalog-a", []string{"product-b1"}))

	err = repo.returnToPending(ctx, a, lockToken)
	requirePgCode(t, err, uniqueViolation)

	_, err = tx.ExecContext(ctx, "SELECT 1")
	requirePgCode(t, err, abortedTx)
	require.ErrorIs(t, tx.Commit(), pgx.ErrTxCommitRollback)
	require.Equal(t, "RUNNING", jobStatus(t, a))
}
