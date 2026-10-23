package evolution

import "testing"

func TestHistorySurvivesNewReaderAndRejectsBadID(t *testing.T) {
	dir := t.TempDir()
	before := RetrievalRun{Strategy: "fixed", DatasetHash: "same", Cases: 1, Results: []CaseResult{{ID: "c1", ExpectedSource: "doc", RetrievedSources: []string{"other"}}}}
	after := RetrievalRun{Strategy: "recursive", DatasetHash: "same", Cases: 1, HitRate: 1, Results: []CaseResult{{ID: "c1", ExpectedSource: "doc", RetrievedSources: []string{"doc"}, Hit: true}}}
	exp, err := NewExperiment(before, after, Prediction{ExpectedHitRateDeltaMin: 0.5, ExpectedHitRateDeltaMax: 1}, Decision{Passed: true, Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := (History{Dir: dir}).Save(exp); err != nil {
		t.Fatal(err)
	}
	reader := History{Dir: dir}
	got, err := reader.Get(exp.ID)
	if err != nil || got.ID != exp.ID || got.AnswerQuality != "not_evaluated" || len(got.Failures) != 1 || !got.Failures[0].ResolvedByCandidate {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	list, err := reader.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	if _, err := reader.Get("../../secret"); err != ErrExperimentNotFound {
		t.Fatalf("bad ID err=%v", err)
	}
}
