package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParsePlan(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")

	t.Run("valid plan", func(t *testing.T) {
		content := `## Phase 1: Setup
- [x] First task done
- [ ] Second task pending
- [ ] Third task pending

## Phase 2: Core
- [ ] Core task 1
- [ ] Core task 2
`
		if err := os.WriteFile(planPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		phases, err := parsePlan(planPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(phases) != 2 {
			t.Fatalf("got %d phases, want 2", len(phases))
		}
		if phases[0].Name != "Phase 1: Setup" {
			t.Errorf("phase 0 name = %q", phases[0].Name)
		}
		if len(phases[0].Tasks) != 3 {
			t.Errorf("phase 0 tasks = %d, want 3", len(phases[0].Tasks))
		}
		if len(phases[0].Done) != 1 {
			t.Errorf("phase 0 done = %d, want 1", len(phases[0].Done))
		}
		if phases[0].Done[0] != "First task done" {
			t.Errorf("phase 0 done[0] = %q", phases[0].Done[0])
		}
	})

	t.Run("empty file", func(t *testing.T) {
		if err := os.WriteFile(planPath, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := parsePlan(planPath)
		if err == nil {
			t.Fatal("expected error for empty plan")
		}
	})
}

func TestLoadSaveState(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")

	state := &OrchState{
		Phase: 1,
		Task:  2,
		Phases: []OrchPhase{
			{Name: "Setup", Tasks: []string{"a", "b"}, Done: []string{"a"}},
		},
		Retries: 0,
		Status:  "developing",
	}

	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, b, 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Phase != 1 || loaded.Task != 2 {
		t.Errorf("phase/task mismatch: %d/%d", loaded.Phase, loaded.Task)
	}
	if loaded.Status != "developing" {
		t.Errorf("status = %q", loaded.Status)
	}
	if len(loaded.Phases) != 1 {
		t.Errorf("phases = %d", len(loaded.Phases))
	}
}

func TestReadReviewVerdict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review_1.md")

	t.Run("pass", func(t *testing.T) {
		os.WriteFile(path, []byte("## Verdict: PASS\n## Summary\nAll good."), 0o644)
		pass, summary, err := readReviewVerdict(path)
		if err != nil {
			t.Fatal(err)
		}
		if !pass {
			t.Error("expected pass")
		}
		if summary != "All good." {
			t.Errorf("summary = %q", summary)
		}
	})

	t.Run("fail", func(t *testing.T) {
		os.WriteFile(path, []byte("## Verdict: FAIL\n## Summary\nBad code."), 0o644)
		pass, _, err := readReviewVerdict(path)
		if err != nil {
			t.Fatal(err)
		}
		if pass {
			t.Error("expected fail")
		}
	})
}
