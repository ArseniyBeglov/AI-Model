package analyzer

import (
	"ai-model/internal/config"
	"ai-model/internal/domain"
	"ai-model/internal/storage"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var transientFailurePattern = regexp.MustCompile(`(?i)(timeout|timed out|deadline exceeded|temporar(il)?y unavailable|connection reset|connection refused|connection aborted|connection closed|broken pipe|unexpected eof|eof|i/o timeout|tls handshake timeout|bad gateway|gateway timeout|502|503|504|read tcp|write tcp|no route to host|network is unreachable|connection lost)`)

type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

type ChatModel interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

type Service struct {
	store    *storage.Store
	embedder Embedder
	llm      ChatModel
	cfg      config.Config
}

func NewService(store *storage.Store, embedder Embedder, llm ChatModel, cfg config.Config) *Service {
	return &Service{
		store:    store,
		embedder: embedder,
		llm:      llm,
		cfg:      cfg,
	}
}

func (s *Service) AnalyzeLaunch(ctx context.Context, launchID int64) ([]domain.AnalysisResult, error) {
	tests, err := s.store.ListFailedTestResultsByLaunch(ctx, launchID)
	if err != nil {
		return nil, fmt.Errorf("list failed tests: %w", err)
	}

	results := make([]domain.AnalysisResult, 0, len(tests))
	for _, test := range tests {
		result, err := s.AnalyzeTest(ctx, test)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
		if err := s.store.SaveDecision(ctx, result); err != nil {
			return nil, fmt.Errorf("save decision for test %d: %w", test.ID, err)
		}
	}

	return results, nil
}

func (s *Service) AnalyzeTest(ctx context.Context, test domain.TestResult) (domain.AnalysisResult, error) {
	history, err := s.store.GetHistoryStats(ctx, test, s.cfg.Analysis.HistoryWindow)
	if err != nil {
		return domain.AnalysisResult{}, fmt.Errorf("history stats for test %d: %w", test.ID, err)
	}

	exactCandidates, err := s.store.SearchHistoricalCandidates(ctx, test, s.cfg.Analysis.TopKPerQuery)
	if err != nil {
		return domain.AnalysisResult{}, fmt.Errorf("historical candidates for test %d: %w", test.ID, err)
	}

	var lexicalCandidates []domain.KnowledgeDocument
	for _, query := range buildQueries(test) {
		candidates, err := s.store.SearchLexicalCandidates(ctx, test.ProjectID, query, s.cfg.Analysis.TopKPerQuery)
		if err != nil {
			return domain.AnalysisResult{}, fmt.Errorf("lexical candidates for test %d: %w", test.ID, err)
		}
		for i := range candidates {
			candidates[i].CombinedScore = maxFloat(candidates[i].CombinedScore, candidates[i].LexicalScore)
		}
		lexicalCandidates = append(lexicalCandidates, candidates...)
	}

	var semanticCandidates []domain.KnowledgeDocument
	if s.cfg.Analysis.SemanticSearchEnabled && s.embedder != nil {
		queries := buildQueries(test)
		vectors, err := s.embedder.Embed(ctx, queries)
		if err == nil {
			for idx, vector := range vectors {
				candidates, searchErr := s.store.SearchSemanticCandidates(ctx, test.ProjectID, s.cfg.Analysis.SemanticViewName, vector, s.cfg.Analysis.TopKPerQuery)
				if searchErr != nil {
					break
				}
				for i := range candidates {
					candidates[i].CombinedScore = maxFloat(candidates[i].CombinedScore, candidates[i].SemanticScore)
					if idx == 0 {
						candidates[i].CombinedScore += 0.02
					}
				}
				semanticCandidates = append(semanticCandidates, candidates...)
			}
		}
	}

	candidates := scoreCandidates(exactCandidates, lexicalCandidates, semanticCandidates)
	if len(candidates) > s.cfg.Analysis.MaxCandidates {
		candidates = candidates[:s.cfg.Analysis.MaxCandidates]
	}

	decision := s.heuristicDecision(test, history, candidates)
	if s.llm != nil {
		if llmDecision, err := s.llmDecision(ctx, test, history, candidates, decision); err == nil {
			decision = llmDecision
		}
	}

	result := domain.AnalysisResult{
		TestID:      test.ID,
		LaunchID:    test.LaunchID,
		TestName:    firstNonEmpty(test.FullName, test.Name),
		HistoryID:   test.HistoryID,
		Status:      test.Status,
		Message:     test.Message,
		FailureStep: test.FailureStep,
		Decision:    decision,
		History:     history,
		Candidates:  toBriefCandidates(candidates),
	}

	return result, nil
}

func (s *Service) heuristicDecision(test domain.TestResult, history domain.HistoryStats, candidates []domain.KnowledgeDocument) domain.Decision {
	action := domain.ActionCreateBug
	confidence := 0.63
	generatedBy := "rules"

	evidence := []string{}
	if history.Total > 0 {
		evidence = append(evidence, fmt.Sprintf("history: %d runs, pass_rate=%.2f, consecutive_fails=%d", history.Total, history.PassRate, history.ConsecutiveFails))
	}
	if test.HasRetryPass {
		evidence = append(evidence, "current launch already has a successful retry")
	}
	if len(test.DefectIDs) > 0 {
		evidence = append(evidence, fmt.Sprintf("current result already linked to defects %v", test.DefectIDs))
	}

	if len(test.DefectIDs) > 0 {
		defectID := test.DefectIDs[0]
		return domain.Decision{
			Action:           domain.ActionAttachExistingBug,
			Confidence:       0.98,
			Reasoning:        "Тест уже привязан к дефекту в текущем запуске Allure.",
			ExistingDefectID: &defectID,
			Evidence:         evidence,
			GeneratedBy:      generatedBy,
			AnalyzedAt:       time.Now().UTC(),
		}
	}

	bestDefect := topCandidateByType(candidates, "defect")
	if bestDefect != nil && bestDefect.CombinedScore >= s.cfg.Analysis.StrongDefectThreshold {
		action = domain.ActionAttachExistingBug
		confidence = clamp(bestDefect.CombinedScore, 0.78, 0.97)
		evidence = append(evidence, fmt.Sprintf("strong defect candidate %s score=%.2f", bestDefect.SourceKey, bestDefect.CombinedScore))

		decision := domain.Decision{
			Action:           action,
			Confidence:       confidence,
			Reasoning:        "Нашёлся сильный матч по уже существующему дефекту или очень близкому прошлому падению.",
			ExistingDefectID: extractDefectID(bestDefect),
			ExistingIssueKey: extractIssueKey(bestDefect),
			Evidence:         evidence,
			GeneratedBy:      generatedBy,
			AnalyzedAt:       time.Now().UTC(),
		}
		return decision
	}

	transient := isTransientFailure(test)
	if transient {
		evidence = append(evidence, "failure signature matches transient/network patterns")
	}

	if test.HasRetryPass || (transient && history.Total >= 3 && history.PassRate >= s.cfg.Analysis.StrongRerunPassRate) {
		action = domain.ActionRerun
		confidence = 0.82
		return domain.Decision{
			Action:      action,
			Confidence:  confidence,
			Reasoning:   "Похоже на флак: история в целом зелёная, а сигнатура ошибки похожа на временную инфраструктурную проблему.",
			Evidence:    evidence,
			GeneratedBy: generatedBy,
			AnalyzedAt:  time.Now().UTC(),
		}
	}

	if bestDefect != nil && bestDefect.CombinedScore >= s.cfg.Analysis.AttachCandidateMinimum {
		action = domain.ActionAttachExistingBug
		confidence = clamp(bestDefect.CombinedScore, 0.70, 0.88)
		evidence = append(evidence, fmt.Sprintf("candidate defect %s score=%.2f", bestDefect.SourceKey, bestDefect.CombinedScore))
		return domain.Decision{
			Action:           action,
			Confidence:       confidence,
			Reasoning:        "Есть неплохое совпадение с существующим дефектом, хотя и не максимально сильное.",
			ExistingDefectID: extractDefectID(bestDefect),
			ExistingIssueKey: extractIssueKey(bestDefect),
			Evidence:         evidence,
			GeneratedBy:      generatedBy,
			AnalyzedAt:       time.Now().UTC(),
		}
	}

	if transient && history.PassRate >= 0.5 {
		return domain.Decision{
			Action:      domain.ActionRerun,
			Confidence:  0.72,
			Reasoning:   "Ошибка похожа на временную, а история не выглядит стабильно красной.",
			Evidence:    evidence,
			GeneratedBy: generatedBy,
			AnalyzedAt:  time.Now().UTC(),
		}
	}

	summary, body := s.buildIssueSuggestion(test, history, candidates)
	if history.ConsecutiveFails >= 3 {
		evidence = append(evidence, "multiple consecutive failures without a strong matching defect")
		confidence = 0.86
	}

	return domain.Decision{
		Action:                action,
		Confidence:            confidence,
		Reasoning:             "Надёжного соответствия существующему дефекту нет; падение выглядит как новая проблема и требует отдельного бага.",
		SuggestedIssueSummary: summary,
		SuggestedIssueBody:    body,
		Evidence:              evidence,
		GeneratedBy:           generatedBy,
		AnalyzedAt:            time.Now().UTC(),
	}
}

func (s *Service) llmDecision(ctx context.Context, test domain.TestResult, history domain.HistoryStats, candidates []domain.KnowledgeDocument, fallback domain.Decision) (domain.Decision, error) {
	payload := map[string]any{
		"test": map[string]any{
			"id":             test.ID,
			"launch_id":      test.LaunchID,
			"name":           firstNonEmpty(test.FullName, test.Name),
			"history_id":     test.HistoryID,
			"status":         test.Status,
			"failure_step":   test.FailureStep,
			"message":        test.Message,
			"trace":          trimForPrompt(test.Trace, 3000),
			"retries_count":  test.RetriesCount,
			"has_retry_pass": test.HasRetryPass,
			"defect_ids":     test.DefectIDs,
		},
		"history":    history,
		"candidates": candidates,
		"fallback":   fallback,
	}

	requestJSON, err := json.Marshal(payload)
	if err != nil {
		return domain.Decision{}, err
	}

	systemPrompt := "You are an expert test triage assistant. Decide whether a failed test should be rerun, attached to an existing defect, or sent to a new bug. Respond with strict JSON only."
	userPrompt := "Input JSON:\n" + string(requestJSON) + "\n\nReturn JSON with keys action, confidence, reasoning, existing_defect_id, existing_issue_key, suggested_issue_summary, suggested_issue_body, evidence."

	response, err := s.llm.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return domain.Decision{}, err
	}

	response = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(response, "```"), "```json"))
	var decision domain.Decision
	if err := json.Unmarshal([]byte(response), &decision); err != nil {
		return domain.Decision{}, err
	}

	switch decision.Action {
	case domain.ActionRerun, domain.ActionAttachExistingBug, domain.ActionCreateBug:
	default:
		return domain.Decision{}, fmt.Errorf("llm returned unsupported action %q", decision.Action)
	}

	decision.GeneratedBy = "llm"
	decision.AnalyzedAt = time.Now().UTC()
	if decision.Action == domain.ActionCreateBug && decision.SuggestedIssueBody == "" {
		decision.SuggestedIssueSummary, decision.SuggestedIssueBody = s.buildIssueSuggestion(test, history, candidates)
	}

	return decision, nil
}

