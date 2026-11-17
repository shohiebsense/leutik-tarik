package ui

import (
	"os"
	"testing"
	"time"
)

// The operator types wall-clock time in their own zone. What they typed must be
// what is queried: 09:15 in Jakarta is 02:15 UTC, not 09:15 UTC.
func TestParseTimeUsesDisplayZone(t *testing.T) {
	jakarta, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	old := displayLocation
	displayLocation = jakarta
	defer func() { displayLocation = old }()

	got, ok := parseTimeInDisplay("2026-11-18 09:15")
	if !ok {
		t.Fatal("did not parse")
	}
	want := time.Date(2026, 11, 18, 2, 15, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("parsed %v, want the same instant as %v", got.UTC(), want)
	}
	// Round trip: rendering it back shows the wall clock the user typed.
	if s := inDisplay(got).Format(inputTimeLayout); s != "2026-11-18 09:15" {
		t.Errorf("round trip = %q", s)
	}
}

func TestParseTimeAcceptsRFC3339WithOffset(t *testing.T) {
	got, ok := parseTimeInDisplay("2026-11-18T09:15:00+07:00")
	if !ok {
		t.Fatal("did not parse RFC3339")
	}
	if got.UTC().Hour() != 2 {
		t.Errorf("instant = %v, want 02:15Z", got.UTC())
	}
}

func TestZoneLabelShowsOffset(t *testing.T) {
	jakarta, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	if got := buildZoneLabel(jakarta); got != "Asia/Jakarta (UTC+07:00)" {
		t.Errorf("label = %q", got)
	}
}

// A bad zone name must not take the service down, and must not pretend to be
// the zone that was asked for.
func TestBadZoneFallsBackToUTC(t *testing.T) {
	os.Setenv("LEUTIK_TIMEZONE", "Not/AZone")
	defer os.Unsetenv("LEUTIK_TIMEZONE")
	loc := loadLocation()
	if loc.String() != "UTC" {
		t.Errorf("fell back to %q, want UTC", loc.String())
	}
}
