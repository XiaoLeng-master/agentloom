// Package core defines the agent runtime primitives: messages, tools, the LLM
// client contract, the ReAct loop, and the factory/registry that builds agents.
package core

// Role identifies the speaker of a Message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is a model-issued request to invoke a tool. It is produced by the
// LLMClient and consumed by the Agent loop.
type ToolCall struct {
	ID   string         // model-assigned id; echoed back in the tool result message
	Name string         // tool name, must match a registered Tool's Name
	Args map[string]any // arguments, validated against Tool.Schema
}

// Message is a single conversational turn. An assistant message either carries
// a final answer in Content (when ToolCalls is empty) or one or more ToolCalls.
// A tool message carries a tool result in Content and links back via ToolCallID.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string // set when Role == RoleTool, links back to the ToolCall.ID
}
