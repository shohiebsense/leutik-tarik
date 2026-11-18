package ingest

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// noiseSpans are instrumentation-internal spans that add rows without adding
// information. pgx emits pool.acquire for every query, so a trace with 30
// queries carries 30 rows that only say "a connection was taken from the pool".
// The wait is already visible as the gap before the query span.
var noiseSpans = map[string]bool{
	"pool.acquire":      true,
	"pool.release":      true,
	"pool.acquire.wait": true,
	"pool.empty":        true,
}

// isNoise reports a span that should not be stored.
func isNoise(name string) bool {
	return noiseSpans[strings.ToLower(strings.TrimSpace(name))]
}

// genericDBNames are what SQL instrumentation calls a span when it has nothing
// better: the bare verb. A trace full of "SELECT" tells you nothing about which
// query ran.
var genericDBNames = map[string]bool{
	"select": true, "insert": true, "update": true, "delete": true,
	"with": true, "begin": true, "commit": true, "rollback": true,
	"exec": true, "exec_context": true, "query": true, "query_row": true,
	"batch": true, "prepare": true,
}

var tablePattern = regexp.MustCompile(`(?i)\b(?:from|join|into|update|table)\s+("[^"]+"|[a-z0-9_."]+)`)

// descriptiveName turns "SELECT" into something a reader can act on:
// "SELECT customers" from the statement's first table, or db.query.summary when
// the instrumentation provides one. The original name is kept as an attribute by
// the caller, so nothing is lost.
func descriptiveName(spanName string, attrs map[string]string) string {
	if !genericDBNames[strings.ToLower(strings.TrimSpace(spanName))] {
		return spanName
	}
	if summary := strings.TrimSpace(attrs["db.query.summary"]); summary != "" {
		return truncateName(summary)
	}
	text := attrs["db.query.text"]
	if text == "" {
		// Older instrumentation puts the statement here.
		text = attrs["db.statement"]
	}
	if m := tablePattern.FindStringSubmatch(text); len(m) > 1 {
		return truncateName(strings.ToUpper(spanName) + " " + cleanTable(m[1]))
	}
	return spanName
}

// cleanTable strips quotes and any schema prefix so the label stays short.
func cleanTable(t string) string {
	t = strings.Trim(t, `"`)
	t = strings.TrimSpace(t)
	if i := strings.LastIndex(t, "."); i >= 0 && i+1 < len(t) {
		t = t[i+1:]
	}
	return strings.Trim(t, `"`)
}

// truncateName keeps a label readable at a glance; an ellipsis is honest about
// what was cut, unlike a silently shortened string. The budget is bytes, and the
// ellipsis itself costs three, so cut three short rather than one.
func truncateName(s string) string {
	const max = 64
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	cut := max - 3
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
