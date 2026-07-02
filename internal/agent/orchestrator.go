package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"reasonix/internal/provider"
	"reasonix/internal/tool"

	"log/slog"
)

// Orchestrator coordinates a planner → developer → reviewer workflow across
// three agent models. The planner produces a plan file, feeds tasks one at a
// time to the developer, and the reviewer gates each result. Models communicate
// via files (plan.md, workload_brief.md, review_N.md) plus short JSON status
// responses the orchestrator parses.
//
// Lifecycle: Orchestrator.Run(ctx, userInput) drives the full cycle within one
// user turn. Progress is persisted to state.json after each step so a crashed
// session can resume.
type Orchestrator struct {
	planner        *Agent
	developer      *Agent
	reviewer       *Agent
	reviewer2      *Agent // optional second reviewer

	orchDir    string
	maxRetries int
	autoCommit bool

	state *OrchState
	mu    sync.Mutex
}

// OrchState is the orchestrator's progress, persisted to state.json.
type OrchState struct {
	Phase          int         `json:"phase"`
	Task           int         `json:"task"`
	Phases         []OrchPhase `json:"phases"`
	Retries        int         `json:"retries"`
	Status         string      `json:"status"`
	PlannerLabel   string      `json:"plannerLabel"`
	DeveloperLabel string      `json:"developerLabel"`
	ReviewerLabel  string      `json:"reviewerLabel"`
	Reviewer2Label string      `json:"reviewer2Label"`
}

// OrchPhase is one phase of the plan.
type OrchPhase struct {
	Name  string   `json:"name"`
	Tasks []string `json:"tasks"`
	Done  []string `json:"done"`
}

// OrchestratorOptions configures a new Orchestrator.
type OrchestratorOptions struct {
	Planner    *Agent
	Developer  *Agent
	Reviewer   *Agent
	Reviewer2  *Agent // optional second reviewer (nil = disabled)
	OrchDir    string
	MaxRetries int
	AutoCommit bool
}

// NewOrchestrator creates an orchestrator. Callers must ensure all three agents
// are fully constructed with their own sessions, tools, and sinks.
func NewOrchestrator(opts OrchestratorOptions) *Orchestrator {
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = 3
	}
	// Ensure the orchDir is absolute so agents using write_file can reach it.
	orchDir := opts.OrchDir
	if !filepath.IsAbs(orchDir) {
		if abs, err := filepath.Abs(orchDir); err == nil {
			orchDir = abs
		}
	}
	return &Orchestrator{
		planner:    opts.Planner,
		developer:  opts.Developer,
		reviewer:   opts.Reviewer,
		reviewer2:  opts.Reviewer2,
		orchDir:    orchDir,
		maxRetries: opts.MaxRetries,
		autoCommit: opts.AutoCommit,
	}
}

