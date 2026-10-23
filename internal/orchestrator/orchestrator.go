// Package orchestrator implements the multi-Agent pool choreography the
// resume describes as "Planner/Executor/Verifier 能力池分离" — the layer that
// turns one user turn into a planned → executed → verified collaboration
// across role-separated Agent pools.
//
// It is deliberately a thin layer on top of internal/core: every pool member
// is a *core.Agent (the ReAct loop), and the orchestrator only owns (1) the
// routing (round-robin within a role for horizontal scale), (2) the message
// contract that flows between roles, and (3) the re-plan loop when a Verifier
// rejects an executor's output. The trace it returns is the surface the eval
// harness consumes to replay a turn.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/XiaoLeng-master/agentloom/internal/core"
)

// Role is the capability bucket an Agent plays in a collaboration. The three
// roles map 1:1 to the resume taxonomy: planner (expensive, few), executor
// (cheap, scaled out), verifier (rules + small model).
type Role string

const (
	RolePlanner  Role = "planner"
	RoleExecutor Role = "executor"
	RoleVerifier Role = "verifier"
)

// Phase marks which leg of the loop a Contract belongs to. The trace is a
// flat []Contract ordered by emission time; the eval harness replays it.
type Phase string

const (
	PhasePlan    Phase = "plan"
	PhaseExecute Phase = "execute"
	PhaseVerify  Phase = "verify"
)

// Contract is the message that flows between roles. Agents NEVER share
// internal state — they communicate only via Contract. This is the literal
// "消息契约交接" property the resume advertises.
type Contract struct {
	From     Role   `json:"from"`
	To       Role   `json:"to"`
	TaskID   string `json:"task_id"`
	ParentID string `json:"parent_id,omitempty"`
	Phase    Phase  `json:"phase"`
	Payload  string `json:"payload"`
}

// SubTask is a planner output unit — one executable chunk handed to an
// executor. The planner emits a []SubTask as JSON in its Run payload.
type SubTask struct {
	ID    string `json:"id"`
	Input string `json:"input"`
}

// ExecResult wraps an executor's output for the verifier.
type ExecResult struct {
	TaskID string `json:"task_id"`
	Output string `json:"output"`
}

// Verdict is the verifier's verdict on an ExecResult. Pass=false triggers a
// re-plan (up to Orchestrator.maxReplan).
type Verdict struct {
	TaskID string `json:"task_id"`
	Pass   bool   `json:"pass"`
	Reason string `json:"reason,omitempty"`
}

// Pool is one role's Agent set. pick() round-robins across members so the
// cheap executor role can scale horizontally by adding agents; the expensive
// planner role typically has one or two.
type Pool struct {
	mu     sync.RWMutex
	role   Role
	agents []*core.Agent
	next   uint32
}

func newPool(role Role) *Pool { return &Pool{role: role} }

func (p *Pool) add(a *core.Agent) { p.mu.Lock(); defer p.mu.Unlock(); p.agents = append(p.agents, a) }

// pick returns the next Agent by round-robin. Returns nil if the pool is
// empty — callers must handle that (e.g. a missing verifier is non-fatal).
func (p *Pool) pick() *core.Agent {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := uint32(len(p.agents))
	if n == 0 {
		return nil
	}
	i := atomic.AddUint32(&p.next, 1) % n
	// AddUint32 starts at 1; subtract for a 0-based index. The modulo keeps it
	// in range even under concurrent picks.
	return p.agents[(i+n-1)%n]
}

func (p *Pool) size() int { p.mu.RLock(); defer p.mu.RUnlock(); return len(p.agents) }

// Orchestrator owns the three role pools and runs one collaboration turn.
type Orchestrator struct {
	mu        sync.Mutex
	pools     map[Role]*Pool
	maxReplan int
}

// NewOrchestrator returns an empty Orchestrator. maxReplan caps how many
// times a failed verify leg can re-enter planning (<=0 defaults to 2).
func NewOrchestrator(maxReplan int) *Orchestrator {
	if maxReplan <= 0 {
		maxReplan = 2
	}
	return &Orchestrator{pools: make(map[Role]*Pool), maxReplan: maxReplan}
}

// AddAgent adds a to role's pool. Safe to call concurrently with Run.
func (o *Orchestrator) AddAgent(role Role, a *core.Agent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.pools[role]
	if !ok {
		p = newPool(role)
		o.pools[role] = p
	}
	p.add(a)
}

// PoolSize returns the member count for role (0 if the pool is absent).
func (o *Orchestrator) PoolSize(role Role) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	if p, ok := o.pools[role]; ok {
		return p.size()
	}
	return 0
}

// pool returns the Pool for role under the lock. Callers already holding the
// lock use this; public methods go through it too for consistency.
func (o *Orchestrator) pool(role Role) *Pool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.pools[role]
}

// pickRole returns the next Agent for role by round-robin, or nil if the
// pool is absent. It is the nil-safe entry the Run loop uses so a missing
// verifier pool degrades instead of crashing.
func (o *Orchestrator) pickRole(role Role) *core.Agent {
	p := o.pool(role)
	if p == nil {
		return nil
	}
	return p.pick()
}

