package ui

import (
	"fmt"
	"html/template"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/shohiebsense/leutik-tarik/internal/model"
)

// waterfall renders one trace as an SVG bar chart, server-side.
//
// No JavaScript: the geometry is computed here and emitted as plain SVG, with
// the table further down the page as the accessible equivalent.
type waterfall struct {
	// HTML is the rendered SVG, computed once per page render.
	HTML     template.HTML
	Width    int
	Rows     []wfRow
	Height   int
	Total    time.Duration
	Ticks    []wfTick
	Critical []string // span ids on the critical path
}

type wfRow struct {
	Span     model.Span
	Depth    int
	X        float64 // pixels
	W        float64
	Y        int
	Label    string
	Critical bool
	Color    string
}

type wfTick struct {
	X     float64
	Label string
	Major bool
}

const (
	wfLabelWidth = 240
	wfBarHeight  = 18
	wfRowGap     = 6
	wfRightPad   = 16
)

// buildWaterfall lays out spans as bars against a common time axis.
func buildWaterfall(spans []model.Span, width int) waterfall {
	if width <= 0 {
		width = 900
	}
	if len(spans) == 0 {
		return waterfall{Width: width}
	}

	start := spans[0].StartTime
	end := spans[0].EndTime
	for _, s := range spans {
		if s.StartTime.Before(start) {
			start = s.StartTime
		}
		if s.EndTime.After(end) {
			end = s.EndTime
		}
	}
	total := end.Sub(start)
	if total <= 0 {
		total = time.Microsecond // avoid divide-by-zero on a zero-width trace
	}

	plot := float64(width - wfLabelWidth - wfRightPad)
	wf := waterfall{Width: width, Total: total}
	wf.Critical = criticalPath(spans)
	critical := map[string]bool{}
	for _, id := range wf.Critical {
		critical[id] = true
	}

	byID := make(map[string]model.Span, len(spans))
	for _, s := range spans {
		byID[s.SpanID] = s
	}

	for i, s := range spans {
		offset := s.StartTime.Sub(start)
		x := wfLabelWidth + float64(offset)/float64(total)*plot
		w := float64(s.Duration) / float64(total) * plot
		if w < 1.5 {
			w = 1.5 // a zero-duration span must still be visible
		}
		wf.Rows = append(wf.Rows, wfRow{
			Span:     s,
			Depth:    depthOf(spans, s),
			X:        x,
			W:        w,
			Y:        10 + i*(wfBarHeight+wfRowGap),
			Label:    shortLabel(s),
			Critical: critical[s.SpanID],
			Color:    outcomeColor(s.Outcome),
		})
	}
	wf.Height = 20 + len(spans)*(wfBarHeight+wfRowGap)
	wf.Ticks = ticks(total, plot)
	wf.HTML = wf.svg()
	return wf
}

// criticalPath returns the chain of spans that determines the trace duration:
// starting at the longest root and repeatedly taking the child that ends last.
func criticalPath(spans []model.Span) []string {
	if len(spans) == 0 {
		return nil
	}
	children := map[string][]model.Span{}
	var roots []model.Span
	for _, s := range spans {
		if s.ParentSpanID == "" {
			roots = append(roots, s)
			continue
		}
		children[s.ParentSpanID] = append(children[s.ParentSpanID], s)
	}
	if len(roots) == 0 {
		return nil
	}
	cur := roots[0]
	for _, r := range roots {
		if r.Duration > cur.Duration {
			cur = r
		}
	}
	path := []string{cur.SpanID}
	for i := 0; i < 64; i++ { // bounded: a malformed trace must not loop
		kids := children[cur.SpanID]
		if len(kids) == 0 {
			break
		}
		sort.Slice(kids, func(a, b int) bool { return kids[a].EndTime.After(kids[b].EndTime) })
		next := kids[0]
		// Only follow a child that actually extends the parent's critical span.
		if next.Duration <= 0 {
			break
		}
		path = append(path, next.SpanID)
		cur = next
	}
	return path
}

// ticks places a few labelled gridlines without claiming spurious precision.
func ticks(total time.Duration, plot float64) []wfTick {
	n := 4
	out := make([]wfTick, 0, n+1)
	for i := 0; i <= n; i++ {
		frac := float64(i) / float64(n)
		d := time.Duration(float64(total) * frac)
		out = append(out, wfTick{
			X:     wfLabelWidth + frac*plot,
			Label: tickLabel(d),
			Major: i == 0 || i == n,
		})
	}
	return out
}

// tickLabel rounds to three significant figures: an axis that claims
// "525.000192ms" is noise, and nothing on this page is that precise.
func tickLabel(d time.Duration) string {
	switch {
	case d == 0:
		return "0"
	case d < time.Millisecond:
		return d.Round(time.Microsecond).String()
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < time.Minute:
		return d.Round(10 * time.Millisecond).String()
	default:
		return d.Round(time.Second).String()
	}
}

