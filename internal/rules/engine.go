package rules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ErrExamplesFailed is the sentinel wrapped by ExamplesFailedError; use
// errors.Is(err, ErrExamplesFailed) to detect it.
var ErrExamplesFailed = errors.New("rules: one or more examples failed")

// ExamplesFailedError is returned by Save when RunExamples finds a failing
// example and the caller did not set AllowFailingExamples.
type ExamplesFailedError struct {
	Results []ExampleResult
}

func (e *ExamplesFailedError) Error() string {
	return fmt.Sprintf("%v: %d example(s) failed", ErrExamplesFailed, countFailed(e.Results))
}

func (e *ExamplesFailedError) Unwrap() error { return ErrExamplesFailed }

func countFailed(results []ExampleResult) int {
	n := 0
	for _, r := range results {
		if !r.Passed {
			n++
		}
	}
	return n
}

// RegistryEntry is one row of rules/registry.yaml.
type RegistryEntry struct {
	RuleID   string `yaml:"rule_id"`
	RuleType string `yaml:"rule_type"`
	Path     string `yaml:"path"`
	Domain   string `yaml:"domain"`
	Owner    string `yaml:"owner"`
}

// Engine is the D2 rules engine: it loads rule files from disk into a
// versioned store, keeps an in-memory cache of the live version of every
// rule (so Get/GetContent/List never do I/O), evaluates rules through
// domain-registered Evaluators, and exposes an HTTP editor API with hot
// reload.
type Engine struct {
	store    store.RuleStore
	clock    simclock.Clock
	rulesDir string

	schemas   map[string]*jsonschema.Schema
	schemaErr error

	mu           sync.RWMutex
	registry     []RegistryEntry
	registryByID map[string]RegistryEntry
	cache        map[string]*Rule

	evalMu     sync.RWMutex
	evaluators map[string]Evaluator
}

// NewEngine constructs an Engine and compiles every JSON schema under
// rulesDir/schemas immediately. Schema compilation problems are not
// panicked on (there is no error return here per contract); they are
// surfaced the first time Validate/LoadFromDisk is called.
func NewEngine(st store.RuleStore, clock simclock.Clock, rulesDir string) *Engine {
	e := &Engine{
		store:        st,
		clock:        clock,
		rulesDir:     rulesDir,
		registryByID: map[string]RegistryEntry{},
		cache:        map[string]*Rule{},
		evaluators:   map[string]Evaluator{},
	}
	schemas, err := loadSchemas(rulesDir)
	if err != nil {
		e.schemaErr = err
	} else {
		e.schemas = schemas
	}
	return e
}

// RegisterEvaluator registers the business logic for a rule_type. Domain
// packages call this once at wiring time (main.go).
func (e *Engine) RegisterEvaluator(ruleType string, ev Evaluator) {
	e.evalMu.Lock()
	defer e.evalMu.Unlock()
	e.evaluators[ruleType] = ev
}

func (e *Engine) getEvaluator(ruleType string) (Evaluator, bool) {
	e.evalMu.RLock()
	defer e.evalMu.RUnlock()
	ev, ok := e.evaluators[ruleType]
	return ev, ok
}

// Get returns the live (cached) version of a rule. No I/O.
func (e *Engine) Get(ruleID string) (*Rule, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	r, ok := e.cache[ruleID]
	if !ok {
		return nil, fmt.Errorf("rules: %q: %w", ruleID, store.ErrNotFound)
	}
	return r, nil
}

// GetContent returns the live content and version of a rule. Its
// signature matches ledger.RuleReader.Get so the ledger package can adapt
// an *Engine (or a thin wrapper naming this method Get) without importing
// this package's Engine type directly.
func (e *Engine) GetContent(ruleID string) (map[string]any, int, error) {
	r, err := e.Get(ruleID)
	if err != nil {
		return nil, 0, err
	}
	return r.Content, r.Version, nil
}

