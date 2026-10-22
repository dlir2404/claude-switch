package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"claude-switch/pkg/config"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type ChatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func GenerateHandoffSummary(cfg *config.Config, prompt string) (string, error) {
	if !cfg.LLM.Enabled {
		return "", fmt.Errorf("LLM engine is disabled in config")
	}

	baseURL := strings.TrimRight(cfg.LLM.BaseURL, "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:20128/v1"
	}
	url := fmt.Sprintf("%s/chat/completions", baseURL)

	apiKey := cfg.GetResolvedAPIKey()
	timeout := time.Duration(cfg.LLM.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	model := cfg.LLM.Model
	if model == "" {
		model = "gpt-4o-mini"
	}

	reqBody := ChatCompletionRequest{
		Model: model,
		Messages: []ChatMessage{
			{Role: "system", Content: SystemHandoffPrompt},
			{Role: "user", Content: prompt},
		},
		Temperature: cfg.LLM.Temperature,
		MaxTokens:   cfg.LLM.MaxTokens,
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
	}

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM endpoint returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var chatResp ChatCompletionResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return "", fmt.Errorf("failed to parse LLM response: %w", err)
	}

	if chatResp.Error != nil && chatResp.Error.Message != "" {
		return "", fmt.Errorf("LLM error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}

	return strings.TrimSpace(chatResp.Choices[0].Message.Content), nil
}

func PingTest(cfg *config.Config) (time.Duration, error) {
	baseURL := strings.TrimRight(cfg.LLM.BaseURL, "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:20128/v1"
	}
	url := fmt.Sprintf("%s/chat/completions", baseURL)
	apiKey := cfg.GetResolvedAPIKey()

	reqBody := ChatCompletionRequest{
		Model: cfg.LLM.Model,
		Messages: []ChatMessage{
			{Role: "user", Content: "ping"},
		},
		MaxTokens: 5,
	}
	jsonBytes, _ := json.Marshal(reqBody)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
	}

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		return duration, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return duration, fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}

	return duration, nil
}
