package eval

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeepSeekJudgeUsesLogprobsDistribution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer judge-key" {
			t.Error("missing judge authorization")
		}
		var body struct {
			Logprobs    bool              `json:"logprobs"`
			TopLogprobs int               `json:"top_logprobs"`
			Thinking    map[string]string `json:"thinking"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !body.Logprobs || body.TopLogprobs != 20 || body.Thinking["type"] != "disabled" {
			t.Fatalf("judge request=%+v", body)
		}
		_, _ = w.Write([]byte(`{"choices":[{"logprobs":{"content":[{"token":"4","logprob":-0.1,"top_logprobs":[{"token":" 5","logprob":-0.2},{"token":"4","logprob":-0.1},{"token":"3","logprob":-2.0}]}]}}]}`))
	}))
	defer server.Close()
	judge := DeepSeekJudge{APIKey: "judge-key", Endpoint: server.URL}
	score := judge.Eval(Trace{Query: "q", Answer: "a", Expected: Expected{Answer: "a"}})
	if !score.Pass || score.Name != "deepseek_geval" || score.Value <= 0.7 || score.Value > 1 {
		t.Fatalf("score=%+v", score)
	}
}

func TestDeepSeekJudgeFailsClosedWithoutRatingDistribution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"logprobs":{"content":[{"token":"good","logprob":-0.1,"top_logprobs":[]}]}}]}`))
	}))
	defer server.Close()
	score := (DeepSeekJudge{APIKey: "judge-key", Endpoint: server.URL}).Eval(Trace{})
	if score.Pass || score.Reason != "judge unavailable: rating token missing" {
		t.Fatalf("score=%+v", score)
	}
}
