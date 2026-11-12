package main

import (
	"testing"
	"time"
)

func TestParseInterval(t *testing.T) {
	valid := map[string]time.Duration{
		"":       0, // run once
		"30sec":  30 * time.Second,
		"5min":   5 * time.Minute,
		" 5MIN ": 5 * time.Minute,
		"30SEC":  30 * time.Second,
	}
	for in, want := range valid {
		got, err := parseInterval(in)
		if err != nil || got != want {
			t.Errorf("parseInterval(%q) = %v, %v; want %v", in, got, err, want)
		}
	}

	for _, in := range []string{"0", "0s", "1sec", "10min", "-30sec", "30", "abc"} {
		if _, err := parseInterval(in); err == nil {
			t.Errorf("parseInterval(%q): expected error", in)
		}
	}
}
