package rules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// knownRuleTypes lists every rule_type that ships a JSON schema under
// rules/schemas/<rule_type>.schema.json. LoadFromDisk also tolerates rule
// files whose rule_type isn't in this list only insofar as Validate reports
// a clear "unknown rule_type" problem rather than panicking.
var knownRuleTypes = []string{
	"loan_underwriting",
	"risk_scoring",
	"loan_servicing",
	"fx_policy",
	"depeg_policy",
	"spend_waterfall",
	"dashboard_thresholds",
	"risk_weights",
	"liquidity_stress",
}

// ValidationError lists every problem found while validating a rule
// document (YAML parse errors and/or JSON-schema violations). It always
// carries at least one entry.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return fmt.Sprintf("rules: validation failed: %s", e.Problems[0])
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("rules: validation failed with %d problem(s):", len(e.Problems)))
	for _, p := range e.Problems {
		sb.WriteString("\n  - ")
		sb.WriteString(p)
	}
	return sb.String()
}

// loadSchemas compiles rules/schemas/common.schema.json plus every
// rules/schemas/<rule_type>.schema.json found under rulesDir/schemas, and
// returns one compiled schema per rule_type representing
// allOf(common, <rule_type>). It is called once, at NewEngine time.
func loadSchemas(rulesDir string) (map[string]*jsonschema.Schema, error) {
	schemasDir := filepath.Join(rulesDir, "schemas")

	commonDoc, err := loadJSONFile(filepath.Join(schemasDir, "common.schema.json"))
	if err != nil {
		return nil, fmt.Errorf("rules: loading common schema: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("common.schema.json", commonDoc); err != nil {
		return nil, fmt.Errorf("rules: registering common schema: %w", err)
	}

	out := make(map[string]*jsonschema.Schema, len(knownRuleTypes))
	var problems []string
	for _, ruleType := range knownRuleTypes {
		typeURL := ruleType + ".schema.json"
		doc, err := loadJSONFile(filepath.Join(schemasDir, typeURL))
		if err != nil {
			problems = append(problems, fmt.Sprintf("loading schema for %q: %v", ruleType, err))
			continue
		}
		if err := compiler.AddResource(typeURL, doc); err != nil {
			problems = append(problems, fmt.Sprintf("registering schema for %q: %v", ruleType, err))
			continue
		}
		combinedURL := "combined-" + typeURL
		combined := map[string]any{
			"allOf": []any{
				map[string]any{"$ref": "common.schema.json"},
				map[string]any{"$ref": typeURL},
			},
		}
		if err := compiler.AddResource(combinedURL, combined); err != nil {
			problems = append(problems, fmt.Sprintf("combining schema for %q: %v", ruleType, err))
			continue
		}
		sch, err := compiler.Compile(combinedURL)
		if err != nil {
			problems = append(problems, fmt.Sprintf("compiling schema for %q: %v", ruleType, err))
			continue
		}
		out[ruleType] = sch
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("rules: schema load errors:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return out, nil
}

func loadJSONFile(path string) (any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return jsonschema.UnmarshalJSON(f)
}

// canonicalize parses raw YAML into a map[string]any whose values only use
// JSON-compatible types (map[string]any, []any, string, float64, bool,
// nil) by round-tripping the yaml.v3 decode through encoding/json. This
// guards against yaml.v3 producing non-JSON types (e.g. an unquoted date
// decoding to time.Time) and gives every downstream consumer (schema
// validation, evaluators, example matching) one consistent numeric
// representation.
func canonicalize(raw []byte) (map[string]any, error) {
	var v any
	if err := yaml.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	jb, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(jb, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Validate parses raw as YAML, canonicalizes it, and checks it against the
// common schema plus the per-rule_type schema. On failure the returned
// error is a *ValidationError listing every problem found.
func (e *Engine) Validate(raw []byte) (*Rule, error) {
	if e.schemaErr != nil {
		return nil, e.schemaErr
	}

	content, err := canonicalize(raw)
	if err != nil {
		return nil, &ValidationError{Problems: []string{fmt.Sprintf("yaml parse error: %v", err)}}
	}

	var problems []string
	ruleType, _ := content["rule_type"].(string)
	schema, ok := e.schemas[ruleType]
	if !ok {
		problems = append(problems, fmt.Sprintf("unknown rule_type %q (or missing rule_type key)", ruleType))
	} else if verr := schema.Validate(content); verr != nil {
		problems = append(problems, flattenSchemaError(verr)...)
	}
	if len(problems) > 0 {
		return nil, &ValidationError{Problems: problems}
	}

	rule := &Rule{
		Meta:     metaFromContent(content),
		Content:  content,
		Raw:      append([]byte(nil), raw...),
		Examples: examplesFromContent(content),
	}
	return rule, nil
}

// flattenSchemaError turns a jsonschema.ValidationError's tree (its Error()
// already renders one bullet per leaf cause, indented) into a flat,
// readable list of problem strings.
func flattenSchemaError(err error) []string {
	lines := strings.Split(err.Error(), "\n")
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(l, "- ")
		if l == "" {
			continue
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		out = []string{err.Error()}
	}
	return out
}

func metaFromContent(content map[string]any) Meta {
	return Meta{
		RuleID:       strField(content, "rule_id"),
		RuleType:     strField(content, "rule_type"),
		Version:      intField(content, "version"),
		Domain:       strField(content, "domain"),
		Description:  strField(content, "description"),
		Rationale:    strField(content, "rationale"),
		Owner:        strField(content, "owner"),
		LastReviewed: strField(content, "last_reviewed"),
	}
}

func examplesFromContent(content map[string]any) []Example {
	raw, _ := content["examples"].([]any)
	out := make([]Example, 0, len(raw))
	for _, item := range raw {
		m, _ := item.(map[string]any)
		ex := Example{Name: strField(m, "name")}
		if in, ok := m["input"].(map[string]any); ok {
			ex.Input = in
		} else {
			ex.Input = map[string]any{}
		}
		if exp, ok := m["expected"].(map[string]any); ok {
			ex.Expected = exp
		} else {
			ex.Expected = map[string]any{}
		}
		out = append(out, ex)
	}
	return out
}

func strField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func intField(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	f, _ := m[key].(float64)
	return int(f)
}
