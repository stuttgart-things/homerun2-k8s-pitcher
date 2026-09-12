package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseStartupTimeout(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"unset uses default", "", DefaultStartupTimeout, false},
		{"whitespace uses default", "  ", DefaultStartupTimeout, false},
		{"seconds", "90s", 90 * time.Second, false},
		{"minutes", "5m", 5 * time.Minute, false},
		{"bare number is not a duration", "120", 0, true},
		{"garbage", "soon", 0, true},
		{"zero", "0s", 0, true},
		{"negative", "-10s", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseStartupTimeout(PitcherStartupTimeoutEnv, tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseStartupTimeout(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), PitcherStartupTimeoutEnv) {
				t.Errorf("error %q does not name %s", err, PitcherStartupTimeoutEnv)
			}
			if got != tc.want {
				t.Errorf("ParseStartupTimeout(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
