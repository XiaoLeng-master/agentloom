package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultModel is the Chat Completions model used when Model is empty.
// agentd uses the same value unless DEEPSEEK_MODEL overrides it.
const DefaultModel = "deepseek-flash"

// DeepSeekClient calls the official Chat Completions endpoint.
type DeepSeekClient struct {
	APIKey     string
	Model      string
	Endpoint   string
	Tools      []map[string]any
	HTTPClient *http.Client
	MaxTokens  int
	NoThinking bool
	// OnCall, when set, receives one record per HTTP attempt. It never
	// carries the API key or request headers.
	OnCall func(CallStats)
}

// CallStats describes one Chat Completions attempt for local metering.
type CallStats struct {
	Model            string        // model requested
	ResponseModel    string        // model reported by the API, if any
	Status           int           // HTTP status, 0 when the request failed before a response
	Latency          time.Duration // wall time of the HTTP exchange
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Err              string
}

// Complete converts runtime messages to DeepSeek's chat format and validates its reply.
func (c DeepSeekClient) Complete(ctx context.Context, msgs []Message) (Message, error) {
	if c.APIKey == "" {
		return Message{}, fmt.Errorf("deepseek API key missing")
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = "https://api.deepseek.com/chat/completions"
	}
	model := c.Model
	if model == "" {
		model = DefaultModel
	}
	items := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		item := map[string]any{"role": m.Role, "content": m.Content}
		if m.Role == RoleTool {
			item["tool_call_id"] = m.ToolCallID
		}
		if len(m.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				args, err := json.Marshal(tc.Args)
				if err != nil {
					return Message{}, err
				}
				calls = append(calls, map[string]any{"id": tc.ID, "type": "function", "function": map[string]any{"name": tc.Name, "arguments": string(args)}})
			}
			item["tool_calls"] = calls
		}
		items = append(items, item)
	}
	payload := map[string]any{"model": model, "messages": items, "stream": false}
	if c.MaxTokens > 0 {
		payload["max_tokens"] = c.MaxTokens
	}
	if c.NoThinking {
		payload["thinking"] = map[string]string{"type": "disabled"}
	}
	if len(c.Tools) > 0 {
		payload["tools"] = c.Tools
		if len(msgs) > 0 && msgs[len(msgs)-1].Role == RoleTool {
			payload["tool_choice"] = "none"
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Message{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	stats := CallStats{Model: model}
	report := func(err error) {
		if err != nil {
			stats.Err = err.Error()
		}
		if c.OnCall != nil {
			c.OnCall(stats)
		}
	}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		stats.Latency = time.Since(started)
		err = fmt.Errorf("deepseek request: %w", err)
		report(err)
		return Message{}, err
	}
	defer resp.Body.Close()
	stats.Status = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		stats.Latency = time.Since(started)
		err = fmt.Errorf("deepseek HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
		report(err)
		return Message{}, err
	}
	var decoded struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&decoded)
	stats.Latency = time.Since(started)
	if decodeErr != nil {
		err = fmt.Errorf("deepseek response: %w", decodeErr)
		report(err)
		return Message{}, err
	}
	stats.ResponseModel = decoded.Model
	stats.PromptTokens = decoded.Usage.PromptTokens
	stats.CompletionTokens = decoded.Usage.CompletionTokens
	stats.TotalTokens = decoded.Usage.TotalTokens
	if len(decoded.Choices) == 0 {
		err = fmt.Errorf("deepseek response has no choices")
		report(err)
		return Message{}, err
	}
	report(nil)
	msg := decoded.Choices[0].Message
	out := Message{Role: RoleAssistant, Content: msg.Content}
	for _, call := range msg.ToolCalls {
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args == nil || strings.TrimSpace(call.Function.Name) == "" {
			return Message{}, fmt.Errorf("deepseek invalid tool call")
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: call.ID, Name: call.Function.Name, Args: args})
	}
	return out, nil
}
