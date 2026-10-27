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

// DeepSeekClient calls the official Chat Completions endpoint.
type DeepSeekClient struct {
	APIKey     string
	Model      string
	Endpoint   string
	Tools      []map[string]any
	HTTPClient *http.Client
	MaxTokens  int
	NoThinking bool
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
		model = "deepseek-chat"
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
	resp, err := client.Do(req)
	if err != nil {
		return Message{}, fmt.Errorf("deepseek request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Message{}, fmt.Errorf("deepseek HTTP %d", resp.StatusCode)
	}
	var decoded struct {
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
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&decoded); err != nil {
		return Message{}, fmt.Errorf("deepseek response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return Message{}, fmt.Errorf("deepseek response has no choices")
	}
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
