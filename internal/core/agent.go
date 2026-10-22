package core

import (
	"context"
	"errors"
	"fmt"
)

// DefaultMaxIter caps the ReAct loop to prevent runaway tool chains. It can be
// overridden per-agent via NewAgent; values <= 0 fall back to this default.
const DefaultMaxIter = 10

// ErrMaxIterations is returned when the loop hits its iteration cap without the
// model producing a final answer.
var ErrMaxIterations = errors.New("max iterations exceeded")

// Agent runs a ReAct loop: Think (model) -> Act (tool) -> Observe (result) ->
// repeat until the model emits a final answer or the iteration cap is hit.
type Agent struct {
	name         string
	systemPrompt string
	llm          LLMClient
	tools        map[string]Tool
	maxIter      int
}

// NewAgent builds an Agent from its dependencies. The tools slice is indexed
// by Name for O(1) lookup during a turn. maxIter <= 0 falls back to DefaultMaxIter.
func NewAgent(name, systemPrompt string, llm LLMClient, tools []Tool, maxIter int) *Agent {
	tm := make(map[string]Tool, len(tools))
	for _, t := range tools {
		tm[t.Name()] = t
	}
	if maxIter <= 0 {
		maxIter = DefaultMaxIter
	}
	return &Agent{
		name:         name,
		systemPrompt: systemPrompt,
		llm:          llm,
		tools:        tm,
		maxIter:      maxIter,
	}
}

// Name returns the agent identifier.
func (a *Agent) Name() string { return a.name }

// Run executes the ReAct loop for a single user turn. It returns the model's
// final answer when the model stops emitting tool calls, or an error when the
// context is cancelled, the LLM call fails, or the iteration cap is reached.
//
// Robustness rules baked in (the three an interviewer will probe):
//   - iteration cap: a buggy model that always issues tool calls cannot loop
//     forever;
//   - context propagation: a cancelled HTTP request unwinds through every
//     tool call, not just the top-level LLM call;
//   - degraded-not-crashed: an unknown tool or a tool error becomes an
//     Observation fed back to the model, letting it self-correct.
func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	msgs := []Message{
		{Role: RoleSystem, Content: a.systemPrompt},
		{Role: RoleUser, Content: userInput},
	}
	for iter := 0; iter < a.maxIter; iter++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		resp, err := a.llm.Complete(ctx, msgs)
		if err != nil {
			return "", fmt.Errorf("agent %q: llm complete: %w", a.name, err)
		}
		// No tool calls means the model produced the final answer.
		if len(resp.ToolCalls) == 0 {
			return resp.Content, nil
		}
		msgs = append(msgs, resp)
		// Act + Observe: run every tool call in this turn.
		for _, tc := range resp.ToolCalls {
			tool, ok := a.tools[tc.Name]
			if !ok {
				msgs = append(msgs, Message{
					Role:       RoleTool,
					Content:    fmt.Sprintf("error: unknown tool %q", tc.Name),
					ToolCallID: tc.ID,
				})
				continue
			}
			result, err := tool.Call(ctx, tc.Args)
			if err != nil {
				result = fmt.Sprintf("error: tool %q failed: %v", tc.Name, err)
			}
			msgs = append(msgs, Message{
				Role:       RoleTool,
				Content:    result,
				ToolCallID: tc.ID,
			})
		}
	}
	return "", fmt.Errorf("agent %q: %w", a.name, ErrMaxIterations)
}