// Run drives the full orchestration cycle. It blocks until all phases complete
// or the context is cancelled. If a prior run was interrupted (paused/cancelled),
// it resumes from state.json instead of re-planning.
func (o *Orchestrator) Run(ctx context.Context, userInput string) error {
	slog.Info("orchestrator: starting", "orchDir", o.orchDir)
	if err := os.MkdirAll(o.orchDir, 0o755); err != nil {
		return fmt.Errorf("orchestrator: create orch dir: %w", err)
	}

	// Try to resume from a prior interrupted run
	if state, err := loadState(o.statePath()); err == nil && state.Status != "done" && len(state.Phases) > 0 {
		slog.Info("orchestrator: resuming from state.json", "phase", state.Phase, "task", state.Task)
		// Restore labels if they were empty (state.json from older version)
		if state.PlannerLabel == "" {
			state.PlannerLabel = o.plannerLabel()
		}
		if state.DeveloperLabel == "" {
			state.DeveloperLabel = o.developerLabel()
		}
		if state.ReviewerLabel == "" {
			state.ReviewerLabel = o.reviewerLabel()
		}
		if state.Reviewer2Label == "" && o.reviewer2 != nil {
			state.Reviewer2Label = o.reviewer2Label()
		}
		// Refresh phases from plan.md in case the parser was updated (empty phases)
		if freshPhases, err := parsePlan(o.planPath()); err == nil && len(freshPhases) > 0 {
			state.Phases = freshPhases
		}
		o.mu.Lock()
		o.state = state
		o.mu.Unlock()
		// Continue the task loop — user input becomes a steer message
		if userInput != "" {
			o.developer.Session().Add(provider.Message{Role: provider.RoleUser, Content: "[User steer] " + userInput})
		}
		for !o.isDone() {
			if err := o.runTaskCycle(ctx); err != nil {
				return fmt.Errorf("orchestrator: task cycle: %w", err)
			}
		}
		return nil
	}

	// Fresh run: planning phase
	if err := o.runPlanning(ctx, userInput); err != nil {
		return fmt.Errorf("orchestrator: planning phase: %w", err)
	}

	// Development loop
	for !o.isDone() {
		if err := o.runTaskCycle(ctx); err != nil {
			return fmt.Errorf("orchestrator: task cycle: %w", err)
		}
	}

	slog.Info("orchestrator: done")
	return nil
}

func (o *Orchestrator) isDone() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state == nil {
		return false
	}
	return o.state.Status == "done"
}

func (o *Orchestrator) statePath() string {
	return filepath.Join(o.orchDir, "state.json")
}

func (o *Orchestrator) planPath() string {
	return filepath.Join(o.orchDir, "plan.md")
}

func (o *Orchestrator) briefPath() string {
	return filepath.Join(o.orchDir, "workload_brief.md")
}

func (o *Orchestrator) donePath() string {
	return filepath.Join(o.orchDir, "workload_done.md")
}

func (o *Orchestrator) rationalePath() string {
	return filepath.Join(o.orchDir, "workload_rationale.md")
}

func (o *Orchestrator) reviewPath(n int) string {
	return filepath.Join(o.orchDir, fmt.Sprintf("review_%d.md", n))
}

func (o *Orchestrator) reviewPath2(n int) string {
	return filepath.Join(o.orchDir, fmt.Sprintf("review_%d_b.md", n))
}

// saveState persists the current state to state.json.
// Caller must hold o.mu if the state may be modified concurrently.
func (o *Orchestrator) saveState() error {
	if o.state == nil {
		return nil
	}
	b, err := json.MarshalIndent(o.state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(o.statePath(), b, 0o644)
}

// saveStateLocked is saveState with lock acquisition for callers that
// don't already hold o.mu.
func (o *Orchestrator) saveStateLocked() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.saveState()
}

// State returns a copy of the current orchestrator state (thread-safe).
func (o *Orchestrator) State() *OrchState {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state == nil {
		return nil
	}
	cp := *o.state
	return &cp
}

// SetDeveloper replaces the developer agent (called when the user switches models).
func (o *Orchestrator) SetDeveloper(a *Agent) {
	o.developer = a
}

// OrchDir returns the orchestrator working directory.
func (o *Orchestrator) OrchDir() string { return o.orchDir }

// ReviewPath returns the path for review_N.md.
func (o *Orchestrator) ReviewPath(n int) string { return o.reviewPath(n) }

func (o *Orchestrator) plannerLabel() string {
	if o.planner != nil {
		return o.planner.prov.Name()
	}
	return ""
}
func (o *Orchestrator) developerLabel() string {
	if o.developer != nil {
		return o.developer.prov.Name()
	}
	return ""
}
func (o *Orchestrator) reviewerLabel() string {
	if o.reviewer != nil {
		return o.reviewer.prov.Name()
	}
	return ""
}
func (o *Orchestrator) reviewer2Label() string {
	if o.reviewer2 != nil {
		return o.reviewer2.prov.Name()
	}
	return ""
}

// Stubs — to be implemented in later phases.

