package advertising

import (
	"errors"
	"time"
	_ "time/tzdata"
)

// Start is wall-clock time in Zone. End is derived from campaign duration.
type Schedule struct {
	Enabled      bool   `json:"enabled"`
	Default      bool   `json:"default"`
	Start        string `json:"start"`
	Zone         string `json:"zone"`
	Duration     int    `json:"duration"`
	Unit         string `json:"unit"`
	Repeat       string `json:"repeat"`
	SlotMinutes  int    `json:"slotMinutes"`
	ImageSeconds int    `json:"imageSeconds"`
}

var ErrInvalid = errors.New("invalid advertisement or schedule")

func calendar(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	target := time.Date(y, m+time.Month(months), 1, t.Hour(), t.Minute(), t.Second(), 0, t.Location())
	last := time.Date(target.Year(), target.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
	if d > last {
		d = last
	}
	return time.Date(target.Year(), target.Month(), d, t.Hour(), t.Minute(), t.Second(), 0, t.Location())
}
func advance(t time.Time, n int, unit string) time.Time {
	switch unit {
	case "hours":
		return t.Add(time.Duration(n) * time.Hour)
	case "days":
		return t.AddDate(0, 0, n)
	case "weeks":
		return t.AddDate(0, 0, n*7)
	case "months":
		return calendar(t, n)
	case "years":
		return calendar(t, n*12)
	}
	return t
}
func (s Schedule) Bounds() (time.Time, time.Time, error) {
	z, e := time.LoadLocation(s.Zone)
	if e != nil {
		return time.Time{}, time.Time{}, ErrInvalid
	}
	start, e := time.ParseInLocation("2006-01-02T15:04", s.Start, z)
	if e != nil || start.Format("2006-01-02T15:04") != s.Start || s.Duration < 1 || s.Duration > 1000 || s.ImageSeconds < 3 || s.ImageSeconds > 300 {
		return start, start, ErrInvalid
	}
	switch s.Unit {
	case "hours", "days", "weeks", "months", "years":
	default:
		return start, start, ErrInvalid
	}
	switch s.Repeat {
	case "none", "hourly", "daily", "weekly", "monthly", "yearly":
	default:
		return start, start, ErrInvalid
	}
	if s.Repeat != "none" && (s.SlotMinutes < 1 || s.SlotMinutes > 525600) {
		return start, start, ErrInvalid
	}
	end := advance(start, s.Duration, s.Unit)
	if end.Year() > 9999 {
		return start, end, ErrInvalid
	}
	return start, end, nil
}
func (s Schedule) Active(now time.Time) bool {
	if !s.Enabled || s.Default {
		return false
	}
	start, end, e := s.Bounds()
	if e != nil || now.Before(start) || !now.Before(end) {
		return false
	}
	if s.Repeat == "none" {
		return true
	}
	now = now.In(start.Location())
	n := 0
	unit := "hours"
	day := func(t time.Time) int64 {
		y, m, d := t.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400
	}
	switch s.Repeat {
	case "hourly":
		n = int(now.Sub(start) / time.Hour)
	case "daily":
		unit = "days"
		n = int(day(now) - day(start))
	case "weekly":
		unit = "weeks"
		n = int(day(now)-day(start)) / 7
	case "monthly":
		unit = "months"
		n = (now.Year()-start.Year())*12 + int(now.Month()-start.Month())
	case "yearly":
		unit = "years"
		n = now.Year() - start.Year()
	}
	occurrence := advance(start, n, unit)
	if occurrence.After(now) {
		n--
		occurrence = advance(start, n, unit)
	}
	return n >= 0 && now.Before(occurrence.Add(time.Duration(s.SlotMinutes)*time.Minute))
}
