package storage

import (
	"ai-model/internal/config"
	"ai-model/internal/domain"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

var safeIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, cfg config.DatabaseConfig) (*Store, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConns = cfg.MaxConns

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pgx pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) Migrate(ctx context.Context) error {
	for _, statement := range schemaMigrations {
		if _, err := s.pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("apply migration %q: %w", statement, err)
		}
	}
	return nil
}

func (s *Store) UpsertLaunch(ctx context.Context, launch domain.Launch) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO launches (id, project_id, name, created_at, closed, raw, synced_at)
		VALUES ($1, $2, $3, NULLIF($4, TIMESTAMPTZ '0001-01-01 00:00:00+00'), $5, $6, NOW())
		ON CONFLICT (id) DO UPDATE SET
			project_id = EXCLUDED.project_id,
			name = EXCLUDED.name,
			created_at = EXCLUDED.created_at,
			closed = EXCLUDED.closed,
			raw = EXCLUDED.raw,
			synced_at = NOW()
	`, launch.ID, launch.ProjectID, launch.Name, zeroTimeToNil(launch.CreatedAt), launch.Closed, jsonOrEmpty(launch.Raw))
	return err
}

func (s *Store) UpsertDefect(ctx context.Context, defect domain.Defect) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO defects (id, project_id, name, summary, status, issue_key, issue_url, raw, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
		ON CONFLICT (id) DO UPDATE SET
			project_id = EXCLUDED.project_id,
			name = EXCLUDED.name,
			summary = EXCLUDED.summary,
			status = EXCLUDED.status,
			issue_key = EXCLUDED.issue_key,
			issue_url = EXCLUDED.issue_url,
			raw = EXCLUDED.raw,
			updated_at = NOW()
	`, defect.ID, defect.ProjectID, defect.Name, defect.Summary, defect.Status, defect.IssueKey, defect.IssueURL, jsonOrEmpty(defect.Raw))
	return err
}

func (s *Store) UpsertTestResult(ctx context.Context, test domain.TestResult) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer rollbackIfNeeded(ctx, tx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO test_results (
			id, project_id, launch_id, test_case_id, history_id, full_name, name, status, message, trace,
			failure_step, retries_count, has_retry_pass, start_at, end_at, duration_ms, raw, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, NULLIF($14, TIMESTAMPTZ '0001-01-01 00:00:00+00'),
			NULLIF($15, TIMESTAMPTZ '0001-01-01 00:00:00+00'), $16, $17, NOW()
		)
		ON CONFLICT (id) DO UPDATE SET
			project_id = EXCLUDED.project_id,
			launch_id = EXCLUDED.launch_id,
			test_case_id = EXCLUDED.test_case_id,
			history_id = EXCLUDED.history_id,
			full_name = EXCLUDED.full_name,
			name = EXCLUDED.name,
			status = EXCLUDED.status,
			message = EXCLUDED.message,
			trace = EXCLUDED.trace,
			failure_step = EXCLUDED.failure_step,
			retries_count = EXCLUDED.retries_count,
			has_retry_pass = EXCLUDED.has_retry_pass,
			start_at = EXCLUDED.start_at,
			end_at = EXCLUDED.end_at,
			duration_ms = EXCLUDED.duration_ms,
			raw = EXCLUDED.raw,
			updated_at = NOW()
	`, test.ID, test.ProjectID, test.LaunchID, test.TestCaseID, test.HistoryID, test.FullName, test.Name, test.Status, test.Message, test.Trace,
		test.FailureStep, test.RetriesCount, test.HasRetryPass, zeroTimeToNil(test.StartAt), zeroTimeToNil(test.EndAt), test.DurationMS, jsonOrEmpty(test.Raw)); err != nil {
		return fmt.Errorf("upsert test result %d: %w", test.ID, err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM test_result_defects WHERE test_result_id = $1`, test.ID); err != nil {
		return fmt.Errorf("delete test_result_defects for %d: %w", test.ID, err)
	}

	for _, defectID := range test.DefectIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO test_result_defects (test_result_id, defect_id)
			VALUES ($1, $2)
			ON CONFLICT (test_result_id, defect_id) DO NOTHING
		`, test.ID, defectID); err != nil {
			return fmt.Errorf("insert test_result_defect for %d: %w", test.ID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit test result %d: %w", test.ID, err)
	}

	return nil
}

func (s *Store) UpsertKnowledgeDocument(ctx context.Context, doc domain.KnowledgeDocument) error {
	metadata, err := json.Marshal(doc.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata for %s: %w", doc.SourceKey, err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO knowledge_documents (
			source_type, source_key, project_id, launch_id, test_result_id, test_case_id,
			history_id, title, content, metadata, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, NOW()
		)
		ON CONFLICT (source_key) DO UPDATE SET
			source_type = EXCLUDED.source_type,
			project_id = EXCLUDED.project_id,
			launch_id = EXCLUDED.launch_id,
			test_result_id = EXCLUDED.test_result_id,
			test_case_id = EXCLUDED.test_case_id,
			history_id = EXCLUDED.history_id,
			title = EXCLUDED.title,
			content = EXCLUDED.content,
			metadata = EXCLUDED.metadata,
			updated_at = NOW()
	`, doc.SourceType, doc.SourceKey, doc.ProjectID, doc.LaunchID, doc.TestResultID, doc.TestCaseID, doc.HistoryID, doc.Title, doc.Content, metadata)
	return err
}