func (s *Service) buildIssueSuggestion(test domain.TestResult, history domain.HistoryStats, candidates []domain.KnowledgeDocument) (string, string) {
	summary := fmt.Sprintf("[autotest] %s", firstNonEmpty(test.FullName, test.Name))
	if test.Message != "" {
		summary += ": " + trimForPrompt(oneLine(test.Message), 120)
	}

	lines := []string{
		"Autogenerated by ai-model triage.",
		"",
		"Launch: " + renderTemplate(s.cfg.Allure.LaunchURLTemplate, s.cfg.Allure.BaseURL, test.LaunchID, test.ID),
		"Test result: " + renderTemplate(s.cfg.Allure.TestURLTemplate, s.cfg.Allure.BaseURL, test.LaunchID, test.ID),
		"Test name: " + firstNonEmpty(test.FullName, test.Name),
	}
	if test.HistoryID != "" {
		lines = append(lines, "History ID: "+test.HistoryID)
	}
	if test.FailureStep != "" {
		lines = append(lines, "Failure step: "+test.FailureStep)
	}
	if test.Message != "" {
		lines = append(lines, "Message: "+oneLine(test.Message))
	}
	lines = append(lines, fmt.Sprintf("History summary: total=%d passed=%d failed=%d pass_rate=%.2f consecutive_fails=%d",
		history.Total, history.Passed, history.Failed, history.PassRate, history.ConsecutiveFails))
	if len(candidates) > 0 {
		lines = append(lines, "Top related candidates:")
		for _, candidate := range candidates[:minInt(3, len(candidates))] {
			lines = append(lines, fmt.Sprintf("- %s %s score=%.2f", candidate.SourceType, candidate.SourceKey, candidate.CombinedScore))
		}
	}
	if test.Trace != "" {
		lines = append(lines, "", "Trace:", trimForPrompt(test.Trace, 5000))
	}

	return summary, strings.Join(lines, "\n")
}

