// Command evolve runs the task-driven offline loop on the fixed 98-case set.
package main

import (
	"fmt"
	"os"

	"github.com/XiaoLeng-master/agentloom/internal/config"
	"github.com/XiaoLeng-master/agentloom/internal/evolution"
)

func main() {
	cases, docs, hash, err := evolution.LoadPublicCorpus(evolution.PublicDatasetPath)
	if err != nil {
		fail(err)
	}
	if len(cases) != evolution.PublicDatasetSize {
		fail(fmt.Errorf("public dataset must contain %d cases, got %d", evolution.PublicDatasetSize, len(cases)))
	}
	manager := config.NewManager()
	baseline := evolution.BaselinePolicy()
	manager.Register("kb-agent", config.Config{Name: "kb-agent", SystemPrompt: "Retrieval baseline", ChunkStrategy: baseline.Splitter.Name()})
	report, err := evolution.RunLoop(evolution.LoopOptions{
		DatasetPath: evolution.PublicDatasetPath,
		Cases:       cases,
		Docs:        docs,
		DatasetHash: hash,
		Baseline:    baseline,
		Manager:     manager,
		AgentName:   "kb-agent",
	})
	if err != nil {
		fail(err)
	}
	fmt.Print(report.Markdown())
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