func (s *Store) ListFailedTestResultsByLaunch(ctx context.Context, launchID int64) ([]domain.TestResult, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, launch_id, test_case_id, history_id, full_name, name, status, message,
			trace, failure_step, retries_count, has_retry_pass, start_at, end_at, duration_ms, raw
		FROM test_results
		WHERE launch_id = $1 AND status IN ('failed', 'broken')
		ORDER BY id
	`, launchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.TestResult
	for rows.Next() {
		var item domain.TestResult
		var raw []byte
		if err := rows.Scan(
			&item.ID, &item.ProjectID, &item.LaunchID, &item.TestCaseID, &item.HistoryID, &item.FullName,
			&item.Name, &item.Status, &item.Message, &item.Trace, &item.FailureStep, &item.RetriesCount,
			&item.HasRetryPass, &item.StartAt, &item.EndAt, &item.DurationMS, &raw,
		); err != nil {
			return nil, err
		}
		item.Raw = raw
		item.DefectIDs, _ = s.listDefectIDsByTestResult(ctx, item.ID)
		results = append(results, item)
	}

	return results, rows.Err()
}

func (s *Store) GetHistoryStats(ctx context.Context, test domain.TestResult, window int) (domain.HistoryStats, error) {
	if window <= 0 {
		window = 20
	}

	rows, err := s.pool.Query(ctx, `
		SELECT status, id
		FROM test_results
		WHERE project_id = $1
			AND id <> $2
			AND (
				(history_id <> '' AND history_id = $3)
				OR ($4 <> 0 AND test_case_id = $4)
				OR ($5 <> '' AND full_name = $5)
			)
		ORDER BY COALESCE(start_at, end_at, updated_at) DESC, id DESC
		LIMIT $6
	`, test.ProjectID, test.ID, test.HistoryID, test.TestCaseID, test.FullName, window)
	if err != nil {
		return domain.HistoryStats{}, err
	}
	defer rows.Close()

	var (
		stats       domain.HistoryStats
		statuses    []string
		testIDs     []int64
		seenPass    bool
		consecutive int
	)

	for rows.Next() {
		var (
			status string
			id     int64
		)
		if err := rows.Scan(&status, &id); err != nil {
			return domain.HistoryStats{}, err
		}
		status = strings.ToLower(status)
		statuses = append(statuses, status)
		testIDs = append(testIDs, id)
		stats.Total++
		if status == "passed" {
			stats.Passed++
			seenPass = true
		} else if status == "failed" || status == "broken" {
			stats.Failed++
			if !seenPass {
				consecutive++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return domain.HistoryStats{}, err
	}

	stats.ConsecutiveFails = consecutive
	if stats.Total > 0 {
		stats.PassRate = float64(stats.Passed) / float64(stats.Total)
		stats.LastStatuses = statuses
		stats.KnownDefectIDs, _ = s.collectKnownDefectIDs(ctx, testIDs)
	}

	return stats, nil
}

func (s *Store) SearchHistoricalCandidates(ctx context.Context, test domain.TestResult, limit int) ([]domain.KnowledgeDocument, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT kd.id, kd.source_type, kd.source_key, kd.project_id, kd.launch_id, kd.test_result_id, kd.test_case_id,
			kd.history_id, kd.title, kd.content, kd.metadata
		FROM knowledge_documents kd
		LEFT JOIN test_results tr ON tr.id = kd.test_result_id
		WHERE kd.project_id = $1
			AND kd.source_key <> $2
			AND (
				($3 <> '' AND kd.history_id = $3)
				OR ($4 <> 0 AND kd.test_case_id = $4)
				OR ($5 <> '' AND tr.full_name = $5)
			)
		ORDER BY tr.updated_at DESC NULLS LAST, kd.id DESC
		LIMIT $6
	`, test.ProjectID, sourceKeyForTest(test.ID), test.HistoryID, test.TestCaseID, test.FullName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []domain.KnowledgeDocument
	for rows.Next() {
		doc, err := scanKnowledgeDocument(rows)
		if err != nil {
			return nil, err
		}
		doc.CombinedScore = 1
		candidates = append(candidates, doc)
	}

	return candidates, rows.Err()
}

