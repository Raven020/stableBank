// Package rules implements the D2 deliverable: a hot-reloadable rules engine
// whose business logic lives in versioned, auditable YAML files. Domain
// packages (ledger, loan, account, dashboard) register Evaluators for the
// rule_types they own; this package owns loading, validating, persisting,
// diffing and serving those rules over HTTP.
package rules

import "time"

// Meta holds the common metadata every rule file carries.
type Meta struct {
	RuleID       string `json:"rule_id" yaml:"rule_id"`
	RuleType     string `json:"rule_type" yaml:"rule_type"`
	Version      int    `json:"version" yaml:"version"`
	Domain       string `json:"domain" yaml:"domain"`
	Description  string `json:"description" yaml:"description"`
	Rationale    string `json:"rationale" yaml:"rationale"`
	Owner        string `json:"owner" yaml:"owner"`
	LastReviewed string `json:"last_reviewed" yaml:"last_reviewed"`
}

// Example is one worked input/output pair authored alongside a rule so that
// its evaluator can be checked automatically (RunExamples) whenever the rule
// is saved.
type Example struct {
	Name     string         `json:"name" yaml:"name"`
	Input    map[string]any `json:"input" yaml:"input"`
	Expected map[string]any `json:"expected" yaml:"expected"`
}

// Rule is one live (or historical) rule document: its metadata, its whole
// parsed body (JSON-compatible types throughout, i.e. numbers are float64),
// the exact bytes it was authored as, and its worked examples.
type Rule struct {
	Meta
	Content       map[string]any `json:"content"`
	Raw           []byte         `json:"-"`
	Examples      []Example      `json:"examples"`
	EffectiveFrom time.Time      `json:"effective_from"`
}

// Evaluator runs the business logic for one rule_type against the live rule
// content and a request-shaped input, returning a JSON-compatible output.
type Evaluator func(content map[string]any, input map[string]any) (map[string]any, error)

// Summary is the lightweight listing shape returned by List/GET /rules.
type Summary struct {
	RuleID       string `json:"rule_id"`
	RuleType     string `json:"rule_type"`
	Domain       string `json:"domain"`
	Owner        string `json:"owner"`
	Path         string `json:"path"`
	Description  string `json:"description,omitempty"`
	Version      int    `json:"version,omitempty"`
	LastReviewed string `json:"last_reviewed,omitempty"`
}

// Decision is the result of Evaluate: the rule version that was applied,
// the input/output pair, and the audit record it produced.
type Decision struct {
	RuleID        string         `json:"rule_id"`
	Version       int            `json:"version"`
	Input         map[string]any `json:"input"`
	Output        map[string]any `json:"output"`
	AppliedAt     time.Time      `json:"applied_at"`
	ApplicationID string         `json:"application_id"`
}

// ExampleResult is the outcome of running one Example against the
// registered evaluator.
type ExampleResult struct {
	Name     string         `json:"name"`
	Passed   bool           `json:"passed"`
	Expected map[string]any `json:"expected"`
	Actual   map[string]any `json:"actual,omitempty"`
	Error    string         `json:"error,omitempty"`
}

// SaveOptions controls how Save treats a submitted edit.
type SaveOptions struct {
	Author               string
	ChangeNote           string
	AllowFailingExamples bool
}

// SaveResult is returned by Save and Reactivate.
type SaveResult struct {
	Version  int             `json:"version"`
	Previous int             `json:"previous"`
	Examples []ExampleResult `json:"examples"`
	Diff     string          `json:"diff"`
}
