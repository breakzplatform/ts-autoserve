// Package portset matches ports against lists of ports and ranges.
package portset

import (
	"fmt"
	"strconv"
	"strings"
)

// Set is a list of ports and inclusive ranges, e.g. "3000", "5173-5183".
type Set []span

type span struct{ lo, hi int }

// Parse builds a Set from entries like "3000" or "5173-5183".
func Parse(entries []string) (Set, error) {
	var s Set
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		lo, hi, err := parseEntry(e)
		if err != nil {
			return nil, err
		}
		s = append(s, span{lo, hi})
	}
	return s, nil
}

func parseEntry(e string) (int, int, error) {
	if lo, hi, ok := strings.Cut(e, "-"); ok {
		l, err := port(lo)
		if err != nil {
			return 0, 0, err
		}
		h, err := port(hi)
		if err != nil {
			return 0, 0, err
		}
		if l > h {
			return 0, 0, fmt.Errorf("port range %q is inverted", e)
		}
		return l, h, nil
	}
	p, err := port(e)
	return p, p, err
}

func port(s string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return p, nil
}

// Has reports whether p falls in the set.
func (s Set) Has(p int) bool {
	for _, sp := range s {
		if p >= sp.lo && p <= sp.hi {
			return true
		}
	}
	return false
}

// Empty reports whether the set has no entries.
func (s Set) Empty() bool { return len(s) == 0 }
