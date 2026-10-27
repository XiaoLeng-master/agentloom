// Command loop runs the offline evaluation, attribution, optimization, and retest gate.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/XiaoLeng-master/agentloom/internal/config"
	"github.com/XiaoLeng-master/agentloom/internal/evolution"
)

func main() {
	dataset := flag.String("dataset", evolution.PublicDatasetPath, "fixed evaluation set")
	asJSON := flag.Bool("json", false, "print the JSON report instead of Markdown")
	flag.Parse()
	text, err := execute(*dataset, *asJSON)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(text)
}

func execute(dataset string, asJSON bool) (string, error) {
	cases, docs, hash, err := evolution.LoadPublicCorpus(dataset)
	if err != nil {
		return "", err
	}
	if dataset == evolution.PublicDatasetPath && len(cases) != evolution.PublicDatasetSize {
		return "", fmt.Errorf("public dataset must contain %d cases, got %d", evolution.PublicDatasetSize, len(cases))
	}
	manager := config.NewManager()
	baseline := evolution.BaselinePolicy()
	manager.Register("kb-agent", config.Config{
		Name:          "kb-agent",
		SystemPrompt:  "Retrieval baseline",
		ChunkStrategy: baseline.Splitter.Name(),
	})
	report, err := evolution.RunLoop(evolution.LoopOptions{
		DatasetPath: dataset,
		Cases:       cases,
		Docs:        docs,
		DatasetHash: hash,
		Baseline:    baseline,
		Manager:     manager,
		AgentName:   "kb-agent",
	})
	if err != nil {
		return "", err
	}
	if asJSON {
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return "", err
		}
		return string(body) + "\n", nil
	}
	return report.Markdown(), nil
}