func (o *Orchestrator) runPlanning(ctx context.Context, userInput string) error {
	slog.Info("orchestrator: planning phase")
	prompt := fmt.Sprintf(`You are the Planner in a developer orchestrator. Given the user's request,
create a detailed implementation plan. First explore the codebase with
read_file, glob, and grep to understand the existing structure, then write
the plan to %s using ## for phases and - [ ] for individual tasks.
Each task should be concrete and implementable in one developer session.

When you're done, call the report_plan tool with your results. Do NOT respond
with text — use ONLY the tool.`, o.planPath(), userInput)

	planTool := newReportTool("report_plan",
		"Report planning results back to the orchestrator. Call this when your plan is complete.",
		json.RawMessage(`{"type":"object","properties":{"phase_count":{"type":"integer"},"task_count":{"type":"integer"}},"required":["phase_count","task_count"]}`))
	o.planner.tools.Add(planTool)
	defer o.planner.tools.Remove("report_plan")

	if err := o.planner.Run(ctx, prompt); err != nil {
		return fmt.Errorf("planner: %w", err)
	}

	raw, planErr := planTool.Wait()
	if planErr != nil {
		// Fallback: try to parse from text if the agent didn't use the tool
		text := o.lastAssistantText(o.planner)
		if text != "" {
			raw = json.RawMessage(text)
		} else {
			return fmt.Errorf("planner: %w", planErr)
		}
	}
	var planResult plannerReport
	if err := json.Unmarshal(raw, &planResult); err != nil {
		return fmt.Errorf("planner report: %w", err)
	}

	// Verify the plan file was actually written. If not, nudge the planner.
	if _, err := os.Stat(o.planPath()); err != nil {
		nudge := fmt.Sprintf("You did not write the plan file to %s. Please write_file to that path with the plan in the specified format (## Phase headings, - [ ] tasks). Then respond with the JSON summary again.", o.planPath())
		o.planner.Session().Add(provider.Message{Role: provider.RoleUser, Content: nudge})
		if err := o.requestStructured(ctx, o.planner, "Write the plan file now.", &planResult,
			`{"phase_count": N, "task_count": M}`, 2); err != nil {
			return fmt.Errorf("planner file check: %w", err)
		}
	}

	// Parse the plan file
	phases, err := parsePlan(o.planPath())
	if err != nil {
		return fmt.Errorf("parse plan: %w", err)
	}

	slog.Info("orchestrator: plan parsed", "phases", len(phases), "tasks", planResult.TaskCount)

	o.mu.Lock()
	o.state = &OrchState{
		Phase: 1, Task: 1, Phases: phases, Status: "developing",
		PlannerLabel:   o.plannerLabel(),
		DeveloperLabel: o.developerLabel(),
		ReviewerLabel:  o.reviewerLabel(),
		Reviewer2Label: o.reviewer2Label(),
	}
	o.mu.Unlock()
	o.mu.Lock()
	err = o.saveState()
	o.mu.Unlock()
	return err
}

