package ui

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Display location.
//
// Storage is always UTC (ClickHouse DateTime64 with an explicit UTC zone) and
// stays that way: a stored instant must not depend on who is looking. Only the
// presentation changes, so an operator in UTC+07 sees wall-clock times they
// recognise instead of doing arithmetic in their head.
//
// There is no JavaScript in this UI, so the browser's own zone cannot be read.
// The zone is therefore configured per deployment (LEUTIK_TIMEZONE, default
// Asia/Jakarta) and always labelled on the page, so a reader is never guessing
// which clock they are looking at.
var displayLocation = loadLocation()

// Zone name and current offset, e.g. "Asia/Jakarta (UTC+07:00)".
var displayZoneLabel = buildZoneLabel(displayLocation)

func loadLocation() *time.Location {
	name := os.Getenv("LEUTIK_TIMEZONE")
	if name == "" {
		name = "Asia/Jakarta"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		// Falling back to UTC silently would be worse than saying so: the label
		// below shows "UTC" and the page visibly disagrees with the operator.
		return time.UTC
	}
	return loc
}

// DisplayLocation returns the zone used for rendering.
func DisplayLocation() *time.Location { return displayLocation }

// ZoneLabel returns the human-readable zone, for the page header.
func ZoneLabel() string { return displayZoneLabel }

func buildZoneLabel(loc *time.Location) string {
	name := loc.String()
	_, offset := time.Now().In(loc).Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("%s (UTC%s%02d:%02d)", name, sign, offset/3600, (offset%3600)/60)
}

// inDisplay converts an instant to the display zone.
func inDisplay(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.In(displayLocation)
}

// parseTimeInDisplay reads a wall-clock string as the display zone, so what the
// user typed is what they meant. The returned time is the same instant, which is
// what the store compares against.
func parseTimeInDisplay(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	// Accept a trailing zone name as a convenience ("2026-11-18 09:15 WIB" is
	// not parseable, but "2026-11-18 09:15" is, and that is the common case).
	for _, layout := range []string{inputTimeLayout, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, displayLocation); err == nil {
			return t, true
		}
	}
	// Also accept RFC3339, which carries its own offset.
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	return time.Time{}, false
}
