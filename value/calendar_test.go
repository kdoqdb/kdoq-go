package value_test

import (
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go/value"
)

func TestCalendarDateHasNoTimeZoneShift(t *testing.T) {
	tokyo := time.FixedZone("UTC+9", 9*3600)
	honolulu := time.FixedZone("UTC-10", -10*3600)
	for _, loc := range []*time.Location{time.UTC, tokyo, honolulu} {
		late := time.Date(2026, 11, 1, 23, 59, 59, 0, loc)
		early := time.Date(2026, 11, 2, 0, 0, 1, 0, loc)
		if got := value.CalendarDateOf(late); got != (value.CalendarDate{Year: 2026, Month: 11, Day: 1}) {
			t.Errorf("%s: late evening is %v", loc, got)
		}
		if got := value.CalendarDateOf(early); got != (value.CalendarDate{Year: 2026, Month: 11, Day: 2}) {
			t.Errorf("%s: just after midnight is %v", loc, got)
		}
		day := value.CalendarDate{Year: 2026, Month: 11, Day: 1}
		if got, want := day.ToTime(loc), time.Date(2026, 11, 1, 0, 0, 0, 0, loc); !got.Equal(want) || got.Location() != loc {
			t.Errorf("%s: ToTime = %v, want %v", loc, got, want)
		}
	}
	if got := (value.CalendarDate{Year: 1970, Month: 1, Day: 1}).ToTime(nil); !got.Equal(time.Unix(0, 0)) {
		t.Errorf("nil location is not UTC: %v", got)
	}
}

func TestCalendarDateRoundTripsTheRange(t *testing.T) {
	for _, day := range []value.CalendarDate{
		{Year: 0, Month: 1, Day: 1},
		{Year: 1970, Month: 1, Day: 1},
		{Year: 2024, Month: 2, Day: 29},
		{Year: 9999, Month: 12, Day: 31},
	} {
		if got := value.CalendarDateOf(day.ToTime(time.UTC)); got != day {
			t.Errorf("%v round-tripped to %v", day, got)
		}
	}
	if got := (value.CalendarDate{Year: 7, Month: 3, Day: 9}).String(); got != "0007-03-09" {
		t.Errorf("String() = %q", got)
	}
}
