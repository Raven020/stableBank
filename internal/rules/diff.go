package rules

import (
	"fmt"
	"strings"
)

// lineDiff produces a simple unified-style line diff between a and b using
// a longest-common-subsequence backtrace (stdlib only): unchanged lines
// are prefixed "  ", removed lines "- ", added lines "+ ".
func lineDiff(a, b string) string {
	aLines := splitLines(a)
	bLines := splitLines(b)
	n, m := len(aLines), len(bLines)

	// dp[i][j] = length of the LCS of aLines[i:] and bLines[j:]
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if aLines[i] == bLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var sb strings.Builder
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case aLines[i] == bLines[j]:
			fmt.Fprintf(&sb, "  %s\n", aLines[i])
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			fmt.Fprintf(&sb, "- %s\n", aLines[i])
			i++
		default:
			fmt.Fprintf(&sb, "+ %s\n", bLines[j])
			j++
		}
	}
	for ; i < n; i++ {
		fmt.Fprintf(&sb, "- %s\n", aLines[i])
	}
	for ; j < m; j++ {
		fmt.Fprintf(&sb, "+ %s\n", bLines[j])
	}
	return strings.TrimRight(sb.String(), "\n")
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
