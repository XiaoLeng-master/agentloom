// Command evolve runs one offline retrieval improvement with per-case evidence.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/XiaoLeng-master/agentloom/internal/config"
	"github.com/XiaoLeng-master/agentloom/internal/evolution"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

func main() {
	cases, docs, hash, err := evolution.LoadPublicCorpus("datasets/rag_eval_98.json")
	if err != nil {
		fail(err)
	}
	mgr := config.NewManager()
	mgr.Register("kb-agent", config.Config{Name: "kb-agent", SystemPrompt: "Retrieval baseline", ChunkStrategy: "fixed"})
	before, after, decision, err := evolution.ImproveRetrieval(mgr, "kb-agent",
		config.Config{Name: "kb-agent", SystemPrompt: "Retrieval baseline"}, cases, docs, hash,
		rag.FixedSplitter{Size: 40}, rag.RecursiveSplitter{Size: 60, Overlap: 10},
		evolution.Prediction{ExpectedHitRateDeltaMin: 0.10, ExpectedHitRateDeltaMax: 0.25})
	if err != nil {
		fail(err)
	}
	active, err := mgr.Acquire("kb-agent")
	if err != nil {
		fail(err)
	}
	defer active.Release()
	result := struct {
		Before       evolution.RetrievalRun `json:"before"`
		After        evolution.RetrievalRun `json:"after"`
		Prediction   evolution.Prediction   `json:"prediction"`
		ActualDelta  float64                `json:"actual_delta"`
		Failures     []evolution.Failure    `json:"baseline_failures"`
		Decision     evolution.Decision     `json:"decision"`
		ActiveConfig config.Config          `json:"active_config"`
	}{before, after, evolution.Prediction{ExpectedHitRateDeltaMin: 0.10, ExpectedHitRateDeltaMax: 0.25}, after.HitRate - before.HitRate, evolution.DiagnoseRetrieval(before, after), decision, active.Config()}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fail(err)
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
