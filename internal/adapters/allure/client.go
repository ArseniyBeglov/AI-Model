package allure

import (
	"ai-model/internal/config"
	"ai-model/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	statusFailed = "failed"
	statusBroken = "broken"
	statusPassed = "passed"
)

type Client struct {
	cfg        config.AllureConfig
	httpClient *http.Client
	token      string
}

type pageResponse[T any] struct {
	Content []T `json:"content"`
}

type launchPayload struct {
	ID          int64           `json:"id"`
	ProjectID   int64           `json:"projectId"`
	Name        string          `json:"name"`
	Closed      bool            `json:"closed"`
	CreatedDate json.RawMessage `json:"createdDate"`
	Raw         json.RawMessage `json:"-"`
}

type statusDetailsPayload struct {
	Message string `json:"message"`
	Trace   string `json:"trace"`
}

type stepPayload struct {
	Name   string        `json:"name"`
	Status string        `json:"status"`
	Steps  []stepPayload `json:"steps"`
}

type testResultPayload struct {
	ID            int64                `json:"id"`
	ProjectID     int64                `json:"projectId"`
	LaunchID      int64                `json:"launchId"`
	TestCaseID    int64                `json:"testCaseId"`
	HistoryID     string               `json:"historyId"`
	FullName      string               `json:"fullName"`
	Name          string               `json:"name"`
	Status        string               `json:"status"`
	Message       string               `json:"message"`
	Trace         string               `json:"trace"`
	StatusDetails statusDetailsPayload `json:"statusDetails"`
	Steps         []stepPayload        `json:"steps"`
	Start         json.RawMessage      `json:"start"`
	Stop          json.RawMessage      `json:"stop"`
	Duration      int64                `json:"duration"`
	Raw           json.RawMessage      `json:"-"`
}

type retryPayload struct {
	Status string `json:"status"`
}

type defectPayload struct {
	ID      int64           `json:"id"`
	Name    string          `json:"name"`
	Summary string          `json:"summary"`
	Status  string          `json:"status"`
	Issue   issuePayload    `json:"issue"`
	Raw     json.RawMessage `json:"-"`
}

