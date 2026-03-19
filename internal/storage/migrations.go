package storage

var schemaMigrations = []string{
	`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
	`CREATE TABLE IF NOT EXISTS launches (
		id BIGINT PRIMARY KEY,
		project_id BIGINT NOT NULL,
		name TEXT NOT NULL,
		created_at TIMESTAMPTZ,
		closed BOOLEAN NOT NULL DEFAULT FALSE,
		raw JSONB NOT NULL DEFAULT '{}'::jsonb,
		synced_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE TABLE IF NOT EXISTS test_results (
		id BIGINT PRIMARY KEY,
		project_id BIGINT NOT NULL,
		launch_id BIGINT NOT NULL REFERENCES launches(id) ON DELETE CASCADE,
		test_case_id BIGINT NOT NULL DEFAULT 0,
		history_id TEXT NOT NULL DEFAULT '',
		full_name TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL,
		status TEXT NOT NULL,
		message TEXT NOT NULL DEFAULT '',
		trace TEXT NOT NULL DEFAULT '',
		failure_step TEXT NOT NULL DEFAULT '',
		retries_count INTEGER NOT NULL DEFAULT 0,
		has_retry_pass BOOLEAN NOT NULL DEFAULT FALSE,
		start_at TIMESTAMPTZ,
		end_at TIMESTAMPTZ,
		duration_ms BIGINT NOT NULL DEFAULT 0,
		raw JSONB NOT NULL DEFAULT '{}'::jsonb,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_test_results_launch_id ON test_results (launch_id)`,
	`CREATE INDEX IF NOT EXISTS idx_test_results_history_id ON test_results (project_id, history_id)`,
	`CREATE INDEX IF NOT EXISTS idx_test_results_case_id ON test_results (project_id, test_case_id)`,
	`CREATE INDEX IF NOT EXISTS idx_test_results_full_name_trgm ON test_results USING gin (full_name gin_trgm_ops)`,
	`CREATE TABLE IF NOT EXISTS defects (
		id BIGINT PRIMARY KEY,
		project_id BIGINT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		summary TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT '',
		issue_key TEXT NOT NULL DEFAULT '',
		issue_url TEXT NOT NULL DEFAULT '',
		raw JSONB NOT NULL DEFAULT '{}'::jsonb,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE TABLE IF NOT EXISTS test_result_defects (
		test_result_id BIGINT NOT NULL REFERENCES test_results(id) ON DELETE CASCADE,
		defect_id BIGINT NOT NULL REFERENCES defects(id) ON DELETE CASCADE,
		PRIMARY KEY (test_result_id, defect_id)
	)`,
	`CREATE TABLE IF NOT EXISTS knowledge_documents (
		id BIGSERIAL PRIMARY KEY,
		source_type TEXT NOT NULL,
		source_key TEXT NOT NULL UNIQUE,
		project_id BIGINT NOT NULL,
		launch_id BIGINT,
		test_result_id BIGINT REFERENCES test_results(id) ON DELETE CASCADE,
		test_case_id BIGINT,
		history_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL,
		content TEXT NOT NULL,
		metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
		tsv tsvector GENERATED ALWAYS AS (
			to_tsvector('simple', coalesce(title, '') || ' ' || coalesce(content, ''))
		) STORED,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_documents_project_source ON knowledge_documents (project_id, source_type)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_documents_history_id ON knowledge_documents (project_id, history_id)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_documents_tsv ON knowledge_documents USING gin (tsv)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_documents_title_trgm ON knowledge_documents USING gin (title gin_trgm_ops)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_documents_content_trgm ON knowledge_documents USING gin (content gin_trgm_ops)`,
	`CREATE TABLE IF NOT EXISTS triage_decisions (
		test_result_id BIGINT PRIMARY KEY REFERENCES test_results(id) ON DELETE CASCADE,
		launch_id BIGINT NOT NULL,
		action TEXT NOT NULL,
		confidence DOUBLE PRECISION NOT NULL,
		reasoning TEXT NOT NULL,
		defect_id BIGINT,
		issue_key TEXT NOT NULL DEFAULT '',
		suggested_issue_summary TEXT NOT NULL DEFAULT '',
		suggested_issue_body TEXT NOT NULL DEFAULT '',
		evidence JSONB NOT NULL DEFAULT '[]'::jsonb,
		generated_by TEXT NOT NULL,
		analyzed_at TIMESTAMPTZ NOT NULL,
		raw JSONB NOT NULL DEFAULT '{}'::jsonb
	)`,
}
