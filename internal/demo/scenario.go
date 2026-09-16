// Package demo implements the D8 deliverable: scripted, stage-safe demo
// scenarios read from demo/demo-scenarios.yaml. A scenario is an ordered
// list of steps that either call the real HTTP API in-process (call_api),
// edit a live rule's YAML through the real rules engine (edit_rule), or
// restore every rule the scenario touched back to its pre-demo version
// (reset_scenario). Every edit goes through rules.Engine.Save exactly like
// a human editing the rule in the Rules Editor tab — nothing here bypasses
// validation, examples or versioning.
//
// This is a PoC-only surface (like internal/simulate): it exists purely to
// drive live demos and must be excluded from any production build/deploy.
package demo

import (
	"bytes"
	"fmt"
	"os"

	"github.com/Raven020/stableBank/internal/rules"
	"gopkg.in/yaml.v3"
)

// StepAction is the set of actions a demo step may perform.
type StepAction string

const (
	ActionCallAPI       StepAction = "call_api"
	ActionEditRule      StepAction = "edit_rule"
	ActionResetScenario StepAction = "reset_scenario"
)

// Step is one entry in a scenario's steps list. Which fields are populated
// depends on Action:
//
//   - call_api:  Method, Path, (optional) Request
//   - edit_rule: RuleID, FieldPath, NewValue, (optional) AllowFailingExamples
//   - reset_scenario: none of the above
type Step struct {
	ID        string     `yaml:"id" json:"id"`
	Label     string     `yaml:"label" json:"label"`
	Action    StepAction `yaml:"action" json:"action"`
	Narration string     `yaml:"narration" json:"narration"`

	// call_api
	Method  string         `yaml:"method,omitempty" json:"method,omitempty"`
	Path    string         `yaml:"path,omitempty" json:"path,omitempty"`
	Request map[string]any `yaml:"request,omitempty" json:"request,omitempty"`

	// edit_rule
	RuleID               string `yaml:"rule_id,omitempty" json:"rule_id,omitempty"`
	FieldPath            string `yaml:"field_path,omitempty" json:"field_path,omitempty"`
	NewValue             any    `yaml:"new_value,omitempty" json:"new_value,omitempty"`
	AllowFailingExamples bool   `yaml:"allow_failing_examples,omitempty" json:"allow_failing_examples,omitempty"`
}

// Scenario is one scripted demo: an ordered list of steps.
type Scenario struct {
	ScenarioID  string `yaml:"scenario_id" json:"scenario_id"`
	Description string `yaml:"description" json:"description"`
	Steps       []Step `yaml:"steps" json:"steps"`
}

// scenarioFile is the top-level shape of demo/demo-scenarios.yaml.
type scenarioFile struct {
	Scenarios []Scenario `yaml:"scenarios"`
}

// loadAndValidate reads and parses path, then checks every scenario for
// structural problems: unknown action, missing required fields per action,
// duplicate scenario/step IDs, and edit_rule steps that target a rule_id
// not in engine's registry. It returns the parsed scenarios or the first
// problem found, wrapped with enough context to locate it.
func loadAndValidate(path string, engine *rules.Engine) ([]Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("demo: reading %q: %w", path, err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var f scenarioFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("demo: parsing %q: %w", path, err)
	}
	if len(f.Scenarios) == 0 {
		return nil, fmt.Errorf("demo: %q defines no scenarios", path)
	}

	seenScenarios := make(map[string]bool, len(f.Scenarios))
	for _, sc := range f.Scenarios {
		if sc.ScenarioID == "" {
			return nil, fmt.Errorf("demo: %q: a scenario is missing scenario_id", path)
		}
		if seenScenarios[sc.ScenarioID] {
			return nil, fmt.Errorf("demo: %q: duplicate scenario_id %q", path, sc.ScenarioID)
		}
		seenScenarios[sc.ScenarioID] = true

		if len(sc.Steps) == 0 {
			return nil, fmt.Errorf("demo: scenario %q has no steps", sc.ScenarioID)
		}
		seenSteps := make(map[string]bool, len(sc.Steps))
		for i, st := range sc.Steps {
			if st.ID == "" {
				return nil, fmt.Errorf("demo: scenario %q: step %d is missing id", sc.ScenarioID, i)
			}
			if seenSteps[st.ID] {
				return nil, fmt.Errorf("demo: scenario %q: duplicate step id %q", sc.ScenarioID, st.ID)
			}
			seenSteps[st.ID] = true

			if err := validateStep(sc.ScenarioID, st, engine); err != nil {
				return nil, err
			}
		}
	}
	return f.Scenarios, nil
}

func validateStep(scenarioID string, st Step, engine *rules.Engine) error {
	switch st.Action {
	case ActionCallAPI:
		if st.Method == "" {
			return fmt.Errorf("demo: scenario %q step %q: call_api requires method", scenarioID, st.ID)
		}
		if st.Path == "" {
			return fmt.Errorf("demo: scenario %q step %q: call_api requires path", scenarioID, st.ID)
		}
	case ActionEditRule:
		if st.RuleID == "" {
			return fmt.Errorf("demo: scenario %q step %q: edit_rule requires rule_id", scenarioID, st.ID)
		}
		if st.FieldPath == "" {
			return fmt.Errorf("demo: scenario %q step %q: edit_rule requires field_path", scenarioID, st.ID)
		}
		if st.NewValue == nil {
			return fmt.Errorf("demo: scenario %q step %q: edit_rule requires new_value", scenarioID, st.ID)
		}
		if engine != nil {
			if _, err := engine.Get(st.RuleID); err != nil {
				return fmt.Errorf("demo: scenario %q step %q: rule_id %q is not a registered rule: %w", scenarioID, st.ID, st.RuleID, err)
			}
		}
	case ActionResetScenario:
		// no required fields
	default:
		return fmt.Errorf("demo: scenario %q step %q: unknown action %q", scenarioID, st.ID, st.Action)
	}
	return nil
}

// findStep returns the index and value of the step with the given ID.
func findStep(sc *Scenario, stepID string) (int, Step, error) {
	for i, st := range sc.Steps {
		if st.ID == stepID {
			return i, st, nil
		}
	}
	return -1, Step{}, fmt.Errorf("demo: scenario %q: %w: step %q", sc.ScenarioID, ErrStepNotFound, stepID)
}

// ruleIDsForScenario returns the deduplicated, order-preserving list of
// rule_ids touched by edit_rule steps in sc.
func ruleIDsForScenario(sc *Scenario) []string {
	seen := map[string]bool{}
	var out []string
	for _, st := range sc.Steps {
		if st.Action != ActionEditRule || st.RuleID == "" {
			continue
		}
		if !seen[st.RuleID] {
			seen[st.RuleID] = true
			out = append(out, st.RuleID)
		}
	}
	return out
}
