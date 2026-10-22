package core

import "context"

// LLMClient abstracts the model backend. D1 uses a mock implementation in
// tests; D3 swaps in an OpenAI-compatible / Ollama client. The returned
// Message either carries a final answer in Content or one or more ToolCalls
// to execute; the Agent loop decides which branch applies.
type LLMClient interface {
	Complete(ctx context.Context, msgs []Message) (Message, error)
}