func outcomeColor(outcome string) string {
	switch outcome {
	case model.OutcomeError, model.OutcomeServerError:
		return "#b00020"
	case model.OutcomeClientError:
		return "#8a5b00"
	case model.OutcomeSuccess:
		return "#1a7f37"
	default:
		return "#5b6770"
	}
}

// shortLabel keeps the SVG readable; the table carries the full names.
func shortLabel(s model.Span) string {
	name := s.Name
	if s.HTTPRoute != "" {
		name = s.HTTPRoute
	}
	if len(name) > 34 {
		name = name[:33] + "…"
	}
	return name
}

// svg emits the chart. Uses only primitives (rect/line/text) so it renders
// identically everywhere and needs no client-side code.
func (wf waterfall) svg() template.HTML {
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" style="width:100%%;height:auto" role="img" aria-label="Span waterfall for this trace" xmlns="http://www.w3.org/2000/svg">`,
		wf.Width, wf.Height)
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%d" height="%d" fill="#fbfbfb"/>`, wf.Width, wf.Height)

	for _, t := range wf.Ticks {
		fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%d" stroke="#e3e3e3" stroke-width="1"/>`, t.X, t.X, wf.Height)
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="10" fill="#888">%s</text>`, t.X+2, 10, t.Label)
	}

	for _, r := range wf.Rows {
		// Critical-path bars get a dark outline so the chain is followable.
		stroke := "none"
		strokeWidth := 0
		if r.Critical {
			stroke = "#111"
			strokeWidth = 1
		}
		fmt.Fprintf(&b, `<text x="4" y="%d" font-size="11" fill="%s">%s</text>`,
			r.Y+13, textFill(r.Critical), escapeXML(padLeft(r.Label, r.Depth)))
		fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="%.1f" height="%d" rx="2" fill="%s" stroke="%s" stroke-width="%d"><title>%s · %s · %s</title></rect>`,
			r.X, r.Y, r.W, wfBarHeight, r.Color, stroke, strokeWidth,
			escapeXML(r.Span.Name), barDuration(r.Span.Duration), r.Span.ServiceName)
	}

	fmt.Fprintf(&b, `<text x="4" y="%d" font-size="10" fill="#666">outlined = critical path</text>`, wf.Height-2)
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// barDuration rounds for a tooltip; exact values live in the table.
func barDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return d.Round(time.Microsecond).String()
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	default:
		return d.Round(10 * time.Millisecond).String()
	}
}

func textFill(critical bool) string {
	if critical {
		return "#111"
	}
	return "#333"
}

func padLeft(s string, depth int) string {
	if depth <= 0 {
		return s
	}
	return strings.Repeat("  ", depth) + "└ " + s
}

// escapeXML protects the SVG from span names containing markup.
func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// pctWidth turns a ratio into a CSS width for inline bars in tables.
func pctWidth(frac float64) template.CSS {
	if math.IsNaN(frac) || frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	return template.CSS(fmt.Sprintf("width:%.1f%%", frac*100))
}

// orderedRows flattens the span tree depth-first from the roots, children in
// start order. Depth is the nesting level, so a query sits under the function
// that executed it rather than next to whichever call happened to begin first.
func orderedRows(spans []model.Span, traceStart time.Time) []traceRow {
	children := map[string][]model.Span{}
	var roots []model.Span
	byID := map[string]model.Span{}
	for _, s := range spans {
		byID[s.SpanID] = s
	}
	for _, s := range spans {
		if s.ParentSpanID == "" {
			roots = append(roots, s)
			continue
		}
		if _, ok := byID[s.ParentSpanID]; !ok {
			// Orphan: its parent is missing from this trace, so it is a root for
			// display purposes. Showing it beats hiding it.
			roots = append(roots, s)
			continue
		}
		children[s.ParentSpanID] = append(children[s.ParentSpanID], s)
	}
	sortSpans(roots)
	for id := range children {
		sortSpans(children[id])
	}

	rows := make([]traceRow, 0, len(spans))
	var walk func(s model.Span, depth int)
	walk = func(s model.Span, depth int) {
		rows = append(rows, traceRow{
			Span:   s,
			Offset: s.StartTime.Sub(traceStart),
			Depth:  depth,
		})
		for _, c := range children[s.SpanID] {
			if depth < 64 { // bounded: a malformed trace must not recurse forever
				walk(c, depth+1)
			}
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return rows
}

func sortSpans(spans []model.Span) {
	sort.SliceStable(spans, func(i, j int) bool {
		if !spans[i].StartTime.Equal(spans[j].StartTime) {
			return spans[i].StartTime.Before(spans[j].StartTime)
		}
		return spans[i].SpanID < spans[j].SpanID
	})
}
