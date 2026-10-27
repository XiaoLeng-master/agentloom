package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteTempCorpusPrintsGate(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(source, []byte(strings.Repeat("needle token is the ground fact. ", 12)), 0600); err != nil {
		t.Fatal(err)
	}
	dataset := filepath.Join(dir, "cases.json")
	body, _ := json.Marshal([]map[string]string{{
		"id": "case-1", "type": "qa", "question": "needle token", "groundTruth": "needle token", "sourceDoc": source,
	}})
	if err := os.WriteFile(dataset, body, 0600); err != nil {
		t.Fatal(err)
	}
	text, err := execute(dataset, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"评测—归因—优化—复测门禁报告", "失败样本", "门禁", "词法代理 deterministic_proxy"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s\n%s", want, text)
		}
	}
	raw, err := execute(dataset, true)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Cases int `json:"cases"`
	}
	if err := json.Unmarshal([]byte(raw), &report); err != nil || report.Cases != 1 {
		t.Fatalf("json cases=%d err=%v", report.Cases, err)
	}
}
