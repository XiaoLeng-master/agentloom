package evolution

import (
	"errors"
	"testing"

	"github.com/XiaoLeng-master/agentloom/internal/eval"
)

type passingJudge struct{}

func (passingJudge) Name() string { return "test_judge" }
func (passingJudge) Eval(eval.Trace) eval.Score {
	return eval.Score{Name: "test_judge", Value: 0.9, Pass: true}
}

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

func TestTraceBatchScoresActualAnswerOnlyWithMatchingGroundTruth(t *testing.T) {
	traces := []RuntimeTrace{
		{ID: "matched", Query: "what is agentloom", Answer: "agentloom runtime", ContractPhases: []string{"plan", "execute", "verify"}, RetrievalSources: []string{"guide.md"}},
		{ID: "unmatched", Query: "unlabelled question", Answer: "answer", ContractPhases: []string{"plan", "execute", "verify"}, RetrievalSources: []string{"guide.md"}},
	}
	batch, err := NewTraceBatchWithCases(traces, []Case{{ID: "case-1", Question: "what is agentloom", GroundTruth: "agentloom runtime", SourceDoc: "guide.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Reports[0].AnswerQuality == nil || batch.Reports[0].AnswerQuality.Mode != "deterministic_actual_answer" || batch.Reports[0].AnswerQuality.CaseID != "case-1" || !batch.Reports[0].AnswerQuality.Passed {
		t.Fatalf("matched report=%+v", batch.Reports[0])
	}
	if batch.Reports[1].AnswerQuality != nil {
		t.Fatalf("unmatched trace received a quality score: %+v", batch.Reports[1])
	}
}

func TestTraceBatchUsesConfiguredRealJudge(t *testing.T) {
	trace := RuntimeTrace{ID: "matched", Query: "question", Answer: "answer", ContractPhases: []string{"plan", "execute", "verify"}, RetrievalSources: []string{"guide.md"}}
	batch, err := NewTraceBatchWithCases([]RuntimeTrace{trace}, []Case{{ID: "case-1", Question: "question", GroundTruth: "answer", SourceDoc: "guide.md"}}, passingJudge{})
	if err != nil {
		t.Fatal(err)
	}
	quality := batch.Reports[0].AnswerQuality
	if quality == nil || quality.Mode != "test_judge_actual_answer" || len(quality.Scores) != 5 || quality.Scores[4].Name != "test_judge" || !quality.Passed {
		t.Fatalf("quality=%+v", quality)
	}
}