func scoreCandidates(exact, lexical, semantic []domain.KnowledgeDocument) []domain.KnowledgeDocument {
	candidates := mergeCandidates(exact, lexical, semantic)
	for i := range candidates {
		exactBonus := 0.0
		if candidates[i].SourceType == "failed_test" && candidates[i].CombinedScore >= 1 {
			exactBonus = 0.15
		}
		candidates[i].CombinedScore = clamp(
			exactBonus+(candidates[i].LexicalScore*0.45)+(candidates[i].SemanticScore*0.55),
			maxFloat(candidates[i].CombinedScore, 0),
			1,
		)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].CombinedScore > candidates[j].CombinedScore
	})
	return candidates
}

func toBriefCandidates(candidates []domain.KnowledgeDocument) []domain.CandidateBrief {
	brief := make([]domain.CandidateBrief, 0, minInt(5, len(candidates)))
	for _, candidate := range candidates[:minInt(5, len(candidates))] {
		brief = append(brief, domain.CandidateBrief{
			SourceType: candidate.SourceType,
			SourceKey:  candidate.SourceKey,
			Title:      candidate.Title,
			Score:      candidate.CombinedScore,
		})
	}
	return brief
}

func buildQueries(test domain.TestResult) []string {
	values := []string{
		firstNonEmpty(test.FullName, test.Name),
		test.FailureStep,
		test.Message,
		trimForPrompt(test.Trace, 600),
	}

	var queries []string
	full := strings.TrimSpace(strings.Join(values, "\n"))
	if full != "" {
		queries = append(queries, full)
	}

	compact := strings.TrimSpace(strings.Join([]string{test.Name, test.Message, test.FailureStep}, " "))
	if compact != "" {
		queries = append(queries, compact)
	}

	if test.HistoryID != "" {
		queries = append(queries, test.HistoryID+" "+compact)
	}

	return dedupeStrings(queries)
}

