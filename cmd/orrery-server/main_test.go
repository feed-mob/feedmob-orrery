package main

import "testing"

func TestDashboardURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{":8080", "http://localhost:8080/"},
		{"0.0.0.0:8080", "http://localhost:8080/"},
		{"127.0.0.1:9000", "http://127.0.0.1:9000/"},
		{"[::]:8080", "http://localhost:8080/"},
		{"orrery.internal:80", "http://orrery.internal:80/"},
	} {
		if got := dashboardURL(tc.in); got != tc.want {
			t.Errorf("dashboardURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
