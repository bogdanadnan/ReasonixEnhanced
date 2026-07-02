package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// reportTool lets orchestrator agents report their status back to the
// orchestrator as a structured tool call instead of raw JSON text.
// Each agent type (planner, developer, reviewer) registers the tool with
// a schema matching its expected response format.
type reportTool struct {
	name        string
	description string
	schema      json.RawMessage
	result      chan json.RawMessage // buffered channel; the orchestrator reads from it
	called      bool                 // true after first Execute call
}

func newReportTool(name, desc string, schema json.RawMessage) *reportTool {
	return &reportTool{
		name:        name,
		description: desc,
		schema:      schema,
		result:      make(chan json.RawMessage, 1),
	}
}

func (t *reportTool) Name() string        { return t.name }
func (t *reportTool) ReadOnly() bool      { return true }
func (t *reportTool) Description() string { return t.description }
func (t *reportTool) Schema() json.RawMessage {
	return t.schema
}

func (t *reportTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	if t.called {
		return "ERROR: You already called this tool. STOP immediately — do not call any tool again. Respond with only 'done' as text.", nil
	}
	t.called = true
	select {
	case t.result <- args:
	default:
		select {
		case <-t.result:
		default:
		}
		t.result <- args
	}
	return "Report received. Your work is complete. Respond with ONLY the word 'done' as text now.", nil
}

func (t *reportTool) Wait() (json.RawMessage, error) {
	if !t.called {
		return nil, fmt.Errorf("%s: agent did not call the report tool", t.name)
	}
	select {
	case r := <-t.result:
		return r, nil
	default:
		return nil, fmt.Errorf("%s: no result in channel", t.name)
	}
}

// Planner return value
type plannerReport struct {
	PhaseCount int `json:"phase_count"`
	TaskCount  int `json:"task_count"`
}

// Developer return value
type developerReport struct {
	Status    string `json:"status"`
	Summary   string `json:"summary"`
	Rationale string `json:"rationale"`
}

// Reviewer return value
type reviewerReport struct {
	Status  string `json:"status"`
	Issues  int    `json:"issues,omitempty"`
	Summary string `json:"summary"`
}