func (o *Orchestrator) runTaskCycle(ctx context.Context) error {
	o.mu.Lock()
	if o.state == nil || len(o.state.Phases) == 0 {
		o.mu.Unlock()
		return fmt.Errorf("no plan loaded")
	}
	state := *o.state // shallow copy for this cycle
	o.mu.Unlock()

	// Find current task
	if state.Phase < 1 || state.Phase > len(state.Phases) {
		return fmt.Errorf("phase index %d out of range", state.Phase)
	}
	phase := state.Phases[state.Phase-1]
	if state.Task < 1 || state.Task > len(phase.Tasks) || len(phase.Tasks) == 0 {
		// Empty phase (free-text section) or past-end — skip to next
		slog.Info("orchestrator: skipping empty/ended phase", "phase", state.Phase, "name", phase.Name, "tasks", len(phase.Tasks))
		o.mu.Lock()
		o.state.Phase++
		o.state.Task = 1
		if o.state.Phase > len(o.state.Phases) {
			o.state.Status = "done"
		}
		o.mu.Unlock()
		o.saveStateLocked()
		return nil
	}
	taskName := phase.Tasks[state.Task-1]
	if o.isTaskDone(phase, taskName) {
		return o.advanceTask(ctx)
	}

	// --- DEVELOPING ---
	slog.Info("orchestrator: developing", "phase", state.Phase, "task", state.Task, "name", taskName)
	state.Status = "developing"
	o.mu.Lock()
	o.state = &state
	o.mu.Unlock()
	o.saveStateLocked()

	brief := fmt.Sprintf("Implement this task from the plan:\n\n**%s**\n\nPhase: %s\nTask %d of %d",
		taskName, phase.Name, state.Task, len(phase.Tasks))
	if err := writeBrief(o.briefPath(), brief); err != nil {
		return err
	}

	commitInstr := ""
	if o.autoCommit {
		commitInstr = "\n\nBefore you finish: if this is a git repository, commit your changes with `git add -A && git commit -m \"[orchestrator] " + taskName + "\"`. The reviewer needs a clean baseline to diff against."
	}
	devPrompt := fmt.Sprintf("You are the Developer. Read the workload brief at %s and implement it.\nRead existing code with read_file before editing. Run build/tests with bash after changes.\n\nIf there is a review history, address any issues from the latest review_N.md.\nIf you cannot or choose not to implement any aspect, document it with rationale in %s.%s\n\nWhen done, write a completion summary to %s and a rationale file at %s explaining any skipped items, deliberate deviations, or design choices the reviewer should know about. Then call the report_work tool. Do NOT respond with text — use ONLY the tool.",
		o.briefPath(), o.rationalePath(), commitInstr, o.donePath(), o.rationalePath())

	devTool := newReportTool("report_work",
		"Report work completion back to the orchestrator. Call this when you're done.",
		json.RawMessage(`{"type":"object","properties":{"status":{"type":"string"},"summary":{"type":"string"},"rationale":{"type":"string"}},"required":["status","summary"]}`))
	o.developer.tools.Add(devTool)
	defer o.developer.tools.Remove("report_work")

	if err := o.developer.Run(ctx, devPrompt); err != nil {
		return fmt.Errorf("developer: %w", err)
	}

	raw, devErr := devTool.Wait()
	if devErr != nil {
		text := o.lastAssistantText(o.developer)
		if text != "" {
			raw = json.RawMessage(text)
		}
	}
	// Parse just to verify; developer output is secondary

	// --- REVIEWING ---
	state.Status = "reviewing"
	o.mu.Lock()
	o.state = &state
	o.mu.Unlock()
	o.saveStateLocked()

	reviewPath := o.reviewPath(state.Task)
	reviewDiffInstr := "if this is a git repo, use `git diff` to see what changed"
	if o.autoCommit {
		reviewDiffInstr = "if this is a git repo, use `git log -1 -p` to see committed changes"
	}
	reviewPrompt := fmt.Sprintf(`You are the Reviewer. Review ONLY the deliverables the developer produced
against the workload brief (%s). The developer may have produced code,
documentation, configuration, or other artifacts — judge what was delivered,
not the format.

%s. Also inspect the changed files directly with read_file.

Read the workload brief at %s — this is THE deliverable spec.

IMPORTANT: You are a READ-ONLY reviewer. Do NOT commit, push, or write code.
Use pseudocode or step-by-step instructions to illustrate alternatives.

Write your review to %s with this format:
## Verdict: PASS or FAIL
## Summary
Brief summary of your assessment.
## Issues (if FAIL)
1. Issue description
2. Issue description

After writing the review file, call the report_review tool. Do NOT respond
with text — use ONLY the tool.`,
		o.briefPath(), reviewDiffInstr, o.briefPath(), reviewPath)

	var verdict reviewerReport
	reviewTool := newReportTool("report_review",
		"Report your review verdict to the orchestrator. Call this when your review is complete.",
		json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["pass","fail"]},"issues":{"type":"integer"},"summary":{"type":"string"}},"required":["status","summary"]}`))
	o.reviewer.tools.Add(reviewTool)
	defer o.reviewer.tools.Remove("report_review")

	if err := o.reviewer.Run(ctx, reviewPrompt); err != nil {
		return fmt.Errorf("reviewer: %w", err)
	}

	// Parse reviewer verdict from tool call
	raw, waitErr := reviewTool.Wait()
	if waitErr != nil {
		text := o.lastAssistantText(o.reviewer)
		if text != "" && o.parseStructured(text, &verdict) == nil {
			// Fallback succeeded
		} else {
			return fmt.Errorf("reviewer: %w", waitErr)
		}
	} else {
		if err := json.Unmarshal(raw, &verdict); err != nil {
			return fmt.Errorf("reviewer report: %w", err)
		}
	}

	// --- SECOND REVIEWER (if configured) ---
	// Always run if configured, regardless of first reviewer's verdict.
	// The developer gets the union of all issues from all reviewers.
	combinedPass := verdict.Status == "pass"
	if o.reviewer2 != nil {
		state.Status = "reviewing2"
		o.mu.Lock()
		o.state = &state
		o.mu.Unlock()
		o.saveStateLocked()

		review2Path := o.reviewPath2(state.Task)
		review2Prompt := fmt.Sprintf(`You are the Second Reviewer. Review ONLY the deliverables the developer
