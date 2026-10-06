package stacks

import (
	"fmt"
	"strings"
)

// DiffLine is one line of a line diff: op " " (kept), "-" (removed), "+"
// (added), or "…" for a folded run of kept lines (text says how many).
type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// Diff limits: files are small, but the table is O(n·m).
const (
	diffMaxLines = 4000
	diffContext  = 3
)

// lineDiff is a line diff from a to b, with runs of more than
// 2·diffContext kept lines folded. It's the longest common subsequence,
// which for compose files (hundreds of lines) is instant; past
// diffMaxLines it shows the whole of a removed and the whole of b added.
func lineDiff(a, b string) []DiffLine {
	al, bl := splitLines(a), splitLines(b)
	var raw []DiffLine
	if len(al) > diffMaxLines || len(bl) > diffMaxLines {
		for _, l := range al {
			raw = append(raw, DiffLine{"-", l})
		}
		for _, l := range bl {
			raw = append(raw, DiffLine{"+", l})
		}
		return raw
	}
	// Trim the common prefix and suffix first; edits are usually local.
	pre := 0
	for pre < len(al) && pre < len(bl) && al[pre] == bl[pre] {
		pre++
	}
	suf := 0
	for suf < len(al)-pre && suf < len(bl)-pre && al[len(al)-1-suf] == bl[len(bl)-1-suf] {
		suf++
	}
	am, bm := al[pre:len(al)-suf], bl[pre:len(bl)-suf]
	n, m := len(am), len(bm)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if am[i] == bm[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	for _, l := range al[:pre] {
		raw = append(raw, DiffLine{" ", l})
	}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && am[i] == bm[j]:
			raw = append(raw, DiffLine{" ", am[i]})
			i++
			j++
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			raw = append(raw, DiffLine{"+", bm[j]})
			j++
		default:
			raw = append(raw, DiffLine{"-", am[i]})
			i++
		}
	}
	for _, l := range al[len(al)-suf:] {
		raw = append(raw, DiffLine{" ", l})
	}
	return fold(raw)
}

// fold replaces long runs of kept lines with a "…" line, keeping
// diffContext lines on either side of each change.
func fold(raw []DiffLine) []DiffLine {
	out := []DiffLine{}
	for i := 0; i < len(raw); {
		if raw[i].Op != " " {
			out = append(out, raw[i])
			i++
			continue
		}
		j := i
		for j < len(raw) && raw[j].Op == " " {
			j++
		}
		run := raw[i:j]
		head, tail := diffContext, diffContext
		if i == 0 {
			head = 0
		}
		if j == len(raw) {
			tail = 0
		}
		if len(run) > head+tail+1 {
			out = append(out, run[:head]...)
			out = append(out, DiffLine{"…", fmt.Sprintf("%d unchanged lines", len(run)-head-tail)})
			out = append(out, run[len(run)-tail:]...)
		} else {
			out = append(out, run...)
		}
		i = j
	}
	return out
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
