// Command cachebench prints embed and LLM call counts for one fixed batch.
// It uses HashEmbedder and an in-process counter. It does not call a model API.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/XiaoLeng-master/agentloom/internal/app"
)

func main() {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(app.MeasureCacheSavings()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