produced against the workload brief. The first reviewer's review is at %s
(verdict: %s).

Read:
  1. The workload brief at %s — this is THE deliverable spec
  2. The first reviewer's review at %s — examine for gaps but don't re-litigate
  3. The actual changes — use read_file and (if git) git diff

Do NOT read workload_done.md or workload_rationale.md — they are metadata,
not deliverables.

If you find issues the first reviewer missed, or disagree with their
assessment, document why. Do NOT repeat issues unless you have
additional context.

IMPORTANT: You are READ-ONLY. Never edit or write files.

Write your review to %s with this format:
## Verdict: PASS or FAIL
## Summary
Brief summary. If you disagree with the first reviewer, explain why.
## Issues (if FAIL)
1. Issue (prefix [MISSED] if first reviewer didn't flag)
2. Issue

DO NOT mention workload_done.md, workload_rationale.md, or any metadata.

After writing the review file, call the report_review tool. Do NOT respond
with text — use ONLY the tool.`,
			reviewPath, verdict.Status, o.briefPath(), reviewPath, review2Path,
		)

		// Register the report tool BEFORE the run so reviewer2 can call it
		rev2Tool := newReportTool("report_review",
			"Report your review verdict to the orchestrator.",
			json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["pass","fail"]},"issues":{"type":"integer"},"summary":{"type":"string"}},"required":["status","summary"]}`))
		o.reviewer2.tools.Add(rev2Tool)
		defer o.reviewer2.tools.Remove("report_review")

		if err := o.reviewer2.Run(ctx, review2Prompt); err != nil {
			return fmt.Errorf("second reviewer: %w", err)
		}

		raw2, waitErr2 := rev2Tool.Wait()
		var verdict2 reviewerReport
		if waitErr2 != nil {
			if o.parseStructured(o.lastAssistantText(o.reviewer2), &verdict2) != nil {
				return fmt.Errorf("second reviewer: %w", waitErr2)
			}
		} else if err := json.Unmarshal(raw2, &verdict2); err != nil {
			return fmt.Errorf("second reviewer report: %w", err)
		}

		if verdict2.Status != "pass" {
			combinedPass = false
		}
	}

	if combinedPass {
		slog.Info("orchestrator: all reviewers pass", "task", taskName)
		return o.advanceTask(ctx)
	}

	// Review fail — retry if budget remains
	state.Retries++
	slog.Info("orchestrator: review fail", "task", taskName, "retries", state.Retries, "issues", verdict.Issues)
	if state.Retries <= o.maxRetries {
		o.mu.Lock()
		o.state = &state
		o.mu.Unlock()
		o.saveStateLocked()
		return nil // loop back to developing (caller re-enters runTaskCycle)
	}

	// Retries exhausted — skip task
	slog.Warn("orchestrator: retries exhausted, skipping task", "task", taskName)
	return o.advanceTask(ctx)
}

