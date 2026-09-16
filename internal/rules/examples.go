package rules

import "fmt"

// RunExamples runs every worked example on rule against the evaluator
// registered for rule.RuleType, comparing the evaluator's output to the
// example's expected output via MatchExpected (subset match).
func (e *Engine) RunExamples(rule *Rule) []ExampleResult {
	out := make([]ExampleResult, 0, len(rule.Examples))
	ev, ok := e.getEvaluator(rule.RuleType)
	for _, ex := range rule.Examples {
		res := ExampleResult{Name: ex.Name, Expected: ex.Expected}
		if !ok {
			res.Error = fmt.Sprintf("no evaluator registered for rule_type %q", rule.RuleType)
			out = append(out, res)
			continue
		}
		actual, err := ev(rule.Content, ex.Input)
		if err != nil {
			res.Error = err.Error()
			out = append(out, res)
			continue
		}
		res.Actual = actual
		passed, msg := MatchExpected(ex.Expected, actual)
		res.Passed = passed
		if !passed {
			res.Error = msg
		}
		out = append(out, res)
	}
	return out
}
