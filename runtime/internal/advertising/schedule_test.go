package advertising

import (
	"testing"
	"time"
)

func TestSchedules(t *testing.T) {
	base := Schedule{Enabled: true, Start: "2026-01-31T14:00", Zone: "Asia/Kolkata", Duration: 2, Unit: "months", Repeat: "monthly", SlotMinutes: 60, ImageSeconds: 10}
	for _, tc := range []struct {
		at   string
		want bool
	}{{"2026-01-31T13:59", false}, {"2026-01-31T14:00", true}, {"2026-01-31T15:00", false}, {"2026-02-28T14:30", true}, {"2026-03-01T14:30", false}, {"2026-03-31T14:00", false}} {
		z, _ := time.LoadLocation(base.Zone)
		at, _ := time.ParseInLocation("2006-01-02T15:04", tc.at, z)
		if got := base.Active(at); got != tc.want {
			t.Fatalf("%s: %t", tc.at, got)
		}
	}
	base.Start = "2024-02-29T14:00"
	base.Duration = 3
	base.Unit = "years"
	base.Repeat = "yearly"
	z, _ := time.LoadLocation(base.Zone)
	at, _ := time.ParseInLocation("2006-01-02T15:04", "2025-02-28T14:30", z)
	if !base.Active(at) {
		t.Fatal("leap-day clamp")
	}
	base.Default = true
	if base.Active(at) {
		t.Fatal("default mixed into scheduled playlist")
	}
}
func TestWeeklyTimezoneAndInvalidSchedule(t *testing.T) {
	s := Schedule{Enabled: true, Start: "2026-10-19T14:00", Zone: "Asia/Kolkata", Duration: 1, Unit: "months", Repeat: "weekly", SlotMinutes: 60, ImageSeconds: 10}
	for _, tc := range []struct {
		at   string
		want bool
	}{{"2026-10-19T08:30:00Z", true}, {"2026-10-20T08:30:00Z", false}, {"2026-10-26T09:29:00Z", true}, {"2026-10-26T09:30:00Z", false}} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		if s.Active(at) != tc.want {
			t.Fatal(tc.at)
		}
	}
	s.Zone = "made/up"
	if _, _, e := s.Bounds(); e == nil {
		t.Fatal("invalid timezone")
	}
	s.Zone = "America/New_York"
	s.Start = "2026-03-08T02:30"
	if _, _, e := s.Bounds(); e == nil {
		t.Fatal("nonexistent DST time")
	}
}
func TestHourlyAndDaily(t *testing.T) {
	s := Schedule{Enabled: true, Start: "2026-10-19T10:15", Zone: "UTC", Duration: 1, Unit: "days", Repeat: "hourly", SlotMinutes: 10, ImageSeconds: 10}
	at := time.Date(2026, 10, 19, 11, 20, 0, 0, time.UTC)
	if !s.Active(at) || s.Active(at.Add(5*time.Minute)) {
		t.Fatal("hourly slot")
	}
	s.Repeat = "daily"
	s.Duration = 2
	if !s.Active(at.Add(23*time.Hour)) || s.Active(at) {
		t.Fatal("daily slot")
	}
}
