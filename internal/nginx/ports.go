package nginx

import (
	"strconv"
	"strings"
)

// ParsePorts reads a free-form "53, 8000-8100; 9000" list into ranges, dropping
// unreadable tokens so one typo does not cost the ports listed around it.
func ParsePorts(input string) []PortRange {
	var out []PortRange
	seen := map[PortRange]bool{}
	for _, tok := range strings.Split(strings.ReplaceAll(input, ";", ","), ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(tok, "-")
		from, ok1 := parsePortNumber(lo)
		to, ok2 := from, true
		if isRange {
			to, ok2 = parsePortNumber(hi)
		}
		if !ok1 || !ok2 || from > to {
			continue
		}
		r := PortRange{From: from, To: to}
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

func parsePortNumber(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0, false
	}
	return n, true
}
