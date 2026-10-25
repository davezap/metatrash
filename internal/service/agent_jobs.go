package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// agentJob is one run of a folder action (metatrash_agent_jobs, schema v15;
// agent_id since v16, contributor_id since v17).
type agentJob struct {
	ID, SpaceID, Folder, Action, File, Commit string
	// AgentID is the agent the job ran on; empty until it starts.
	AgentID string
	// Contributor is the user whose change brought the file; empty when
	// nobody clear did (a GitHub pull, a space key).
	Contributor string
	// Status is queued, running, done, failed or skipped.
	Status                          string
	QueuedAt, StartedAt, FinishedAt time.Time
	Steps                           int
	InputTokens, OutputTokens       int64
	Error                           string
}

const (
	jobQueued  = "queued"
	jobRunning = "running"
	jobDone    = "done"
	jobFailed  = "failed"
	jobSkipped = "skipped"
)

// agentJobStore keeps the job queue. The account database holds it; tests
// use a memory store.
type agentJobStore interface {
	add(ctx context.Context, j agentJob) error
	// queuedSince counts a folder's jobs queued at or after since.
	queuedSince(ctx context.Context, spaceID, folder string, since time.Time) (int, error)
	// tokensSince sums the tokens of an agent's jobs that finished at or
	// after since.
	tokensSince(ctx context.Context, agentID string, since time.Time) (int64, error)
	// queued returns waiting jobs, oldest first.
	queued(ctx context.Context, limit int) ([]agentJob, error)
	// claim moves a queued job to running; false when another took it.
	claim(ctx context.Context, id string, at time.Time) (bool, error)
	finish(ctx context.Context, j agentJob) error
	// running returns jobs left running, as after a restart.
	running(ctx context.Context) ([]agentJob, error)
	// get returns one job; false when there is none.
	get(ctx context.Context, id string) (agentJob, bool, error)
}

func (db *accountDatabase) checkAgentJobSchema(ctx context.Context) error {
	bad := fmt.Errorf("account schema v17 required (folder agent tasks); follow docs/deployment.md")
	var count int
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'metatrash_agent_jobs' AND engine = 'InnoDB'").Scan(&count); err != nil || count != 1 {
		return bad
	}
	rows, err := db.db.QueryContext(ctx, "SELECT "+agentJobColumns+" FROM metatrash_agent_jobs LIMIT 0")
	if err != nil {
		return bad
	}
	rows.Close()
	rows, err = db.db.QueryContext(ctx, "SELECT "+userAgentColumns+" FROM metatrash_agents LIMIT 0")
	if err != nil {
		return bad
	}
	rows.Close()
	rows, err = db.db.QueryContext(ctx, "SELECT "+optInColumns+" FROM metatrash_action_optins LIMIT 0")
	if err != nil {
		return bad
	}
	rows.Close()
	return nil
}

// dbJobStore is the account database's job queue.
type dbJobStore struct{ db *sql.DB }

const agentJobColumns = "job_id, space_id, folder, action, file, commit_id, status, queued_at, started_at, finished_at, steps, input_tokens, output_tokens, error, agent_id, contributor_id"

func unixOrNull(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

func (s dbJobStore) add(ctx context.Context, j agentJob) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO metatrash_agent_jobs ("+agentJobColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		j.ID, j.SpaceID, j.Folder, j.Action, j.File, j.Commit, j.Status, j.QueuedAt.Unix(), unixOrNull(j.StartedAt), unixOrNull(j.FinishedAt), j.Steps, j.InputTokens, j.OutputTokens, nullText(j.Error), nullText(j.AgentID), nullText(j.Contributor))
	if err != nil {
		return fmt.Errorf("cannot queue agent job")
	}
	return nil
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s dbJobStore) queuedSince(ctx context.Context, spaceID, folder string, since time.Time) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_agent_jobs WHERE space_id = ? AND folder = ? AND queued_at >= ?", spaceID, folder, since.Unix()).Scan(&n); err != nil {
		return 0, fmt.Errorf("cannot count agent jobs")
	}
	return n, nil
}

func (s dbJobStore) tokensSince(ctx context.Context, agentID string, since time.Time) (int64, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(input_tokens + output_tokens), 0) FROM metatrash_agent_jobs WHERE agent_id = ? AND finished_at >= ?", agentID, since.Unix()).Scan(&n); err != nil {
		return 0, fmt.Errorf("cannot sum agent job tokens")
	}
	return n, nil
}

func (s dbJobStore) list(ctx context.Context, query string, args ...any) ([]agentJob, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+agentJobColumns+" FROM metatrash_agent_jobs "+query, args...)
	if err != nil {
		return nil, fmt.Errorf("cannot read agent jobs")
	}
	defer rows.Close()
	var jobs []agentJob
	for rows.Next() {
		var j agentJob
		var queued int64
		var started, finished sql.NullInt64
		var message, agent, contributor sql.NullString
		if err := rows.Scan(&j.ID, &j.SpaceID, &j.Folder, &j.Action, &j.File, &j.Commit, &j.Status, &queued, &started, &finished, &j.Steps, &j.InputTokens, &j.OutputTokens, &message, &agent, &contributor); err != nil {
			return nil, fmt.Errorf("cannot read agent jobs")
		}
		j.QueuedAt = time.Unix(queued, 0).UTC()
		if started.Valid {
			j.StartedAt = time.Unix(started.Int64, 0).UTC()
		}
		if finished.Valid {
			j.FinishedAt = time.Unix(finished.Int64, 0).UTC()
		}
		j.Error, j.AgentID, j.Contributor = message.String, agent.String, contributor.String
		jobs = append(jobs, j)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("cannot read agent jobs")
	}
	return jobs, nil
}

func (s dbJobStore) queued(ctx context.Context, limit int) ([]agentJob, error) {
	return s.list(ctx, "WHERE status = 'queued' ORDER BY queued_at, job_id LIMIT ?", limit)
}

func (s dbJobStore) running(ctx context.Context) ([]agentJob, error) {
	return s.list(ctx, "WHERE status = 'running' LIMIT 1000")
}

func (s dbJobStore) get(ctx context.Context, id string) (agentJob, bool, error) {
	jobs, err := s.list(ctx, "WHERE job_id = ?", id)
	if err != nil || len(jobs) == 0 {
		return agentJob{}, false, err
	}
	return jobs[0], true, nil
}

func (s dbJobStore) claim(ctx context.Context, id string, at time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, "UPDATE metatrash_agent_jobs SET status = 'running', started_at = ? WHERE job_id = ? AND status = 'queued'", at.Unix(), id)
	if err != nil {
		return false, fmt.Errorf("cannot start agent job")
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("cannot start agent job")
	}
	return n == 1, nil
}

func (s dbJobStore) finish(ctx context.Context, j agentJob) error {
	_, err := s.db.ExecContext(ctx, "UPDATE metatrash_agent_jobs SET status = ?, started_at = ?, finished_at = ?, steps = ?, input_tokens = ?, output_tokens = ?, error = ?, agent_id = ? WHERE job_id = ?",
		j.Status, unixOrNull(j.StartedAt), unixOrNull(j.FinishedAt), j.Steps, j.InputTokens, j.OutputTokens, nullText(j.Error), nullText(j.AgentID), j.ID)
	if err != nil {
		return fmt.Errorf("cannot record agent job")
	}
	return nil
}
