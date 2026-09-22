package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Table lays out rows as columns two spaces apart, padding each cell to the
// widest text of its column, so the columns line up in every mode. A non-nil
// header becomes a first, muted row. Cells keep their tones; the padding has
// none, and the last cell of a row is not padded.
func Table(header []string, rows [][]Span) []Line {
	all := make([][]Span, 0, len(rows)+1)
	if header != nil {
		head := make([]Span, len(header))
		for i, h := range header {
			head[i] = Span{Text: h, Tone: ToneMuted}
		}
		all = append(all, head)
	}
	all = append(all, rows...)

	var widths []int
	for _, r := range all {
		for i, c := range r {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], lipgloss.Width(c.Text))
		}
	}
	lines := make([]Line, 0, len(all))
	for _, r := range all {
		l := make(Line, 0, 2*len(r))
		for i, c := range r {
			l = append(l, c)
			if i < len(r)-1 {
				l = append(l, Span{Text: strings.Repeat(" ", widths[i]-lipgloss.Width(c.Text)+2)})
			}
		}
		lines = append(lines, l)
	}
	return lines
}