func topCandidateByType(candidates []domain.KnowledgeDocument, sourceType string) *domain.KnowledgeDocument {
	for i := range candidates {
		if candidates[i].SourceType == sourceType {
			return &candidates[i]
		}
	}
	return nil
}

func extractDefectID(candidate *domain.KnowledgeDocument) *int64 {
	if candidate == nil {
		return nil
	}
	if raw, ok := candidate.Metadata["defect_id"]; ok {
		switch value := raw.(type) {
		case float64:
			id := int64(value)
			return &id
		case int64:
			id := value
			return &id
		}
	}
	return nil
}

func extractIssueKey(candidate *domain.KnowledgeDocument) string {
	if candidate == nil {
		return ""
	}
	if raw, ok := candidate.Metadata["issue_key"]; ok {
		if value, ok := raw.(string); ok {
			return value
		}
	}
	return ""
}

func isTransientFailure(test domain.TestResult) bool {
	haystack := strings.Join([]string{test.Message, test.FailureStep, test.Trace}, "\n")
	return transientFailurePattern.MatchString(haystack)
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func renderTemplate(template, baseURL string, launchID, testResultID int64) string {
	replacer := strings.NewReplacer(
		"{base_url}", strings.TrimRight(baseURL, "/"),
		"{launch_id}", fmt.Sprintf("%d", launchID),
		"{test_result_id}", fmt.Sprintf("%d", testResultID),
	)
	return replacer.Replace(template)
}

func trimForPrompt(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(value[:limit]) + "..."
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func clamp(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
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
			existing.LexicalScore = maxFloat(existing.LexicalScore, candidate.LexicalScore)
			existing.SemanticScore = maxFloat(existing.SemanticScore, candidate.SemanticScore)
			existing.CombinedScore = maxFloat(existing.CombinedScore, candidate.CombinedScore)
			if candidate.Content != "" && len(candidate.Content) > len(existing.Content) {
				existing.Content = candidate.Content
			}
			if existing.Metadata == nil {
				existing.Metadata = candidate.Metadata
			}
			byKey[candidate.SourceKey] = existing
		}
	}

	merged := make([]domain.KnowledgeDocument, 0, len(byKey))
	for _, candidate := range byKey {
		merged = append(merged, candidate)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].CombinedScore > merged[j].CombinedScore
	})
	return merged
}
