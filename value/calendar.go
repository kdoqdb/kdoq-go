package value

import (
	"fmt"
	"time"
)

// CalendarDate is a date without a time of day (years 0000 to 9999).
type CalendarDate struct {
	Year       int
	Month, Day uint8
}

// CalendarDateOf returns the calendar date t shows in its own location.
func CalendarDateOf(t time.Time) CalendarDate {
	y, m, d := t.Date()
	return CalendarDate{Year: y, Month: uint8(m), Day: uint8(d)}
}

// ToTime returns midnight of the date in loc (UTC when loc is nil).
func (c CalendarDate) ToTime(loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	return time.Date(c.Year, time.Month(c.Month), int(c.Day), 0, 0, 0, 0, loc)
}

// String returns the date as YYYY-MM-DD.
func (c CalendarDate) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", c.Year, c.Month, c.Day)
}

func (c CalendarDate) valid() bool {
	if c.Year < 0 || c.Year > 9999 || c.Month < 1 || c.Month > 12 || c.Day < 1 {
		return false
	}
	return time.Date(c.Year, time.Month(c.Month), int(c.Day), 0, 0, 0, 0, time.UTC).Day() == int(c.Day)
}
