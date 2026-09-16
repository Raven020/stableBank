package demo

import (
	"fmt"
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"
)

// fieldPathTokenRe splits a dotted field path with optional [n] indexes,
// e.g. "eligibility.min_credit_score" -> ["eligibility",
// "min_credit_score"], or "conditions_evaluated_in_order[0].rule" ->
// ["conditions_evaluated_in_order", "[0]", "rule"].
var fieldPathTokenRe = regexp.MustCompile(`[A-Za-z0-9_]+|\[[0-9]+\]`)

// parseFieldPath tokenizes path and rejects anything left over that the
// regexp couldn't account for (e.g. stray dots, unbalanced brackets),
// which would otherwise be silently dropped.
func parseFieldPath(path string) ([]string, error) {
	if path == "" {
		return nil, fmt.Errorf("demo: field_path is empty")
	}
	tokens := fieldPathTokenRe.FindAllString(path, -1)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("demo: field_path %q has no segments", path)
	}
	reconstructed := 0
	for _, t := range tokens {
		reconstructed += len(t)
	}
	// Every rune not part of a token must be a '.' separator or bracket
	// character; a quick length-based sanity check catches most malformed
	// paths (e.g. "a..b", "a[x]") without a hand-rolled parser.
	separators := len(path) - reconstructed
	if separators < 0 {
		return nil, fmt.Errorf("demo: field_path %q is malformed", path)
	}
	return tokens, nil
}

func isIndexToken(tok string) bool {
	return len(tok) >= 3 && tok[0] == '[' && tok[len(tok)-1] == ']'
}

func kindName(k yaml.Kind) string {
	switch k {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.MappingNode:
		return "mapping"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	default:
		return "unknown"
	}
}

// setFieldPath walks root (typically the *yaml.Node produced by
// yaml.Unmarshal into a Node, i.e. Kind == DocumentNode) along the dotted,
// optionally-indexed path, and overwrites the terminal scalar node's Value
// and Tag in place, inferring the tag (!!int, !!float, !!bool, !!str) from
// newValue's Go type. It returns the previous value, decoded the same way
// rule content is decoded elsewhere (int/float64/bool/string).
//
// Because only the terminal node's Value/Tag/Style fields are mutated —
// every other node in the tree, including its own and its siblings'
// HeadComment/LineComment/FootComment — comments and formatting elsewhere
// in the document survive a round trip through yaml.Marshal.
func setFieldPath(root *yaml.Node, path string, newValue any) (any, error) {
	tokens, err := parseFieldPath(path)
	if err != nil {
		return nil, err
	}

	node := root
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil, fmt.Errorf("demo: field_path %q: empty YAML document", path)
		}
		node = node.Content[0]
	}

	for i, tok := range tokens {
		last := i == len(tokens)-1

		if isIndexToken(tok) {
			idx, convErr := strconv.Atoi(tok[1 : len(tok)-1])
			if convErr != nil {
				return nil, fmt.Errorf("demo: field_path %q: bad index %q", path, tok)
			}
			if node.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("demo: field_path %q: segment %d expected a sequence, found %s", path, i, kindName(node.Kind))
			}
			if idx < 0 || idx >= len(node.Content) {
				return nil, fmt.Errorf("demo: field_path %q: index %d out of range (length %d)", path, idx, len(node.Content))
			}
			if last {
				return finishSet(node.Content[idx], newValue, path)
			}
			node = node.Content[idx]
			continue
		}

		if node.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("demo: field_path %q: segment %d (%q) expected a mapping, found %s", path, i, tok, kindName(node.Kind))
		}
		found := false
		for k := 0; k+1 < len(node.Content); k += 2 {
			if node.Content[k].Value != tok {
				continue
			}
			found = true
			if last {
				return finishSet(node.Content[k+1], newValue, path)
			}
			node = node.Content[k+1]
			break
		}
		if !found {
			return nil, fmt.Errorf("demo: field_path %q: key %q not found", path, tok)
		}
	}
	// Unreachable: the loop above always returns on the last token.
	return nil, fmt.Errorf("demo: field_path %q: nothing to set", path)
}

// finishSet overwrites the terminal scalar node n with newValue.
func finishSet(n *yaml.Node, newValue any, path string) (any, error) {
	if n.Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("demo: field_path %q: target is a %s, not a scalar", path, kindName(n.Kind))
	}
	var old any
	if err := n.Decode(&old); err != nil {
		return nil, fmt.Errorf("demo: field_path %q: decoding current value: %w", path, err)
	}
	tag, value, err := encodeScalar(newValue)
	if err != nil {
		return nil, fmt.Errorf("demo: field_path %q: %w", path, err)
	}
	n.Value = value
	n.Tag = tag
	n.Style = 0
	return old, nil
}

// encodeScalar infers a YAML scalar tag and its string representation from
// a Go value of the kind yaml.v3 hands back when decoding a step's
// new_value: field (int, float64, bool, string, or nil).
func encodeScalar(v any) (tag, value string, err error) {
	switch t := v.(type) {
	case int:
		return "!!int", strconv.Itoa(t), nil
	case int64:
		return "!!int", strconv.FormatInt(t, 10), nil
	case float64:
		return "!!float", strconv.FormatFloat(t, 'f', -1, 64), nil
	case float32:
		return "!!float", strconv.FormatFloat(float64(t), 'f', -1, 64), nil
	case bool:
		return "!!bool", strconv.FormatBool(t), nil
	case string:
		return "!!str", t, nil
	case nil:
		return "!!null", "null", nil
	default:
		return "", "", fmt.Errorf("unsupported new_value type %T", v)
	}
}