func (o *Orchestrator) advanceTask(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state == nil {
		return nil
	}
	phase := &o.state.Phases[o.state.Phase-1]
	taskName := phase.Tasks[o.state.Task-1]
	phase.Done = append(phase.Done, taskName)
	o.state.Task++
	o.state.Retries = 0

	if o.state.Task > len(phase.Tasks) {
		// Phase complete — move to next phase
		o.state.Phase++
		if o.state.Phase > len(o.state.Phases) {
			o.state.Status = "done"
			slog.Info("orchestrator: all phases complete")
		} else {
			o.state.Task = 1
			slog.Info("orchestrator: phase complete", "phase", o.state.Phase-1)
		}
	}
	return o.saveState()
}

// --- helpers ---

func (o *Orchestrator) isTaskDone(phase OrchPhase, task string) bool {
	for _, d := range phase.Done {
		if d == task {
			return true
		}
	}
	return false
}

func (o *Orchestrator) lastAssistantText(agent *Agent) string {
	msgs := agent.Session().Snapshot()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == provider.RoleAssistant && strings.TrimSpace(msgs[i].Content) != "" {
			return strings.TrimSpace(msgs[i].Content)
		}
	}
	return ""
}

// requestStructured runs the agent with the given prompt, then parses the
// final assistant message as JSON into target. If parsing fails, it injects a
// corrective nudge into the agent's session and retries up to maxNudges times.
func (o *Orchestrator) requestStructured(ctx context.Context, agent *Agent, prompt string, target interface{}, schemaDesc string, maxNudges int) error {
	for attempt := 0; attempt <= maxNudges; attempt++ {
		if attempt > 0 {
			// Inject a corrective nudge with the parse error and expected format
			nudge := fmt.Sprintf(
				"Your previous response was not valid JSON for: %s.\n\n"+
					"Please respond with ONLY a valid JSON object, no markdown fences, "+
					"no surrounding text. Example: {\"key\": \"value\"}\n\n"+
					"Expected format: %s\n\n"+
					"Try again.",
				schemaDesc, schemaDesc,
			)
			agent.Session().Add(provider.Message{Role: provider.RoleUser, Content: nudge})
			slog.Info("orchestrator: nudging agent for structured response", "attempt", attempt)
		}

		if err := agent.Run(ctx, prompt); err != nil {
			return fmt.Errorf("agent run: %w", err)
		}

		text := o.lastAssistantText(agent)
		if err := o.parseStructured(text, target); err != nil {
			if attempt < maxNudges {
				slog.Warn("orchestrator: parse error, will nudge", "err", err)
				continue
			}
			return fmt.Errorf("structured response after %d nudges: %w", maxNudges, err)
		}
		return nil
	}
	return nil
}

func (o *Orchestrator) parseStructured(text string, target interface{}) error {
	text = strings.TrimSpace(text)
	// Strip markdown code fences if present
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		if idx := strings.LastIndex(text, "```"); idx >= 0 {
			text = text[:idx]
		}
		text = strings.TrimSpace(text)
	}
	if err := json.Unmarshal([]byte(text), target); err != nil {
		return fmt.Errorf("invalid JSON response (expected key/value pairs like {\"status\":\"done\"}): %w", err)
	}
	return nil
}

// --- Phase 2: plan parsing + state persistence ---