type issuePayload struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Summary string `json:"summary"`
	Status  string `json:"status"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
}

func NewClient(cfg config.AllureConfig) *Client {
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
		token: cfg.BearerToken,
	}
}

func (c *Client) GetLaunch(ctx context.Context, launchID int64) (domain.Launch, error) {
	var payload launchPayload
	if err := c.getJSON(ctx, "/api/launch/"+strconv.FormatInt(launchID, 10), nil, &payload); err != nil {
		return domain.Launch{}, err
	}

	return domain.Launch{
		ID:        payload.ID,
		ProjectID: payload.ProjectID,
		Name:      payload.Name,
		Closed:    payload.Closed,
		CreatedAt: parseTime(payload.CreatedDate),
		Raw:       payload.Raw,
	}, nil
}

func (c *Client) ListRecentLaunches(ctx context.Context, limit int) ([]domain.Launch, error) {
	if limit <= 0 {
		return nil, nil
	}

	page := 0
	pageSize := c.cfg.PageSize
	if pageSize <= 0 {
		pageSize = 200
	}

	launches := make([]domain.Launch, 0, limit)
	for len(launches) < limit {
		var response pageResponse[launchPayload]
		params := url.Values{}
		params.Set("projectId", strconv.FormatInt(c.cfg.ProjectID, 10))
		params.Set("page", strconv.Itoa(page))
		params.Set("size", strconv.Itoa(minInt(pageSize, limit-len(launches))))
		params.Set("sort", "created_date,DESC")

		if err := c.getJSON(ctx, "/api/launch", params, &response); err != nil {
			return nil, err
		}

		if len(response.Content) == 0 {
			break
		}

		for _, item := range response.Content {
			launches = append(launches, domain.Launch{
				ID:        item.ID,
				ProjectID: item.ProjectID,
				Name:      item.Name,
				Closed:    item.Closed,
				CreatedAt: parseTime(item.CreatedDate),
				Raw:       item.Raw,
			})
			if len(launches) == limit {
				break
			}
		}
		page++
	}

	return launches, nil
}

func (c *Client) ListLaunchResults(ctx context.Context, launchID int64) ([]domain.TestResult, error) {
	page := 0
	pageSize := c.cfg.PageSize
	if pageSize <= 0 {
		pageSize = 200
	}

	var results []domain.TestResult
	for {
		var response pageResponse[testResultPayload]
		params := url.Values{}
		params.Set("launchId", strconv.FormatInt(launchID, 10))
		params.Set("page", strconv.Itoa(page))
		params.Set("size", strconv.Itoa(pageSize))
		params.Set("sort", "createdDate,DESC")

		if err := c.getJSON(ctx, "/api/testresult", params, &response); err != nil {
			return nil, err
		}

		if len(response.Content) == 0 {
			break
		}

		for _, payload := range response.Content {
			result := domain.TestResult{
				ID:           payload.ID,
				ProjectID:    firstNonZero(payload.ProjectID, c.cfg.ProjectID),
				LaunchID:     firstNonZero(payload.LaunchID, launchID),
				TestCaseID:   payload.TestCaseID,
				HistoryID:    payload.HistoryID,
				FullName:     payload.FullName,
				Name:         payload.Name,
				Status:       strings.ToLower(payload.Status),
				Message:      firstNonEmpty(payload.Message, payload.StatusDetails.Message),
				Trace:        firstNonEmpty(payload.Trace, payload.StatusDetails.Trace),
				FailureStep:  failedStep(payload.Steps),
				StartAt:      parseTime(payload.Start),
				EndAt:        parseTime(payload.Stop),
				DurationMS:   payload.Duration,
				DefectIDs:    nil,
				RetriesCount: 0,
				HasRetryPass: false,
				Raw:          payload.Raw,
			}

			if isFailure(result.Status) {
				retries, err := c.ListRetries(ctx, result.ID)
				if err == nil {
					result.RetriesCount = len(retries)
					result.HasRetryPass = anyPassed(retries)
				}

				defects, err := c.ListDefects(ctx, result.ID)
				if err == nil {
					for _, defect := range defects {
						result.DefectIDs = append(result.DefectIDs, defect.ID)
					}
				}
			}

			results = append(results, result)
		}

		page++
	}

	return results, nil
}

func (c *Client) ListDefects(ctx context.Context, testResultID int64) ([]domain.Defect, error) {
	var response pageResponse[defectPayload]
	params := url.Values{}
	params.Set("page", "0")
	params.Set("size", "100")
	params.Set("sort", "name,ASC")
	if err := c.getJSON(ctx, "/api/testresult/"+strconv.FormatInt(testResultID, 10)+"/defect", params, &response); err != nil {
		return nil, err
	}

	defects := make([]domain.Defect, 0, len(response.Content))
	for _, item := range response.Content {
		defect := domain.Defect{
			ID:        item.ID,
			ProjectID: c.cfg.ProjectID,
			Name:      firstNonEmpty(item.Name, item.Issue.Name),
			Summary:   firstNonEmpty(item.Summary, item.Issue.Summary),
			Status:    firstNonEmpty(item.Status, item.Issue.Status),
			IssueKey:  item.Issue.Key,
			IssueURL:  item.Issue.URL,
			Raw:       item.Raw,
		}
		defects = append(defects, defect)
	}

	return defects, nil
}

func (c *Client) ListRetries(ctx context.Context, testResultID int64) ([]retryPayload, error) {
	var response pageResponse[retryPayload]
	params := url.Values{}
	params.Set("page", "0")
	params.Set("size", "100")
	params.Set("sort", "start,DESC")
	if err := c.getJSON(ctx, "/api/testresult/"+strconv.FormatInt(testResultID, 10)+"/retries", params, &response); err != nil {
		return nil, err
	}
	return response.Content, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, params url.Values, dst any) error {
	if err := c.ensureToken(ctx); err != nil {
		return err
	}

	reqURL, err := c.buildURL(endpoint, params)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("perform GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("GET %s failed with %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode response %s: %w", endpoint, err)
	}

	switch typed := dst.(type) {
	case *launchPayload:
		typed.Raw = append(typed.Raw[:0], body...)
	case *pageResponse[launchPayload]:
		assignLaunchRaw(typed, body)
	case *pageResponse[testResultPayload]:
		assignResultRaw(typed, body)
	case *pageResponse[defectPayload]:
		assignDefectRaw(typed, body)
	}

	return nil
}

func (c *Client) ensureToken(ctx context.Context) error {
	if c.token != "" {
		return nil
	}
	if c.cfg.UserToken == "" {
		return fmt.Errorf("allure auth is not configured: either ALLURE_BEARER_TOKEN or ALLURE_USER_TOKEN is required")
	}

	form := url.Values{}
	form.Set("grant_type", "apitoken")
	form.Set("scope", "openid")
	form.Set("token", c.cfg.UserToken)

	reqURL, err := c.buildURL("/api/uaa/oauth/token", nil)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return fmt.Errorf("create auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Expect", "")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("exchange allure user token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("allure auth failed with %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var token tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return fmt.Errorf("decode allure auth response: %w", err)
	}
	if token.AccessToken == "" {
		return fmt.Errorf("allure auth response does not contain access token")
	}

	c.token = token.AccessToken
	return nil
}

func (c *Client) buildURL(endpoint string, params url.Values) (string, error) {
	base, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return "", fmt.Errorf("parse allure base url: %w", err)
	}
	base.Path = path.Join(base.Path, endpoint)
	base.RawQuery = params.Encode()
	return base.String(), nil
}

func failedStep(steps []stepPayload) string {
	for _, step := range steps {
		if isFailure(step.Status) {
			if nested := failedStep(step.Steps); nested != "" {
				return step.Name + " > " + nested
			}
			return step.Name
		}
		if nested := failedStep(step.Steps); nested != "" {
			if step.Name == "" {
				return nested
			}
			return step.Name + " > " + nested
		}
	}
	return ""
}

func anyPassed(retries []retryPayload) bool {
	for _, retry := range retries {
		if strings.EqualFold(retry.Status, statusPassed) {
			return true
		}
	}
	return false
}

func parseTime(raw json.RawMessage) time.Time {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}
	}

	var ms int64
	if err := json.Unmarshal(raw, &ms); err == nil {
		if ms == 0 {
			return time.Time{}
		}
		return time.UnixMilli(ms).UTC()
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, parseErr := time.Parse(layout, s); parseErr == nil {
				return parsed.UTC()
			}
		}
	}

	return time.Time{}
}

func isFailure(status string) bool {
	normalized := strings.ToLower(status)
	return normalized == statusFailed || normalized == statusBroken
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func assignLaunchRaw(response *pageResponse[launchPayload], body []byte) {
	var raw pageResponse[json.RawMessage]
	if err := json.Unmarshal(body, &raw); err != nil {
		return
	}
	for i := range response.Content {
		if i < len(raw.Content) {
			response.Content[i].Raw = append(response.Content[i].Raw[:0], raw.Content[i]...)
		}
	}
}

func assignResultRaw(response *pageResponse[testResultPayload], body []byte) {
	var raw pageResponse[json.RawMessage]
	if err := json.Unmarshal(body, &raw); err != nil {
		return
	}
	for i := range response.Content {
		if i < len(raw.Content) {
			response.Content[i].Raw = append(response.Content[i].Raw[:0], raw.Content[i]...)
		}
	}
}

func assignDefectRaw(response *pageResponse[defectPayload], body []byte) {
	var raw pageResponse[json.RawMessage]
	if err := json.Unmarshal(body, &raw); err != nil {
		return
	}
	for i := range response.Content {
		if i < len(raw.Content) {
			response.Content[i].Raw = append(response.Content[i].Raw[:0], raw.Content[i]...)
		}
	}
}
