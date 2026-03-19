package analyzer

import (
	"ai-model/internal/config"
	"ai-model/internal/domain"
	"testing"
)

func TestHeuristicDecisionRerunForTransientFailure(t *testing.T) {
	service := Service{
		cfg: config.Config{
			Analysis: config.AnalysisConfig{
				StrongRerunPassRate:    0.7,
				StrongDefectThreshold:  0.78,
				AttachCandidateMinimum: 0.7,
			},
		},
	}

	testResult := domain.TestResult{
		ID:          101,
		LaunchID:    202,
		Name:        "checkout timeout",
		Status:      "failed",
		Message:     "context deadline exceeded while calling upstream",
		FailureStep: "Submit payment",
	}
	history := domain.HistoryStats{
		Total:        10,
		Passed:       9,
		Failed:       1,
		PassRate:     0.9,
		LastStatuses: []string{"passed", "passed", "failed"},
	}

	decision := service.heuristicDecision(testResult, history, nil)
	if decision.Action != domain.ActionRerun {
		t.Fatalf("expected rerun, got %s", decision.Action)
	}
}

func TestHeuristicDecisionAttachExistingBugForStrongDefectMatch(t *testing.T) {
	service := Service{
		cfg: config.Config{
			Analysis: config.AnalysisConfig{
				StrongRerunPassRate:    0.7,
				StrongDefectThreshold:  0.78,
				AttachCandidateMinimum: 0.7,
			},
		},
	}

	testResult := domain.TestResult{
		ID:       11,
		LaunchID: 22,
		Name:     "billing broken",
		Status:   "failed",
		Message:  "assertion mismatch",
	}
	candidates := []domain.KnowledgeDocument{
		{
			SourceType:    "defect",
			SourceKey:     "defect:555",
			CombinedScore: 0.91,
			Metadata: map[string]any{
				"defect_id": float64(555),
				"issue_key": "BUG-555",
			},
		},
	}

	decision := service.heuristicDecision(testResult, domain.HistoryStats{}, candidates)
	if decision.Action != domain.ActionAttachExistingBug {
		t.Fatalf("expected attach_existing_bug, got %s", decision.Action)
	}
	if decision.ExistingDefectID == nil || *decision.ExistingDefectID != 555 {
		t.Fatalf("expected defect id 555, got %+v", decision.ExistingDefectID)
	}
}

func TestHeuristicDecisionCreateBugWhenNoSignal(t *testing.T) {
	service := Service{
		cfg: config.Config{
			Analysis: config.AnalysisConfig{
				StrongRerunPassRate:    0.7,
				StrongDefectThreshold:  0.78,
				AttachCandidateMinimum: 0.7,
			},
			Allure: config.AllureConfig{
				BaseURL:           "https://allure.example",
				LaunchURLTemplate: "{base_url}/launch/{launch_id}",
				TestURLTemplate:   "{base_url}/launch/{launch_id}/testresult/{test_result_id}",
			},
		},
	}

	testResult := domain.TestResult{
		ID:       1,
		LaunchID: 2,
		Name:     "new regression",
		Status:   "failed",
		Message:  "unexpected business state",
	}
	history := domain.HistoryStats{
		Total:            5,
		Passed:           0,
		Failed:           5,
		ConsecutiveFails: 5,
		PassRate:         0,
	}

	decision := service.heuristicDecision(testResult, history, nil)
	if decision.Action != domain.ActionCreateBug {
		t.Fatalf("expected create_bug, got %s", decision.Action)
	}
	if decision.SuggestedIssueBody == "" {
		t.Fatal("expected suggested issue body to be populated")
	}
}
