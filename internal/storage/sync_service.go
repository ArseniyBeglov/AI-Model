package storage

import (
	"ai-model/internal/domain"
	"context"
	"fmt"
	"strings"
)

type AllureClient interface {
	GetLaunch(ctx context.Context, launchID int64) (domain.Launch, error)
	ListRecentLaunches(ctx context.Context, limit int) ([]domain.Launch, error)
	ListLaunchResults(ctx context.Context, launchID int64) ([]domain.TestResult, error)
	ListDefects(ctx context.Context, testResultID int64) ([]domain.Defect, error)
}

type SyncService struct {
	store  *Store
	allure AllureClient
}

func NewSyncService(store *Store, client AllureClient) *SyncService {
	return &SyncService{
		store:  store,
		allure: client,
	}
}

func (s *SyncService) SyncRecentLaunches(ctx context.Context, limit int) error {
	launches, err := s.allure.ListRecentLaunches(ctx, limit)
	if err != nil {
		return fmt.Errorf("list recent launches: %w", err)
	}
	for _, launch := range launches {
		if err := s.SyncLaunch(ctx, launch.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *SyncService) SyncLaunch(ctx context.Context, launchID int64) error {
	launch, err := s.allure.GetLaunch(ctx, launchID)
	if err != nil {
		return fmt.Errorf("get launch %d: %w", launchID, err)
	}
	if err := s.store.UpsertLaunch(ctx, launch); err != nil {
		return fmt.Errorf("store launch %d: %w", launchID, err)
	}

	results, err := s.allure.ListLaunchResults(ctx, launchID)
	if err != nil {
		return fmt.Errorf("list test results for launch %d: %w", launchID, err)
	}

	for _, result := range results {
		if err := s.store.UpsertTestResult(ctx, result); err != nil {
			return err
		}

		if isFailureResult(result) {
			defects, err := s.allure.ListDefects(ctx, result.ID)
			if err == nil {
				for _, defect := range defects {
					if err := s.store.UpsertDefect(ctx, defect); err != nil {
						return err
					}
				}
			}

			if err := s.store.UpsertKnowledgeDocument(ctx, buildTestDocument(result)); err != nil {
				return fmt.Errorf("store test knowledge document for %d: %w", result.ID, err)
			}
		}
	}

	defectCache := make(map[int64]domain.Defect)
	for _, result := range results {
		if !isFailureResult(result) {
			continue
		}
		for _, defectID := range result.DefectIDs {
			defect, ok := defectCache[defectID]
			if !ok {
				defects, err := s.allure.ListDefects(ctx, result.ID)
				if err != nil {
					continue
				}
				for _, item := range defects {
					defectCache[item.ID] = item
				}
				defect = defectCache[defectID]
			}
			if defect.ID == 0 {
				continue
			}
			if err := s.store.UpsertKnowledgeDocument(ctx, buildDefectDocument(result.ProjectID, defect)); err != nil {
				return fmt.Errorf("store defect knowledge document for %d: %w", defect.ID, err)
			}
		}
	}

	return nil
}

func buildTestDocument(test domain.TestResult) domain.KnowledgeDocument {
	var builder strings.Builder
	builder.WriteString("status: " + test.Status + "\n")
	if test.FullName != "" {
		builder.WriteString("full_name: " + test.FullName + "\n")
	}
	if test.FailureStep != "" {
		builder.WriteString("failure_step: " + test.FailureStep + "\n")
	}
	if test.Message != "" {
		builder.WriteString("message: " + test.Message + "\n")
	}
	if test.Trace != "" {
		builder.WriteString("trace:\n" + test.Trace + "\n")
	}
	if test.RetriesCount > 0 {
		builder.WriteString(fmt.Sprintf("retries_count: %d\n", test.RetriesCount))
	}
	if test.HasRetryPass {
		builder.WriteString("retry_passed: true\n")
	}

	launchID := test.LaunchID
	testID := test.ID
	testCaseID := test.TestCaseID

	return domain.KnowledgeDocument{
		SourceType:   "failed_test",
		SourceKey:    sourceKeyForTest(test.ID),
		ProjectID:    test.ProjectID,
		LaunchID:     &launchID,
		TestResultID: &testID,
		TestCaseID:   &testCaseID,
		HistoryID:    test.HistoryID,
		Title:        pickFirstNonEmpty(test.FullName, test.Name),
		Content:      builder.String(),
		Metadata: map[string]any{
			"status":         test.Status,
			"launch_id":      test.LaunchID,
			"test_result_id": test.ID,
			"test_case_id":   test.TestCaseID,
			"defect_ids":     test.DefectIDs,
			"has_retry_pass": test.HasRetryPass,
			"retries_count":  test.RetriesCount,
		},
	}
}

func buildDefectDocument(projectID int64, defect domain.Defect) domain.KnowledgeDocument {
	var builder strings.Builder
	if defect.Summary != "" {
		builder.WriteString(defect.Summary + "\n")
	}
	if defect.Status != "" {
		builder.WriteString("status: " + defect.Status + "\n")
	}
	if defect.IssueKey != "" {
		builder.WriteString("jira_key: " + defect.IssueKey + "\n")
	}
	if defect.IssueURL != "" {
		builder.WriteString("jira_url: " + defect.IssueURL + "\n")
	}

	return domain.KnowledgeDocument{
		SourceType: "defect",
		SourceKey:  fmt.Sprintf("defect:%d", defect.ID),
		ProjectID:  projectID,
		Title:      pickFirstNonEmpty(defect.Name, defect.IssueKey),
		Content:    builder.String(),
		Metadata: map[string]any{
			"defect_id": defect.ID,
			"issue_key": defect.IssueKey,
			"issue_url": defect.IssueURL,
			"status":    defect.Status,
		},
	}
}

func isFailureResult(test domain.TestResult) bool {
	return test.Status == "failed" || test.Status == "broken"
}

func pickFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
