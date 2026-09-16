package rules

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// MatchExpected reports whether expected is a subset of actual: only the
// keys present in expected are compared, maps recurse, arrays are compared
// element-wise by length and content, and numbers are compared as float64
// after a JSON round trip (both sides are JSON-normalised first, so it
// doesn't matter whether callers pass json.Number, int, int64 or float64).
// On mismatch it returns a human-readable description of the first
// difference found.
func MatchExpected(expected, actual map[string]any) (bool, string) {
	en, err := normalizeJSON(expected)
	if err != nil {
		return false, fmt.Sprintf("normalizing expected: %v", err)
	}
	an, err := normalizeJSON(actual)
	if err != nil {
		return false, fmt.Sprintf("normalizing actual: %v", err)
	}
	return matchSubset(en, an, "$")
}

func normalizeJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func matchSubset(expected, actual any, path string) (bool, string) {
	switch ev := expected.(type) {
	case map[string]any:
		av, ok := actual.(map[string]any)
		if !ok {
			return false, fmt.Sprintf("%s: expected an object, got %s", path, describe(actual))
		}
		keys := make([]string, 0, len(ev))
		for k := range ev {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			childPath := path + "." + k
			actualVal, present := av[k]
			if !present {
				return false, fmt.Sprintf("%s: missing key %q in actual output", childPath, k)
			}
			if ok, msg := matchSubset(ev[k], actualVal, childPath); !ok {
				return false, msg
			}
		}
		return true, ""
	case []any:
		av, ok := actual.([]any)
		if !ok {
			return false, fmt.Sprintf("%s: expected an array, got %s", path, describe(actual))
		}
		if len(ev) != len(av) {
			return false, fmt.Sprintf("%s: expected array of length %d, got length %d", path, len(ev), len(av))
		}
		for i := range ev {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			if ok, msg := matchSubset(ev[i], av[i], childPath); !ok {
				return false, msg
			}
		}
		return true, ""
	default:
		if !reflect.DeepEqual(expected, actual) {
			return false, fmt.Sprintf("%s: expected %v, got %v", path, expected, actual)
		}
		return true, ""
	}
}

func describe(v any) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%T(%v)", v, v)
}