// List returns the registry entries decorated with their live version and
// last_reviewed date.
func (e *Engine) List() []Summary {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Summary, 0, len(e.registry))
	for _, entry := range e.registry {
		s := Summary{
			RuleID:   entry.RuleID,
			RuleType: entry.RuleType,
			Domain:   entry.Domain,
			Owner:    entry.Owner,
			Path:     entry.Path,
		}
		if r, ok := e.cache[entry.RuleID]; ok {
			s.Version = r.Version
			s.LastReviewed = r.LastReviewed
			s.Description = r.Description
		}
		out = append(out, s)
	}
	return out
}

// Versions returns every stored version of ruleID, ascending.
func (e *Engine) Versions(ctx context.Context, ruleID string) ([]store.RuleVersion, error) {
	return e.store.ListRuleVersions(ctx, ruleID)
}

// Evaluate runs the registered evaluator for ruleID's rule_type against
// the live content, records a store.RuleApplication, and returns the
// Decision.
func (e *Engine) Evaluate(ctx context.Context, ruleID, entityType, entityID string, input map[string]any) (Decision, error) {
	rule, err := e.Get(ruleID)
	if err != nil {
		return Decision{}, err
	}
	ev, ok := e.getEvaluator(rule.RuleType)
	if !ok {
		return Decision{}, fmt.Errorf("rules: no evaluator registered for rule_type %q", rule.RuleType)
	}
	output, err := ev(rule.Content, input)
	if err != nil {
		return Decision{}, fmt.Errorf("rules: evaluating %q: %w", ruleID, err)
	}

	now := e.clock.Now()
	inputsJSON, err := json.Marshal(input)
	if err != nil {
		return Decision{}, fmt.Errorf("rules: marshaling inputs: %w", err)
	}
	outcomeJSON, err := json.Marshal(output)
	if err != nil {
		return Decision{}, fmt.Errorf("rules: marshaling outcome: %w", err)
	}
	app, err := e.store.InsertRuleApplication(ctx, store.RuleApplication{
		RuleID:     rule.RuleID,
		Version:    rule.Version,
		EntityType: entityType,
		EntityID:   entityID,
		Inputs:     inputsJSON,
		Outcome:    outcomeJSON,
		AppliedAt:  now,
	})
	if err != nil {
		return Decision{}, fmt.Errorf("rules: recording application: %w", err)
	}

	return Decision{
		RuleID:        rule.RuleID,
		Version:       rule.Version,
		Input:         input,
		Output:        output,
		AppliedAt:     now,
		ApplicationID: app.ID,
	}, nil
}

// Save validates raw, checks it belongs to ruleID, runs its examples,
// inserts a new version (rewriting the version field, closing the
// previous version) and hot-reloads the cache synchronously.
func (e *Engine) Save(ctx context.Context, ruleID string, raw []byte, opts SaveOptions) (SaveResult, error) {
	e.mu.RLock()
	entry, ok := e.registryByID[ruleID]
	e.mu.RUnlock()
	if !ok {
		return SaveResult{}, fmt.Errorf("rules: %q is not a registered rule_id", ruleID)
	}

	rule, err := e.Validate(raw)
	if err != nil {
		return SaveResult{}, err
	}
	if rule.RuleID != ruleID {
		return SaveResult{}, &ValidationError{Problems: []string{
			fmt.Sprintf("rule_id in document (%q) does not match target rule_id (%q)", rule.RuleID, ruleID),
		}}
	}

	results := e.RunExamples(rule)
	if !opts.AllowFailingExamples {
		for _, r := range results {
			if !r.Passed {
				return SaveResult{}, &ExamplesFailedError{Results: results}
			}
		}
	}

	prevVersion, prevRaw, err := e.latestVersion(ctx, ruleID)
	if err != nil {
		return SaveResult{}, err
	}
	newVersion := prevVersion + 1
	newRaw := rewriteVersionLine(raw, newVersion)

	now := e.clock.Now()
	v := store.RuleVersion{
		RuleID:        ruleID,
		Version:       newVersion,
		RuleType:      entry.RuleType,
		Content:       newRaw,
		ContentHash:   sha256Hex(newRaw),
		SourceCommit:  sourceCommit(),
		Author:        opts.Author,
		ChangeNote:    opts.ChangeNote,
		EffectiveFrom: now,
	}
	if err := e.store.InsertRuleVersion(ctx, v); err != nil {
		return SaveResult{}, fmt.Errorf("rules: saving %q: %w", ruleID, err)
	}

	cacheRule, err := e.Validate(newRaw)
	if err != nil {
		// The submitted raw already validated before the version-line
		// rewrite; a failure here would indicate a rewrite bug, not a bad
		// document, but we must not leave the store and cache
		// inconsistent, so surface it.
		return SaveResult{}, fmt.Errorf("rules: re-validating rewritten version: %w", err)
	}
	cacheRule.EffectiveFrom = now
	e.mu.Lock()
	e.cache[ruleID] = cacheRule
	e.mu.Unlock()

	return SaveResult{
		Version:  newVersion,
		Previous: prevVersion,
		Examples: results,
		Diff:     lineDiff(string(prevRaw), string(newRaw)),
	}, nil
}

