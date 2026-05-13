package main

//go:generate go run github.com/matryer/moq@latest -out mock_rtt_test.go . rttAPI

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- pure function tests ---

func TestParseLeg(t *testing.T) {
	leg, err := parseLeg("A12345|20260511|SHF|MAN")
	require.NoError(t, err)
	assert.Equal(t, Leg{UID: "A12345", Date: "20260511", Origin: "SHF", Dest: "MAN"}, leg)

	_, err = parseLeg("A12345|20260511|SHF") // only 3 parts
	assert.Error(t, err)

	_, err = parseLeg("")
	assert.Error(t, err)
}

func TestParseLegs(t *testing.T) {
	legs, err := parseLegs("")
	require.NoError(t, err)
	assert.Empty(t, legs)

	legs, err = parseLegs("A1|20260511|SHF|MAN")
	require.NoError(t, err)
	assert.Len(t, legs, 1)

	legs, err = parseLegs("A1|20260511|SHF|MAN,B2|20260511|MAN|LDS")
	require.NoError(t, err)
	assert.Len(t, legs, 2)
	assert.Equal(t, "B2", legs[1].UID)

	_, err = parseLegs("bad")
	assert.Error(t, err)
}

func TestEncodeLegRoundtrip(t *testing.T) {
	leg := Leg{UID: "X99", Date: "20260511", Origin: "SHF", Dest: "LDS"}
	encoded := encodeLeg(leg)
	assert.Equal(t, "X99|20260511|SHF|LDS", encoded)

	decoded, err := parseLeg(encoded)
	require.NoError(t, err)
	assert.Equal(t, leg, decoded)
}

func TestEncodeLegsRoundtrip(t *testing.T) {
	legs := []Leg{
		{UID: "A1", Date: "20260511", Origin: "SHF", Dest: "MAN"},
		{UID: "B2", Date: "20260511", Origin: "MAN", Dest: "LDS"},
	}
	encoded := encodeLegs(legs)
	assert.Equal(t, "A1|20260511|SHF|MAN,B2|20260511|MAN|LDS", encoded)

	decoded, err := parseLegs(encoded)
	require.NoError(t, err)
	assert.Equal(t, legs, decoded)
}

func TestFmtTime(t *testing.T) {
	assert.Equal(t, "09:00", fmtTime("0900"))
	assert.Equal(t, "23:59", fmtTime("2359"))
	assert.Equal(t, "09:00", fmtTime("09:00")) // already formatted
	assert.Equal(t, "", fmtTime(""))
	assert.Equal(t, "abc", fmtTime("abc")) // passthrough when not 4 chars
}

func TestHhmm2mins(t *testing.T) {
	assert.Equal(t, 0, hhmm2mins("0000"))
	assert.Equal(t, 540, hhmm2mins("0900"))
	assert.Equal(t, 1439, hhmm2mins("2359"))
	assert.Equal(t, 540, hhmm2mins("09:00")) // colon form
	assert.Equal(t, -1, hhmm2mins(""))
	assert.Equal(t, -1, hhmm2mins("90"))
	assert.Equal(t, -1, hhmm2mins("abcd"))
}

func TestDelayMins(t *testing.T) {
	assert.Equal(t, 0, delayMins("0900", "0900"))
	assert.Equal(t, 5, delayMins("0900", "0905"))
	assert.Equal(t, 0, delayMins("0900", "0855")) // early arrival → 0
	assert.Equal(t, 0, delayMins("", "0900"))
	assert.Equal(t, 0, delayMins("0900", ""))
}

func TestStatusLabel(t *testing.T) {
	assert.Equal(t, "cancelled", statusLabel("CANCELLED_CALL", 0))
	assert.Equal(t, "cancelled", statusLabel("CANCELLED_CALL", 10))
	assert.Equal(t, "delayed", statusLabel("CALL", 5))
	assert.Equal(t, "on-time", statusLabel("CALL", 0))
}

func TestParseDate(t *testing.T) {
	assert.Equal(t, "20260511", parseDate("20260511"))
	assert.Equal(t, "20260511", parseDate("2026-05-11"))
	// invalid → today (just check it's 8 digits)
	result := parseDate("bad")
	assert.Len(t, result, 8)
}

// --- HTTP handler tests ---

func newTestServer(t *testing.T, mock rttAPI) *server {
	t.Helper()
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"nowTime":  func() string { return "09:00" },
		"basePath": func() string { return "" },
		"stationsJSON": func(ss []Station) template.JS {
			b, _ := json.Marshal(ss)
			return template.JS(b)
		},
	}).ParseGlob("templates/*.html")
	require.NoError(t, err)
	return &server{rtt: mock, tmpl: tmpl}
}

func TestHandleDepartures_MissingOrigin(t *testing.T) {
	srv := newTestServer(t, &rttAPIMock{})
	r := httptest.NewRequest(http.MethodGet, "/departures", nil)
	w := httptest.NewRecorder()
	srv.handleDepartures(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleDepartures_EmptyResults(t *testing.T) {
	mock := &rttAPIMock{
		SearchDeparturesFunc: func(crs, date, fromTime string) (*SearchResponse, error) {
			return &SearchResponse{}, nil
		},
	}
	srv := newTestServer(t, mock)
	r := httptest.NewRequest(http.MethodGet, "/departures?origin=SHF&date=20260511&time=0900", nil)
	w := httptest.NewRecorder()
	srv.handleDepartures(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, len(mock.SearchDeparturesCalls()))
}

func TestHandleCallingPoints(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return &ServiceResponse{
				ScheduleMeta: ServiceScheduleMeta{
					TrainReportingIdentity: "1A23",
					Operator:               Operator{Name: "Test Rail"},
				},
				Locations: []ServiceLocation{},
			}, nil
		},
	}
	srv := newTestServer(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/calling-points/A12345/20260511?originCRS=SHF", nil)
	req = mux.SetURLVars(req, map[string]string{"uid": "A12345", "date": "20260511"})
	w := httptest.NewRecorder()
	srv.handleCallingPoints(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleLeg_InvalidIndex(t *testing.T) {
	srv := newTestServer(t, &rttAPIMock{})

	// non-numeric n
	req := httptest.NewRequest(http.MethodGet, "/leg/xyz?legs=A1|20260511|SHF|MAN", nil)
	req = mux.SetURLVars(req, map[string]string{"n": "xyz"})
	w := httptest.NewRecorder()
	srv.handleLeg(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleLeg_OutOfRange(t *testing.T) {
	srv := newTestServer(t, &rttAPIMock{})

	req := httptest.NewRequest(http.MethodGet, "/leg/5?legs=A1|20260511|SHF|MAN", nil)
	req = mux.SetURLVars(req, map[string]string{"n": "5"})
	w := httptest.NewRecorder()
	srv.handleLeg(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleLeg_Valid(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return &ServiceResponse{
				ScheduleMeta: ServiceScheduleMeta{
					TrainReportingIdentity: "1A23",
					Operator:               Operator{Name: "Test Rail"},
				},
				Locations: []ServiceLocation{},
			}, nil
		},
	}
	srv := newTestServer(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/leg/0?legs=A1|20260511|SHF|MAN", nil)
	req = mux.SetURLVars(req, map[string]string{"n": "0"})
	w := httptest.NewRecorder()
	srv.handleLeg(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}
