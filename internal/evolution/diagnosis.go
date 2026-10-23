package evolution

// Failure identifies a retrieval miss and the evidence used for attribution.
type Failure struct {
	CaseID              string   `json:"case_id"`
	ExpectedSource      string   `json:"expected_source"`
	RetrievedSources    []string `json:"retrieved_sources"`
	Cause               string   `json:"cause"`
	ResolvedByCandidate bool     `json:"resolved_by_candidate"`
}

// DiagnoseRetrieval compares the same case IDs under baseline and candidate.
// A source miss is a deterministic retrieval-layer diagnosis, not an LLM judgement.
func DiagnoseRetrieval(before, after RetrievalRun) []Failure {
	if before.DatasetHash != after.DatasetHash || before.Cases != after.Cases {
		return nil
	}
	changed := make(map[string]bool, len(after.Results))
	for _, result := range after.Results {
		changed[result.ID] = result.Hit
	}
	var failures []Failure
	for _, result := range before.Results {
		if result.Hit {
			continue
		}
		failures = append(failures, Failure{CaseID: result.ID, ExpectedSource: result.ExpectedSource,
			RetrievedSources: result.RetrievedSources, Cause: "expected source absent from top-3 retrieval",
			ResolvedByCandidate: changed[result.ID]})
	}
	return failures
}
