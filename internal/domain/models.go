package domain

import (
	"encoding/json"
	"time"
)

type Launch struct {
	ID        int64
	ProjectID int64
	Name      string
	CreatedAt time.Time
	Closed    bool
	Raw       json.RawMessage
}

type TestResult struct {
	ID           int64
	ProjectID    int64
	LaunchID     int64
	TestCaseID   int64
	HistoryID    string
	FullName     string
	Name         string
	Status       string
	Message      string
	Trace        string
	FailureStep  string
	RetriesCount int
	HasRetryPass bool
	StartAt      time.Time
	EndAt        time.Time
	DurationMS   int64
	DefectIDs    []int64
	Raw          json.RawMessage
}

type Defect struct {
	ID        int64
	ProjectID int64
	Name      string
	Summary   string
	Status    string
	IssueKey  string
	IssueURL  string
	Raw       json.RawMessage
}

type KnowledgeDocument struct {
	ID            int64
	SourceType    string
	SourceKey     string
	ProjectID     int64
	LaunchID      *int64
	TestResultID  *int64
	TestCaseID    *int64
	HistoryID     string
	Title         string
	Content       string
	Metadata      map[string]any
	SemanticScore float64
	LexicalScore  float64
	CombinedScore float64
}

type DefectRef struct {
	ID       int64
	IssueKey string
	IssueURL string
}

type HistoryStats struct {
	Total            int      `json:"total"`
	Passed           int      `json:"passed"`
	Failed           int      `json:"failed"`
	ConsecutiveFails int      `json:"consecutive_fails"`
	PassRate         float64  `json:"pass_rate"`
	LastStatuses     []string `json:"last_statuses,omitempty"`
	KnownDefectIDs   []int64  `json:"known_defect_ids,omitempty"`
}

type Action string

const (
	ActionRerun             Action = "rerun"
	ActionAttachExistingBug Action = "attach_existing_bug"
	ActionCreateBug         Action = "create_bug"
)

type Decision struct {
	Action                Action    `json:"action"`
	Confidence            float64   `json:"confidence"`
	Reasoning             string    `json:"reasoning"`
	ExistingDefectID      *int64    `json:"existing_defect_id,omitempty"`
	ExistingIssueKey      string    `json:"existing_issue_key,omitempty"`
	SuggestedIssueSummary string    `json:"suggested_issue_summary,omitempty"`
	SuggestedIssueBody    string    `json:"suggested_issue_body,omitempty"`
	Evidence              []string  `json:"evidence,omitempty"`
	GeneratedBy           string    `json:"generated_by"`
	AnalyzedAt            time.Time `json:"analyzed_at"`
}

type CandidateBrief struct {
	SourceType string  `json:"source_type"`
	SourceKey  string  `json:"source_key"`
	Title      string  `json:"title"`
	Score      float64 `json:"score"`
}

type AnalysisResult struct {
	TestID      int64            `json:"test_id"`
	LaunchID    int64            `json:"launch_id"`
	TestName    string           `json:"test_name"`
	HistoryID   string           `json:"history_id,omitempty"`
	Status      string           `json:"status"`
	Message     string           `json:"message,omitempty"`
	FailureStep string           `json:"failure_step,omitempty"`
	Decision    Decision         `json:"decision"`
	History     HistoryStats     `json:"history"`
	Candidates  []CandidateBrief `json:"candidates"`
}
