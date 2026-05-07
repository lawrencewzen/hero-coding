// Package utils intentionally ships with a small bug for hero-coding to fix.
package utils

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseRange parses "a-b" into the inclusive integer range [a, b].
// e.g. ParseRange("1-5") should return []int{1, 2, 3, 4, 5}.
func ParseRange(input string) ([]int, error) {
	parts := strings.SplitN(input, "-", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid range: %q", input)
	}
	a, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid range: %q", input)
	}
	b, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid range: %q", input)
	}
	out := make([]int, 0, b-a)
	for i := a; i < b; i++ {
		out = append(out, i)
	}
	return out, nil
}
