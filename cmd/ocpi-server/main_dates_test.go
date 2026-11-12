package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad test time %q: %v", s, err)
	}
	return v
}

func TestReadDateRange(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		wantOK   bool
		wantFrom string // "" means zero time
		wantTo   string
	}{
		{"no params", "", true, "", ""},
		{"from only", "date_from=2025-01-01T00:00:00Z", true, "2025-01-01T00:00:00Z", ""},
		{"to only", "date_to=2025-01-01T23:59:59Z", true, "", "2025-01-01T23:59:59Z"},
		{"both", "date_from=2025-01-01T00:00:00Z&date_to=2025-01-02T00:00:00Z", true, "2025-01-01T00:00:00Z", "2025-01-02T00:00:00Z"},
		{"empty values are ignored", "date_from=&date_to=", true, "", ""},
		{"offset form (+ is %2B)", "date_from=2025-01-01T05:30:00%2B05:30", true, "2025-01-01T00:00:00Z", ""},
		{"garbage from", "date_from=not-a-date", false, "", ""},
		{"garbage to", "date_to=not-a-date", false, "", ""},
		{"valid from, garbage to", "date_from=2025-01-01T00:00:00Z&date_to=nope", false, "", ""},
		{"date only is rejected", "date_from=2025-01-01", false, "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations?"+tc.query, nil)
			from, to, ok := readDateRange(req)

			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				if !from.IsZero() || !to.IsZero() {
					t.Fatalf("on failure both times must be zero; got %v, %v", from, to)
				}
				return
			}

			wantFrom, wantTo := time.Time{}, time.Time{}
			if tc.wantFrom != "" {
				wantFrom = mustTime(t, tc.wantFrom)
			}
			if tc.wantTo != "" {
				wantTo = mustTime(t, tc.wantTo)
			}
			if !from.Equal(wantFrom) {
				t.Errorf("from = %v, want %v", from, wantFrom)
			}
			if !to.Equal(wantTo) {
				t.Errorf("to = %v, want %v", to, wantTo)
			}
		})
	}
}

func TestWithinDateRange(t *testing.T) {
	from := mustTime(t, "2025-01-01T00:00:00Z")
	to := mustTime(t, "2025-01-01T23:59:59Z")
	var zero time.Time

	tests := []struct {
		name        string
		lastUpdated string
		from, to    time.Time
		want        bool
	}{
		{"no filter, valid stamp", "2025-06-01T00:00:00Z", zero, zero, true},
		{"no filter, garbage stamp still passes", "garbage", zero, zero, true},
		{"no filter, empty stamp still passes", "", zero, zero, true},

		{"from only, before", "2024-12-31T23:59:59Z", from, zero, false},
		{"from only, equal (inclusive)", "2025-01-01T00:00:00Z", from, zero, true},
		{"from only, after", "2030-01-01T00:00:00Z", from, zero, true},

		{"to only, before", "2020-01-01T00:00:00Z", zero, to, true},
		{"to only, equal (inclusive)", "2025-01-01T23:59:59Z", zero, to, true},
		{"to only, after", "2025-01-02T00:00:00Z", zero, to, false},

		{"both, inside", "2025-01-01T12:00:00Z", from, to, true},
		{"both, before", "2024-12-31T23:59:59Z", from, to, false},
		{"both, after", "2025-01-02T00:00:00Z", from, to, false},

		{"filter active, garbage stamp excluded", "garbage", from, to, false},
		{"filter active, empty stamp excluded", "", from, to, false},

		// 2025-01-01T05:30:00+05:30 is 2025-01-01T00:00:00Z, so it equals from.
		{"offset stamp compares as an instant", "2025-01-01T05:30:00+05:30", from, to, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := withinDateRange(tc.lastUpdated, tc.from, tc.to)
			if got != tc.want {
				t.Errorf("withinDateRange(%q) = %v, want %v", tc.lastUpdated, got, tc.want)
			}
		})
	}
}
