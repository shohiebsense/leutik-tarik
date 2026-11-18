package ui

import (
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/pprof/profile"

	"github.com/shohiebsense/leutik-tarik/internal/store"
)

// Profiles lists stored profile artefacts.
func (u UI) Profiles(w http.ResponseWriter, r *http.Request) {
	ps, ok := u.Store.(store.Profiles)
	if !ok {
		http.Error(w, "this store cannot hold profiles", http.StatusNotImplemented)
		return
	}
	list, err := ps.ListProfiles(r.Context(), 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	render(w, r, profilesView, map[string]any{
		"Title":    "Profiles",
		"Profiles": list,
		"Dir":      store.ProfilesDir(),
	})
}

// ProfileDetail renders one profile as a flamegraph (an icicle: depth by row,
// width by cost), server-side.
func (u UI) ProfileDetail(w http.ResponseWriter, r *http.Request) {
	ps, ok := u.Store.(store.Profiles)
	if !ok {
		http.Error(w, "this store cannot hold profiles", http.StatusNotImplemented)
		return
	}
	id := r.PathValue("profileID")
	meta, data, err := ps.GetProfile(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	fg, err := buildFlamegraph(data, 1100)
	if err != nil {
		// A stored blob that cannot be parsed is shown, not hidden.
		render(w, r, profileDetailView, map[string]any{
			"Title": "Profile " + id, "Meta": meta,
			"Error": err.Error(), "Flame": flamegraph{},
		})
		return
	}
	render(w, r, profileDetailView, map[string]any{
		"Title": "Profile " + id, "Meta": meta, "Flame": fg,
	})
}

// flamegraph is a simple icicle layout of a pprof profile.
type flamegraph struct {
	// Top ranks functions by self time: the actionable ordering.
	Top       []store.TopFunction
	HTML      template.HTML
	Width     int
	Height    int
	Total     int64
	Unit      string
	Frames    int
	Truncated bool
}

type fgNode struct {
	name     string
	value    int64
	depth    int
	x        float64
	w        float64
	children []*fgNode
}

const (
	fgRowHeight = 18
	fgMaxDepth  = 40 // bounded: a pathological profile must not explode the page
)

// buildFlamegraph parses a profile and lays out frames as an icicle chart.
func buildFlamegraph(data []byte, width int) (flamegraph, error) {
	prof, err := profile.ParseData(data)
	if err != nil {
		return flamegraph{}, fmt.Errorf("parse pprof: %w", err)
	}
	if len(prof.Sample) == 0 {
		return flamegraph{}, fmt.Errorf("profile has no samples")
	}
	if width <= 0 {
		width = 1100
	}

	root := &fgNode{name: "all"}
	truncated := false
	var total int64
	for _, s := range prof.Sample {
		v := s.Value[sampleIndex(prof)]
		total += v
		frames := stackNames(s)
		if len(frames) > fgMaxDepth {
			frames = frames[:fgMaxDepth]
			truncated = true
		}
		cur := root
		for _, name := range frames {
			cur = childNamed(cur, name)
		}
		cur.value += v
	}

	// Self time: the leaf frame is where the sample was actually taking time,
	// so ranking by it points at the function to fix rather than its callers.
	self := map[string]int64{}
	cum := map[string]int64{}
	for _, s := range prof.Sample {
		v := s.Value[sampleIndex(prof)]
		leaf := leafName(s)
		if leaf != "" {
			self[leaf] += v
		}
		seen := map[string]struct{}{}
		for _, f := range stackNames(s) {
			if _, dup := seen[f]; dup {
				continue
			}
			seen[f] = struct{}{}
			cum[f] += v
		}
	}
	top := make([]store.TopFunction, 0, len(self))
	for name, v := range self {
		tf := store.TopFunction{Name: name, Self: v, Cum: cum[name]}
		if total > 0 {
			tf.SelfPct = float64(v) / float64(total) * 100
		}
		top = append(top, tf)
	}
	sort.Slice(top, func(i, j int) bool { return top[i].Self > top[j].Self })
	if len(top) > 25 {
		top = top[:25]
	}

	var measure func(n *fgNode, depth int) int64
	maxDepth := 0
	measure = func(n *fgNode, depth int) int64 {
		n.depth = depth
		if depth > maxDepth {
			maxDepth = depth
		}
		// Leaf keeps its own value; parents are the sum of children.
		var sum int64
		for _, c := range n.children {
			sum += measure(c, depth+1)
		}
		if sum > n.value {
			n.value = sum
		}
		return n.value
	}
	measure(root, 0)

	// Width by value, laid out left to right.
	var layout func(n *fgNode, x float64)
	layout = func(n *fgNode, x float64) {
		n.x = x
		if n.value <= 0 || total <= 0 {
			return
		}
		n.w = float64(n.value) / float64(total) * float64(width)
		sort.Slice(n.children, func(i, j int) bool { return n.children[i].value > n.children[j].value })
		cx := x
		for _, c := range n.children {
			layout(c, cx)
			cx += c.w
		}
	}
	layout(root, 0)

	height := (maxDepth + 1) * fgRowHeight
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" style="width:100%%;height:auto" role="img" aria-label="Flamegraph of this profile" xmlns="http://www.w3.org/2000/svg">`,
		width, height)
	b.WriteString(`<rect width="100%" height="100%" fill="#fbfbfb"/>`)

	frames := 0
	var walk func(n *fgNode)
	walk = func(n *fgNode) {
		if n.depth > 0 && n.w > 0.4 { // skip invisible slivers rather than emit noise
			frames++
			fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="%.1f" height="%d" fill="%s"><title>%s — %s</title></rect>`,
				n.x, n.depth*fgRowHeight, n.w, fgRowHeight-1,
				depthColor(n.depth), escapeXML(n.name), policyDuration(n.value, prof))
			if n.w > 60 {
				fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="10" fill="#fff" pointer-events="none">%s</text>`,
					n.x+3, n.depth*fgRowHeight+12, escapeXML(shorten(n.name, 24)))
			}
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(root)
	b.WriteString(`</svg>`)

	return flamegraph{
		Top:       top,
		HTML:      template.HTML(b.String()),
		Width:     width,
		Height:    height,
		Total:     total,
		Unit:      defaultTypeName(prof),
		Frames:    frames,
		Truncated: truncated,
	}, nil
}

func childNamed(n *fgNode, name string) *fgNode {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	c := &fgNode{name: name}
	n.children = append(n.children, c)
	return c
}

// stackNames returns the leaf-first frames reversed, so the root is first.
//
// Inlining is kept: a frame the compiler inlined still cost time, and hiding it
// would misattribute the cost to its caller.
func stackNames(s *profile.Sample) []string {
	var names []string
	for _, loc := range s.Location {
		name := ""
		for _, line := range loc.Line {
			if line.Function != nil && line.Function.Name != "" {
				name = cleanFuncName(line.Function.Name)
				break
			}
		}
		if name == "" {
			name = fmt.Sprintf("0x%x", loc.Address)
		}
		if name == "" {
			continue
		}
		names = append([]string{name}, names...)
	}
	return names
}

// cleanFuncName trims the package path so a bar can show something readable.
func cleanFuncName(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return name
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// depthColor gives each depth a distinct step, not a rainbow.
func depthColor(depth int) string {
	palette := []string{"#5b6770", "#6b7b8c", "#4f7185", "#3f6b7d", "#35606f", "#2d5562", "#264a55"}
	return palette[depth%len(palette)]
}

func defaultTypeName(p *profile.Profile) string {
	if p.DefaultSampleType != "" {
		return p.DefaultSampleType
	}
	return "samples"
}

func policyDuration(v int64, prof *profile.Profile) string {
	unit := "samples"
	if i := sampleIndex(prof); i < len(prof.SampleType) {
		unit = prof.SampleType[i].Unit
	}
	switch unit {
	case "nanoseconds":
		return fmt.Sprintf("%.1fms", float64(v)/1e6)
	case "count", "samples":
		return fmt.Sprintf("%d samples", v)
	default:
		return fmt.Sprintf("%d %s", v, unit)
	}
}

// sampleIndex picks the value column to chart: the profile's default sample
// type when it matches, otherwise the last column (sample counts for CPU).
// leafName is the innermost frame of a sample.
func leafName(s *profile.Sample) string {
	if len(s.Location) == 0 {
		return ""
	}
	loc := s.Location[0]
	for _, line := range loc.Line {
		if line.Function != nil && line.Function.Name != "" {
			return cleanFuncName(line.Function.Name)
		}
	}
	return fmt.Sprintf("0x%x", loc.Address)
}

func sampleIndex(p *profile.Profile) int {
	// DefaultSampleType is a sample-type NAME in modern pprof (empty when the
	// profile does not name one).
	if def := p.DefaultSampleType; def != "" {
		for i, st := range p.SampleType {
			if st.Type == def {
				return i
			}
		}
	}
	if len(p.SampleType) == 0 {
		return 0
	}
	return len(p.SampleType) - 1
}

// nsDuration renders a nanosecond sample value as a duration.
func nsDuration(v int64) string {
	d := time.Duration(v)
	switch {
	case d < time.Millisecond:
		return d.Round(time.Microsecond).String()
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	default:
		return d.Round(10 * time.Millisecond).String()
	}
}