// Reactivate inserts a new version whose Content equals the target
// version's content (with the version field rewritten), hot-reloading the
// cache.
func (e *Engine) Reactivate(ctx context.Context, ruleID string, version int, note string) (SaveResult, error) {
	e.mu.RLock()
	entry, ok := e.registryByID[ruleID]
	e.mu.RUnlock()
	if !ok {
		return SaveResult{}, fmt.Errorf("rules: %q is not a registered rule_id", ruleID)
	}

	target, err := e.store.GetRuleVersion(ctx, ruleID, version)
	if err != nil {
		return SaveResult{}, fmt.Errorf("rules: fetching version %d of %q: %w", version, ruleID, err)
	}

	prevVersion, prevRaw, err := e.latestVersion(ctx, ruleID)
	if err != nil {
		return SaveResult{}, err
	}
	newVersion := prevVersion + 1
	newRaw := rewriteVersionLine(target.Content, newVersion)

	rule, err := e.Validate(newRaw)
	if err != nil {
		return SaveResult{}, err
	}

	now := e.clock.Now()
	v := store.RuleVersion{
		RuleID:        ruleID,
		Version:       newVersion,
		RuleType:      entry.RuleType,
		Content:       newRaw,
		ContentHash:   sha256Hex(newRaw),
		SourceCommit:  sourceCommit(),
		Author:        "",
		ChangeNote:    fmt.Sprintf("reactivated version %d: %s", version, note),
		EffectiveFrom: now,
	}
	if err := e.store.InsertRuleVersion(ctx, v); err != nil {
		return SaveResult{}, fmt.Errorf("rules: reactivating %q: %w", ruleID, err)
	}

	rule.EffectiveFrom = now
	e.mu.Lock()
	e.cache[ruleID] = rule
	e.mu.Unlock()

	results := e.RunExamples(rule)
	return SaveResult{
		Version:  newVersion,
		Previous: prevVersion,
		Examples: results,
		Diff:     lineDiff(string(prevRaw), string(newRaw)),
	}, nil
}

// latestVersion returns the highest version number stored for ruleID and
// the raw content of whichever version is currently open (EffectiveTo ==
// nil), which should always be the same version in practice.
func (e *Engine) latestVersion(ctx context.Context, ruleID string) (int, []byte, error) {
	versions, err := e.store.ListRuleVersions(ctx, ruleID)
	if err != nil {
		return 0, nil, fmt.Errorf("rules: listing versions of %q: %w", ruleID, err)
	}
	var (
		maxVersion int
		openRaw    []byte
	)
	for _, v := range versions {
		if v.Version > maxVersion {
			maxVersion = v.Version
		}
		if v.EffectiveTo == nil {
			openRaw = v.Content
		}
	}
	return maxVersion, openRaw, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sourceCommit() string {
	if c := os.Getenv("SOURCE_COMMIT"); c != "" {
		return c
	}
	return "local"
}
