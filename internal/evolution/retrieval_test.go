package evolution

import (
	"testing"

	"github.com/XiaoLeng-master/agentloom/internal/config"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

func TestImproveRetrievalRecordsCasesAndPromotes(t *testing.T) {
	cases := []Case{{ID: "a", Question: "needle", SourceDoc: "a"}}
	docs := []rag.Document{{Source: "a", Text: "needle"}}
	mgr := config.NewManager()
	mgr.Register("demo", config.Config{Name: "demo", SystemPrompt: "old", ChunkStrategy: "fixed"})
	before, after, decision, err := ImproveRetrieval(mgr, "demo", config.Config{Name: "demo", SystemPrompt: "old"}, cases, docs, "hash", rag.FixedSplitter{Size: 40}, rag.RecursiveSplitter{Size: 60, Overlap: 10}, Prediction{})
	if err != nil || !decision.Passed || len(before.Results) != 1 || len(after.Results) != 1 || !before.Results[0].Hit {
		t.Fatalf("before=%+v after=%+v decision=%+v err=%v", before, after, decision, err)
	}
	h, err := mgr.Acquire("demo")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()
	if h.Config().Version != 2 || h.Config().ChunkStrategy != "recursive" {
		t.Fatalf("active=%+v", h.Config())
	}
}