// Run executes one collaboration turn and returns the final result plus the
// full Contract trace (for the eval harness to replay).
//
// Flow:
//  1. Planner.Run(userQuery) → JSON []SubTask (or a single fallback task)
//  2. For each SubTask, Executor.Run(input) → ExecResult
//  3. Verifier.Run(ExecResult) → Verdict (skipped if no verifier mounted)
//  4. If any Verdict.Pass is false and replans remain, Planner.Run(feedback)
//     re-plans; otherwise the turn ends with whatever passed.
func (o *Orchestrator) Run(ctx context.Context, userQuery string) (string, []Contract, error) {
	var trace []Contract
	subtasks, planPayload, err := o.plan(ctx, o.pickRole(RolePlanner), userQuery)
	if err != nil {
		return "", trace, fmt.Errorf("planner: %w", err)
	}
	trace = append(trace, Contract{From: RolePlanner, To: RoleExecutor, TaskID: "root", Phase: PhasePlan, Payload: planPayload})

	results := make(map[string]string, len(subtasks))
	for replan := 0; replan <= o.maxReplan; replan++ {
		allPass := true
		var verdicts []Verdict
		for _, st := range subtasks {
			exec := o.pickRole(RoleExecutor)
			if exec == nil {
				return "", trace, ErrNoExecutor
			}
			out, err := exec.Run(ctx, st.Input)
			if err != nil {
				return "", trace, fmt.Errorf("executor %s: %w", st.ID, err)
			}
			results[st.ID] = out
			trace = append(trace, Contract{From: RoleExecutor, To: RoleVerifier, TaskID: st.ID, Phase: PhaseExecute, Payload: out})

			ver := o.pickRole(RoleVerifier)
			if ver == nil {
				continue // missing verifier is non-fatal
			}
			verIn, _ := json.Marshal(ExecResult{TaskID: st.ID, Output: out})
			verOut, err := ver.Run(ctx, string(verIn))
			if err != nil {
				trace = append(trace, Contract{From: RoleVerifier, To: RolePlanner, TaskID: st.ID, Phase: PhaseVerify, Payload: err.Error()})
				return "", trace, fmt.Errorf("%w: task %s: %w", ErrVerification, st.ID, err)
			}
			trace = append(trace, Contract{From: RoleVerifier, To: RolePlanner, TaskID: st.ID, Phase: PhaseVerify, Payload: verOut})
			var decoded struct {
				TaskID string `json:"task_id"`
				Pass   *bool  `json:"pass"`
				Reason string `json:"reason"`
			}
			if e := json.Unmarshal([]byte(verOut), &decoded); e != nil || decoded.Pass == nil || decoded.TaskID != st.ID {
				return "", trace, fmt.Errorf("%w: task %s: invalid verdict", ErrVerification, st.ID)
			}
			vd := Verdict{TaskID: decoded.TaskID, Pass: *decoded.Pass, Reason: decoded.Reason}
			verdicts = append(verdicts, vd)
			if !vd.Pass {
				allPass = false
			}
		}
		if allPass {
			break
		}
		if replan >= o.maxReplan {
			return "", trace, ErrReplanExhausted
		}
		// Re-plan with the feedback so the planner does not repeat the same split.
		fb, _ := json.Marshal(map[string]any{"original": userQuery, "results": results, "verdicts": verdicts})
		newSubs, newPayload, err := o.plan(ctx, o.pickRole(RolePlanner), string(fb))
		if err != nil {
			return "", trace, fmt.Errorf("replanner: %w", err)
		}
		subtasks = newSubs
		trace = append(trace, Contract{From: RolePlanner, To: RoleExecutor, TaskID: "replan", Phase: PhasePlan, Payload: newPayload})
	}

	return summarize(subtasks, results), trace, nil
}

// plan runs the planner and parses its JSON output into subtasks. If the
// model did not emit a []SubTask (a small model might just answer directly),
// the planner's raw output becomes a single fallback task — this is the
// "degraded-not-crashed" property the core ReAct loop also uses.
func (o *Orchestrator) plan(ctx context.Context, planner *core.Agent, input string) ([]SubTask, string, error) {
	if planner == nil {
		return nil, "", ErrNoPlanner
	}
	out, err := planner.Run(ctx, input)
	if err != nil {
		return nil, "", err
	}
	var subs []SubTask
	if e := json.Unmarshal([]byte(out), &subs); e != nil || len(subs) == 0 {
		return []SubTask{{ID: "t0", Input: input}}, out, nil
	}
	return subs, out, nil
}

func summarize(subs []SubTask, results map[string]string) string {
	out := ""
	for _, st := range subs {
		if r, ok := results[st.ID]; ok {
			if out != "" {
				out += "\n"
			}
			out += r
		}
	}
	return out
}

// Errors.
var (
	ErrNoPlanner       = errors.New("orchestrator: planner pool empty")
	ErrNoExecutor      = errors.New("orchestrator: executor pool empty")
	ErrVerification    = errors.New("orchestrator: verification failed")
	ErrReplanExhausted = errors.New("orchestrator: replan budget exhausted")
)
