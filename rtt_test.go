package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsoToHHMM(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"2026-05-11T09:00:00", "0900"},
		{"2026-05-11T23:59:00", "2359"},
		{"2026-05-11T09:05:00", "0905"},
		{"", ""},
		{"short", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, isoToHHMM(tt.in), "isoToHHMM(%q)", tt.in)
	}
}

func TestYmdToDash(t *testing.T) {
	assert.Equal(t, "2026-05-11", ymdToDash("20260511"))
	assert.Equal(t, "2026-01-01", ymdToDash("20260101"))
	// passthrough when not 8 chars
	assert.Equal(t, "2026-05-1", ymdToDash("2026-05-1"))
	assert.Equal(t, "", ymdToDash(""))
}

func TestRunDateToYMD(t *testing.T) {
	assert.Equal(t, "20260511", runDateToYMD("2026-05-11"))
	assert.Equal(t, "20260101", runDateToYMD("2026-01-01"))
	// passthrough when not 10 chars
	assert.Equal(t, "20260511", runDateToYMD("20260511"))
	assert.Equal(t, "", runDateToYMD(""))
}

func TestYmdHHMMtoISO(t *testing.T) {
	assert.Equal(t, "2026-05-11T09:00:00", ymdHHMMtoISO("20260511", "0900"))
	assert.Equal(t, "2026-05-11T23:59:00", ymdHHMMtoISO("20260511", "2359"))
	// empty hhmm falls back to current time — just check prefix
	result := ymdHHMMtoISO("20260511", "")
	assert.Contains(t, result, "2026-05-11T")
}