func (s *Store) SearchLexicalCandidates(ctx context.Context, projectID int64, queryText string, limit int) ([]domain.KnowledgeDocument, error) {
	queryText = strings.TrimSpace(queryText)
	if queryText == "" || limit <= 0 {
		return nil, nil
	}

	rows, err := s.pool.Query(ctx, `
		WITH query AS (
			SELECT plainto_tsquery('simple', $2) AS ts_query
		)
		SELECT kd.id, kd.source_type, kd.source_key, kd.project_id, kd.launch_id, kd.test_result_id, kd.test_case_id,
			kd.history_id, kd.title, kd.content, kd.metadata,
			GREATEST(
				ts_rank_cd(kd.tsv, query.ts_query),
				similarity(kd.title, $2),
				similarity(kd.content, $2)
			) AS score
		FROM knowledge_documents kd
		CROSS JOIN query
		WHERE kd.project_id = $1
			AND (kd.tsv @@ query.ts_query OR kd.title % $2 OR kd.content % $2)
		ORDER BY score DESC, kd.id DESC
		LIMIT $3
	`, projectID, queryText, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []domain.KnowledgeDocument
	for rows.Next() {
		doc, score, err := scanKnowledgeDocumentWithScore(rows)
		if err != nil {
			return nil, err
		}
		doc.LexicalScore = score
		doc.CombinedScore = score
		candidates = append(candidates, doc)
	}

	return candidates, rows.Err()
}

func (s *Store) SearchSemanticCandidates(ctx context.Context, projectID int64, viewName string, embedding []float32, limit int) ([]domain.KnowledgeDocument, error) {
	if !safeIdentifier.MatchString(viewName) {
		return nil, fmt.Errorf("unsafe semantic view name %q", viewName)
	}
	if len(embedding) == 0 || limit <= 0 {
		return nil, nil
	}

	sql := fmt.Sprintf(`
		SELECT id, source_type, source_key, project_id, launch_id, test_result_id, test_case_id,
			history_id, title, chunk AS content, metadata,
			1 - (embedding <=> $1) AS score
		FROM %s
		WHERE project_id = $2
		ORDER BY embedding <=> $1
		LIMIT $3
	`, viewName)

	rows, err := s.pool.Query(ctx, sql, pgvector.NewVector(embedding), projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []domain.KnowledgeDocument
	for rows.Next() {
		doc, score, err := scanKnowledgeDocumentWithScore(rows)
		if err != nil {
			return nil, err
		}
		doc.SemanticScore = math.Max(0, score)
		doc.CombinedScore = doc.SemanticScore
		candidates = append(candidates, doc)
	}

	return candidates, rows.Err()
}

func (s *Store) SaveDecision(ctx context.Context, result domain.AnalysisResult) error {
	raw, err := json.Marshal(result.Decision)
	if err != nil {
		return fmt.Errorf("marshal decision: %w", err)
	}
	evidence, err := json.Marshal(result.Decision.Evidence)
	if err != nil {
		return fmt.Errorf("marshal evidence: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO triage_decisions (
			test_result_id, launch_id, action, confidence, reasoning, defect_id, issue_key,
			suggested_issue_summary, suggested_issue_body, evidence, generated_by, analyzed_at, raw
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12, $13
		)
		ON CONFLICT (test_result_id) DO UPDATE SET
			launch_id = EXCLUDED.launch_id,
			action = EXCLUDED.action,
			confidence = EXCLUDED.confidence,
			reasoning = EXCLUDED.reasoning,
			defect_id = EXCLUDED.defect_id,
			issue_key = EXCLUDED.issue_key,
			suggested_issue_summary = EXCLUDED.suggested_issue_summary,
			suggested_issue_body = EXCLUDED.suggested_issue_body,
			evidence = EXCLUDED.evidence,
			generated_by = EXCLUDED.generated_by,
			analyzed_at = EXCLUDED.analyzed_at,
			raw = EXCLUDED.raw
	`, result.TestID, result.LaunchID, result.Decision.Action, result.Decision.Confidence, result.Decision.Reasoning,
		result.Decision.ExistingDefectID, result.Decision.ExistingIssueKey, result.Decision.SuggestedIssueSummary,
		result.Decision.SuggestedIssueBody, evidence, result.Decision.GeneratedBy, result.Decision.AnalyzedAt, raw)
	return err
}

func (s *Store) listDefectIDsByTestResult(ctx context.Context, testResultID int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT defect_id FROM test_result_defects WHERE test_result_id = $1 ORDER BY defect_id`, testResultID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) collectKnownDefectIDs(ctx context.Context, testResultIDs []int64) ([]int64, error) {
	if len(testResultIDs) == 0 {
		return nil, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT defect_id
		FROM test_result_defects
		WHERE test_result_id = ANY($1)
		ORDER BY defect_id
	`, testResultIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func mergeCandidates(slices ...[]domain.KnowledgeDocument) []domain.KnowledgeDocument {
	byKey := make(map[string]domain.KnowledgeDocument)
	for _, items := range slices {
		for _, candidate := range items {
			existing, ok := byKey[candidate.SourceKey]
			if !ok {
				byKey[candidate.SourceKey] = candidate
				continue
			}
			if candidate.LexicalScore > existing.LexicalScore {
				existing.LexicalScore = candidate.LexicalScore
			}
			if candidate.SemanticScore > existing.SemanticScore {
				existing.SemanticScore = candidate.SemanticScore
				if candidate.Content != "" {
					existing.Content = candidate.Content
				}
			}
			if candidate.CombinedScore > existing.CombinedScore {
				existing.CombinedScore = candidate.CombinedScore
			}
			byKey[candidate.SourceKey] = existing
		}
	}

	merged := make([]domain.KnowledgeDocument, 0, len(byKey))
	for _, candidate := range byKey {
		merged = append(merged, candidate)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].CombinedScore == merged[j].CombinedScore {
			return merged[i].SourceKey < merged[j].SourceKey
		}
		return merged[i].CombinedScore > merged[j].CombinedScore
	})
	return merged
}

func scanKnowledgeDocument(rows pgx.Rows) (domain.KnowledgeDocument, error) {
	var (
		doc      domain.KnowledgeDocument
		metadata []byte
	)
	if err := rows.Scan(
		&doc.ID,
		&doc.SourceType,
		&doc.SourceKey,
		&doc.ProjectID,
		&doc.LaunchID,
		&doc.TestResultID,
		&doc.TestCaseID,
		&doc.HistoryID,
		&doc.Title,
		&doc.Content,
		&metadata,
	); err != nil {
		return domain.KnowledgeDocument{}, err
	}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &doc.Metadata); err != nil {
			return domain.KnowledgeDocument{}, err
		}
	}
	if doc.Metadata == nil {
		doc.Metadata = map[string]any{}
	}
	return doc, nil
}

func scanKnowledgeDocumentWithScore(rows pgx.Rows) (domain.KnowledgeDocument, float64, error) {
	var (
		doc      domain.KnowledgeDocument
		metadata []byte
		score    float64
	)
	if err := rows.Scan(
		&doc.ID,
		&doc.SourceType,
		&doc.SourceKey,
		&doc.ProjectID,
		&doc.LaunchID,
		&doc.TestResultID,
		&doc.TestCaseID,
		&doc.HistoryID,
		&doc.Title,
		&doc.Content,
		&metadata,
		&score,
	); err != nil {
		return domain.KnowledgeDocument{}, 0, err
	}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &doc.Metadata); err != nil {
			return domain.KnowledgeDocument{}, 0, err
		}
	}
	if doc.Metadata == nil {
		doc.Metadata = map[string]any{}
	}
	return doc, score, nil
}

func sourceKeyForTest(testResultID int64) string {
	return fmt.Sprintf("test_result:%d", testResultID)
}

func zeroTimeToNil(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func jsonOrEmpty(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte(`{}`)
	}
	return raw
}

func rollbackIfNeeded(ctx context.Context, tx pgx.Tx) {
	_ = tx.Rollback(ctx)
}
