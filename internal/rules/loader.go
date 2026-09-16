package rules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Raven020/stableBank/internal/store"
	"gopkg.in/yaml.v3"
)

// registryFile is the shape of rules/registry.yaml.
type registryFile struct {
	Rules []RegistryEntry `yaml:"rules"`
}

// LoadFromDisk reads rules/registry.yaml, validates every referenced rule
// file against the common + per-type JSON schemas, inserts version 1 into
// the RuleStore for any rule_id that has no stored versions yet, and then
// warms the in-memory cache with the latest (or currently open) stored
// version of every registered rule. Persisted edits from a previous run
// (e.g. against Postgres) therefore win over the on-disk file.
func (e *Engine) LoadFromDisk(ctx context.Context) error {
	if e.schemaErr != nil {
		return e.schemaErr
	}

	registryPath := filepath.Join(e.rulesDir, "registry.yaml")
	registryBytes, err := os.ReadFile(registryPath)
	if err != nil {
		return fmt.Errorf("rules: reading registry %q: %w", registryPath, err)
	}
	var rf registryFile
	if err := yaml.Unmarshal(registryBytes, &rf); err != nil {
		return fmt.Errorf("rules: parsing registry %q: %w", registryPath, err)
	}
	if len(rf.Rules) == 0 {
		return fmt.Errorf("rules: registry %q lists no rules", registryPath)
	}

	registryByID := make(map[string]RegistryEntry, len(rf.Rules))
	for _, entry := range rf.Rules {
		registryByID[entry.RuleID] = entry
	}
	e.mu.Lock()
	e.registry = rf.Rules
	e.registryByID = registryByID
	e.mu.Unlock()

	var problems []string
	for _, entry := range rf.Rules {
		path := e.resolveRulePath(entry.Path)
		raw, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: reading %q: %v", entry.RuleID, path, err))
			continue
		}

		rule, err := e.Validate(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s (%s): %v", entry.RuleID, path, err))
			continue
		}
		if rule.RuleID != entry.RuleID {
			problems = append(problems, fmt.Sprintf("%s (%s): rule_id in file is %q, registry says %q", entry.RuleID, path, rule.RuleID, entry.RuleID))
			continue
		}
		if rule.RuleType != entry.RuleType {
			problems = append(problems, fmt.Sprintf("%s (%s): rule_type in file is %q, registry says %q", entry.RuleID, path, rule.RuleType, entry.RuleType))
			continue
		}

		versions, err := e.store.ListRuleVersions(ctx, entry.RuleID)
		if err != nil {
			return fmt.Errorf("rules: listing versions of %q: %w", entry.RuleID, err)
		}
		if len(versions) == 0 {
			v := store.RuleVersion{
				RuleID:        entry.RuleID,
				Version:       1,
				RuleType:      entry.RuleType,
				Content:       raw,
				ContentHash:   sha256Hex(raw),
				SourceCommit:  sourceCommit(),
				Author:        "system",
				ChangeNote:    "initial load from disk",
				EffectiveFrom: e.clock.Now(),
			}
			if err := e.store.InsertRuleVersion(ctx, v); err != nil {
				return fmt.Errorf("rules: inserting initial version of %q: %w", entry.RuleID, err)
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("rules: load from disk failed with %d problem(s):\n  - %s", len(problems), strings.Join(problems, "\n  - "))
	}

	// Warm the cache from the store's live version of every registered
	// rule (which may differ from the on-disk file if it was edited at
	// runtime in a previous run against a persistent store).
	for _, entry := range rf.Rules {
		versions, err := e.store.ListRuleVersions(ctx, entry.RuleID)
		if err != nil {
			return fmt.Errorf("rules: listing versions of %q: %w", entry.RuleID, err)
		}
		live, ok := openVersion(versions)
		if !ok {
			return fmt.Errorf("rules: %q has stored versions but none is currently open", entry.RuleID)
		}
		rule, err := e.Validate(live.Content)
		if err != nil {
			return fmt.Errorf("rules: warming cache for %q: %w", entry.RuleID, err)
		}
		rule.EffectiveFrom = live.EffectiveFrom
		e.mu.Lock()
		e.cache[entry.RuleID] = rule
		e.mu.Unlock()
	}
	return nil
}

func openVersion(versions []store.RuleVersion) (store.RuleVersion, bool) {
	var best store.RuleVersion
	found := false
	for _, v := range versions {
		if v.EffectiveTo == nil && (!found || v.Version > best.Version) {
			best = v
			found = true
		}
	}
	return best, found
}

// resolveRulePath resolves a registry entry's path (written repo-root
// relative, e.g. "rules/loan/loan_underwriting.yaml", matching
// rules/registry.yaml) against the directory that contains rulesDir
// itself, so callers can pass any rulesDir (e.g. "rules" or
// "../../rules") without the registry needing to know about it.
func (e *Engine) resolveRulePath(entryPath string) string {
	if filepath.IsAbs(entryPath) {
		return entryPath
	}
	return filepath.Join(filepath.Dir(e.rulesDir), entryPath)
}

var topLevelVersionLine = regexp.MustCompile(`(?m)^version:[^\n]*$`)

// rewriteVersionLine replaces the top-level `version:` line with the given
// version number, preserving everything else (formatting, comments). If
// no top-level version line is found, one is prepended.
func rewriteVersionLine(raw []byte, version int) []byte {
	newLine := fmt.Sprintf("version: %d", version)
	if topLevelVersionLine.Match(raw) {
		return topLevelVersionLine.ReplaceAll(raw, []byte(newLine))
	}
	out := make([]byte, 0, len(raw)+len(newLine)+1)
	out = append(out, []byte(newLine+"\n")...)
	out = append(out, raw...)
	return out
}
