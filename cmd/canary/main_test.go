package main

import (
	"strings"
	"testing"
)

func TestExecutePrintsRampAndRollback(t *testing.T) {
	dir := t.TempDir()
	text, err := execute(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"灰度→放量",
		"灰度→回滚",
		"不是 datasets/rag_eval_98.json 的测量",
		"也不是在线流量",
		"稳定版本 1",
		"灰度版本 2",
		"ramp 50%",
		"ramp 100%",
		"rollback 0%",
		"在途请求在回滚前拿到灰度配置",
		"实验记录 0 条，灰度事件 5 条",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s\n%s", want, text)
		}
	}
}
