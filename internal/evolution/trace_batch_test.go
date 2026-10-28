package evolution

import (
	"errors"
	"testing"
)

func TestTraceBatchEvaluatesAndPersistsRuntimeEvidence(t *testing.T) {
	batch, err := NewTraceBatch([]RuntimeTrace{
		{ID: "trace-pass", Query: "question", Answer: "answer", ContractPhases: []string{"plan", "execute", "verify"}, RetrievalSources: []string{"guide.md"}},
		{ID: "trace-fail", Query: "question", ContractPhases: []string{"plan", "execute"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Mode != "runtime_evidence_rules" || batch.Passed != 1 || batch.Failed != 1 {
		t.Fatalf("batch=%+v", batch)
	}
	if batch.FailureClusters["answer_present"] != 1 || batch.FailureClusters["retrieval_evidence_present"] != 1 || batch.FailureClusters["contract_chain_complete"] != 1 {
		t.Fatalf("clusters=%+v", batch.FailureClusters)
	}
	history := TraceBatchHistory{Dir: t.TempDir()}
	if err := history.Save(batch); err != nil {
		t.Fatal(err)
	}
	got, err := history.Get(batch.ID)
	if err != nil || len(got.TraceIDs) != 2 || got.TraceIDs[0] != "trace-pass" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	list, err := history.List()
	if err != nil || len(list) != 1 || list[0].ID != batch.ID {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	if _, err := history.Get("../../private"); !errors.Is(err, ErrTraceBatchNotFound) {
		t.Fatalf("bad id err=%v", err)
	}
}