// parsePlan reads a plan.md file and returns its phases with tasks.
// Format expected: ## Phase N: Name followed by - [ ] or - [x] task items.
func parsePlan(path string) ([]OrchPhase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	var phases []OrchPhase
	var cur *OrchPhase
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## ") {
			name := strings.TrimPrefix(line, "## ")
			phases = append(phases, OrchPhase{Name: name})
			cur = &phases[len(phases)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(line, "- [ ]") || strings.HasPrefix(line, "- [x]") {
			task := strings.TrimPrefix(strings.TrimPrefix(line, "- [ ]"), "- [x]")
			task = strings.TrimPrefix(task, " ")
			if strings.HasPrefix(line, "- [x]") {
				cur.Done = append(cur.Done, task)
			}
			cur.Tasks = append(cur.Tasks, task)
		}
	}
	if len(phases) == 0 {
		return nil, fmt.Errorf("plan.md contains no ## Phase headings")
	}
	// Drop phases with no tasks (e.g. free-text "Overview" sections)
	filtered := phases[:0]
	for _, p := range phases {
		if len(p.Tasks) > 0 {
			filtered = append(filtered, p)
		}
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("plan.md has phases but none contain - [ ] task items")
	}
	return filtered, nil
}

// loadState reads orchestrator progress from state.json.
func loadState(path string) (*OrchState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state OrchState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse state.json: %w", err)
	}
	return &state, nil
}

// writeBrief writes a workload brief to the given path.
func writeBrief(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// readReview extracts the pass/fail verdict from a review_N.md file.
func readReviewVerdict(path string) (pass bool, summary string, _ error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, "", fmt.Errorf("read review: %w", err)
	}
	text := string(data)
	pass = strings.Contains(text, "## Verdict: PASS") || strings.Contains(text, "Verdict: PASS")
	// Extract summary — the ## Summary section
	if idx := strings.Index(text, "## Summary"); idx >= 0 {
		rest := strings.TrimLeft(text[idx+len("## Summary"):], "\n\r ")
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			summary = strings.TrimSpace(rest[:nl])
		} else {
			summary = strings.TrimSpace(rest)
		}
	}
	return pass, summary, nil
}

// Ensure these unused imports compile away — remove when they're actually used.
var (
	_ = provider.Message{}
	_ = tool.Tool(nil)
)

// ReviewerSystemPrompt returns the system prompt for the reviewer agent.
func ReviewerSystemPrompt() string {
	return `You are a code reviewer in a developer orchestrator. Review ONLY the
deliverables the developer produced against the workload brief — the
developer may have produced code, documentation, configuration, or
other artifacts.

Check correctness, edge cases, error handling, build/test pass, and
adherence to the task requirements. Never review metadata files like
workload_done.md or workload_rationale.md — those are for the
orchestrator, not part of the deliverable.

Use bash to run git diff and git log to see what changed. Read modified
files with read_file. Do NOT edit or write files yourself — you are
read-only. When proposing alternatives, use pseudocode or step-by-step
instructions. The developer will implement them.

Write your review to the file path given in the prompt, then call the
report_review tool with your verdict.`
}

// ReviewerToolRegistry returns the tool set for the reviewer: read-only tools
// plus bash (for git diff, git log, build verification) and write_file (for review_N.md).
func ReviewerToolRegistry(parent *tool.Registry) *tool.Registry {
	reg := FilterReadOnlyRegistry(parent, reviewerNonResearchTools...)
	if bash, ok := parent.Get("bash"); ok {
		reg.Add(bash)
	}
	if wf, ok := parent.Get("write_file"); ok {
		reg.Add(wf)
	}
	return reg
}

// reviewer tools explicitly excluded from the read-only set
var reviewerNonResearchTools = []string{
	"bash_output", "kill_shell", "wait", // background job tools
	"task", "run_skill", "read_skill", "install_skill", "install_source", // agent/skill management
	"explore", "research", "review", "security_review", // sub-agent tools
	"history", // session history
	"todo_write", "complete_step", "exit_plan_mode", // workflow
	"ask", // user interaction
	"remember", "forget", "memory", // memory
	"slash_command", // command expansion
}
