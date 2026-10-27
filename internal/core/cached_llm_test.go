package core

import (
	"context"
	"testing"
)

type countingLLM struct {
	calls int
}

func (c *countingLLM) Complete(context.Context, []Message) (Message, error) {
	c.calls++
	return Message{Role: RoleAssistant, Content: "cached-answer"}, nil
}

type memResultCache struct{ items map[string]string }

func (m *memResultCache) Get(prefix []byte) (string, bool) {
	if m.items == nil {
		return "", false
	}
	v, ok := m.items[string(prefix)]
	return v, ok
}
func (m *memResultCache) Set(prefix []byte, result string) {
	if m.items == nil {
		m.items = map[string]string{}
	}
	m.items[string(prefix)] = result
}

func TestCachedLLMSkipsRepeatedCall(t *testing.T) {
	inner := &countingLLM{}
	client := NewCachedLLM(inner, &memResultCache{})
	msgs := []Message{{Role: RoleSystem, Content: "system"}, {Role: RoleUser, Content: "same question"}}
	for i := 0; i < 4; i++ {
		msg, err := client.Complete(context.Background(), msgs)
		if err != nil || msg.Content != "cached-answer" {
			t.Fatalf("call %d: %+v %v", i, msg, err)
		}
	}
	if inner.calls != 1 || client.Calls() != 1 {
		t.Fatalf("inner=%d cached=%d", inner.calls, client.Calls())
	}
	msgs[1].Content = "other question"
	if _, err := client.Complete(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 2 {
		t.Fatalf("different prefix should call the model, calls=%d", inner.calls)
	}
}
