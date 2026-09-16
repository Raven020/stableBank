package demo

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const fieldPathFixture = `rule_id: fixture
eligibility:
  min_credit_score: 620 # do not lower without sign-off
  min_account_age_days: 90

conditions_evaluated_in_order:
  - { rule: prefer_fiat_if_sufficient } # tried first
  - { rule: prefer_stablecoin_if_sufficient }
`

func parseFixture(t *testing.T) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fieldPathFixture), &doc); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return &doc
}

func TestSetFieldPathNestedKeyPreservesComments(t *testing.T) {
	doc := parseFixture(t)

	old, err := setFieldPath(doc, "eligibility.min_credit_score", 700)
	if err != nil {
		t.Fatalf("setFieldPath: %v", err)
	}
	if old != 620 {
		t.Fatalf("old value = %v, want 620", old)
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(out)

	if !strings.Contains(text, "min_credit_score: 700") {
		t.Fatalf("new value not applied:\n%s", text)
	}
	if !strings.Contains(text, "# do not lower without sign-off") {
		t.Fatalf("comment on edited line was lost:\n%s", text)
	}
	if !strings.Contains(text, "min_account_age_days: 90") {
		t.Fatalf("unrelated sibling key was lost:\n%s", text)
	}
}

func TestSetFieldPathIndexedSequencePreservesComments(t *testing.T) {
	doc := parseFixture(t)

	old, err := setFieldPath(doc, "conditions_evaluated_in_order[0].rule", "prefer_stablecoin_if_sufficient")
	if err != nil {
		t.Fatalf("setFieldPath: %v", err)
	}
	if old != "prefer_fiat_if_sufficient" {
		t.Fatalf("old value = %v, want prefer_fiat_if_sufficient", old)
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(out)

	if !strings.Contains(text, "rule: prefer_stablecoin_if_sufficient") {
		t.Fatalf("new value not applied:\n%s", text)
	}
	if !strings.Contains(text, "# tried first") {
		t.Fatalf("comment on edited sequence element was lost:\n%s", text)
	}
	// The second sequence element (untouched) should still be present.
	if strings.Count(text, "prefer_stablecoin_if_sufficient") != 2 {
		t.Fatalf("expected two occurrences (edited first + original second), got:\n%s", text)
	}
}

func TestSetFieldPathErrors(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"missing key", "eligibility.does_not_exist"},
		{"index out of range", "conditions_evaluated_in_order[9].rule"},
		{"index on non-sequence", "eligibility[0]"},
		{"key on non-mapping", "eligibility.min_credit_score.nope"},
		{"empty path", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := parseFixture(t)
			if _, err := setFieldPath(doc, tc.path, "x"); err == nil {
				t.Fatalf("expected an error for path %q", tc.path)
			}
		})
	}
}

func TestEncodeScalarInfersTag(t *testing.T) {
	cases := []struct {
		in      any
		tag     string
		wantVal string
	}{
		{700, "!!int", "700"},
		{10.5, "!!float", "10.5"},
		{true, "!!bool", "true"},
		{"prefer_stablecoin_if_sufficient", "!!str", "prefer_stablecoin_if_sufficient"},
	}
	for _, tc := range cases {
		tag, val, err := encodeScalar(tc.in)
		if err != nil {
			t.Fatalf("encodeScalar(%v): %v", tc.in, err)
		}
		if tag != tc.tag || val != tc.wantVal {
			t.Fatalf("encodeScalar(%v) = (%s, %s), want (%s, %s)", tc.in, tag, val, tc.tag, tc.wantVal)
		}
	}
}
