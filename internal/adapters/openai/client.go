package openai

import (
	"ai-model/internal/config"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

type ChatModel struct {
	cfg        config.OpenAIConfig
	httpClient *http.Client
}

type Embedder struct {
	cfg        config.EmbeddingConfig
	httpClient *http.Client
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

type chatRequest struct {
	Model       string         `json:"model"`
	Temperature float64        `json:"temperature,omitempty"`
	Messages    []chatMessage  `json:"messages"`
	ResponseFmt map[string]any `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

func NewChatModel(cfg config.OpenAIConfig) *ChatModel {
	return &ChatModel{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

func NewEmbedder(cfg config.EmbeddingConfig) *Embedder {
	return &Embedder{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

func (e *Embedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	requestBody, err := json.Marshal(embeddingRequest{
		Model: e.cfg.Model,
		Input: inputs,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal embeddings request: %w", err)
	}

	var response embeddingResponse
	if err := e.doJSON(ctx, e.cfg.BaseURL, "/embeddings", requestBody, e.cfg.APIKey, &response); err != nil {
		return nil, err
	}

	vectors := make([][]float32, 0, len(response.Data))
	for _, item := range response.Data {
		vectors = append(vectors, item.Embedding)
	}

	return vectors, nil
}

func (m *ChatModel) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	requestBody, err := json.Marshal(chatRequest{
		Model:       m.cfg.Model,
		Temperature: m.cfg.Temperature,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		ResponseFmt: map[string]any{
			"type": "json_object",
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}

	var response chatResponse
	if err := m.doJSON(ctx, m.cfg.BaseURL, "/chat/completions", requestBody, m.cfg.APIKey, &response); err != nil {
		return "", err
	}
	if len(response.Choices) == 0 {
		return "", fmt.Errorf("chat completion response does not contain choices")
	}

	return strings.TrimSpace(response.Choices[0].Message.Content), nil
}

func (m *ChatModel) doJSON(ctx context.Context, baseURL, endpoint string, payload []byte, apiKey string, dst any) error {
	reqURL, err := buildURL(baseURL, endpoint)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request %s: %w", endpoint, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("perform POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("POST %s failed with %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response %s: %w", endpoint, err)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode response %s: %w", endpoint, err)
	}
	return nil
}

func (e *Embedder) doJSON(ctx context.Context, baseURL, endpoint string, payload []byte, apiKey string, dst any) error {
	reqURL, err := buildURL(baseURL, endpoint)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request %s: %w", endpoint, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("perform POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("POST %s failed with %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response %s: %w", endpoint, err)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode response %s: %w", endpoint, err)
	}
	return nil
}

func buildURL(baseURL, endpoint string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	parsed.Path = path.Join(parsed.Path, endpoint)
	return parsed.String(), nil
}
