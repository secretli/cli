package cli

import (
	"fmt"
	"path"
	"strconv"
	"strings"
)

// pickFiles reads the answer to "Save which?". An empty answer is every
// file. Otherwise it is numbers counted from 1, ranges like 2-4, and names
// or patterns like *.jpg, separated by spaces or commas; names and patterns
// ignore case. It returns the chosen files' indexes in list order.
func pickFiles(answer string, names []string) ([]int, error) {
	tokens := strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	if len(tokens) == 0 {
		all := make([]int, len(names))
		for i := range all {
			all[i] = i
		}
		return all, nil
	}
	chosen := make([]bool, len(names))
	for _, token := range tokens {
		if from, to, ok := numberRange(token); ok {
			if from > to {
				from, to = to, from
			}
			if from < 1 || to > len(names) {
				return nil, fmt.Errorf("there is no file %s; the files are numbered 1 to %d", token, len(names))
			}
			for i := from; i <= to; i++ {
				chosen[i-1] = true
			}
			continue
		}
		matched := false
		for i, name := range names {
			if matchesName(token, name) {
				chosen[i] = true
				matched = true
			}
		}
		if !matched {
			return nil, fmt.Errorf("no file is called %q", token)
		}
	}
	var picked []int
	for i, ok := range chosen {
		if ok {
			picked = append(picked, i)
		}
	}
	return picked, nil
}

// numberRange reads "3" or "2-4".
func numberRange(token string) (int, int, bool) {
	first, last, isRange := strings.Cut(token, "-")
	from, err := strconv.Atoi(first)
	if err != nil || !allDigits(first) {
		return 0, 0, false
	}
	if !isRange {
		return from, from, true
	}
	to, err := strconv.Atoi(last)
	if err != nil || !allDigits(last) {
		return 0, 0, false
	}
	return from, to, true
}

func allDigits(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return r < '0' || r > '9' })
}

// matchesName compares a typed name or pattern with a file's name. A name
// that happens to contain pattern characters still matches itself.
func matchesName(token, name string) bool {
	token, name = strings.ToLower(token), strings.ToLower(name)
	if token == name {
		return true
	}
	ok, err := path.Match(token, name)
	return err == nil && ok
}
