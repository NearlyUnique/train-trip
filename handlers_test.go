package main

//go:generate go run github.com/matryer/moq@latest -out mock_rtt_test.go . rttAPI

import (
	"encoding/json"
	"errors"
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

func TestNowHHMM(t *testing.T) {
	result := nowHHMM()
	assert.Len(t, result, 4)
	h, m := result[:2], result[2:]
	assert.NotEmpty(t, h)
	assert.NotEmpty(t, m)
}

func TestParseDate(t *testing.T) {
	assert.Equal(t, "20260511", parseDate("20260511"))
	assert.Equal(t, "20260511", parseDate("2026-05-11"))
	// invalid → today (just check it's 8 digits)
	result := parseDate("bad")
	assert.Len(t, result, 8)
}

func TestStationByName(t *testing.T) {
	crs, ok := stationByName("Ely") // unique name → deterministic result
	assert.True(t, ok)
	assert.Equal(t, "ELY", crs)

	_, ok = stationByName("Nonexistent City")
	assert.False(t, ok)

	crs, ok = stationByName("ELY") // case-insensitive
	assert.True(t, ok)
	assert.Equal(t, "ELY", crs)
}

func TestStationName(t *testing.T) {
	assert.Equal(t, "Sheffield", stationName("SHF"))
	assert.Equal(t, "ZZZ", stationName("ZZZ")) // unknown → returns CRS as fallback
}

// --- test helpers ---

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

// iso builds an ISO 8601 datetime from "HHMM".
func iso(hhmm string) string {
	return "2026-05-11T" + hhmm[:2] + ":" + hhmm[2:] + ":00"
}

// svcLoc builds a ServiceLocation with the given CRS and optional departure/arrival times (HHMM).
func svcLoc(crs, depBooked, depRT, arrBooked, arrRT string) ServiceLocation {
	loc := ServiceLocation{
		Location:     StopLocation{ShortCodes: []string{crs}},
		TemporalData: ServiceTemporalData{DisplayAs: "CALL"},
	}
	if depBooked != "" {
		loc.TemporalData.Departure = &TemporalPoint{
			ScheduleAdvertised: iso(depBooked),
			RealtimeForecast:   iso(depRT),
		}
	}
	if arrBooked != "" {
		loc.TemporalData.Arrival = &TemporalPoint{
			ScheduleAdvertised: iso(arrBooked),
			RealtimeForecast:   iso(arrRT),
		}
	}
	return loc
}

// testSvcResp builds a ServiceResponse with origin departure and destination arrival.
func testSvcResp(originCRS, destCRS, depBooked, depRT, arrBooked, arrRT string) *ServiceResponse {
	return &ServiceResponse{
		ScheduleMeta: ServiceScheduleMeta{
			TrainReportingIdentity: "1A23",
			Operator:               Operator{Name: "Test Rail"},
		},
		Locations: []ServiceLocation{
			svcLoc(originCRS, depBooked, depRT, "", ""),
			svcLoc(destCRS, "", "", arrBooked, arrRT),
		},
	}
}

// passengerDep builds a minimal passenger Service with a departure time for SearchResponse use.
func passengerDep(uid, depRT string) Service {
	return Service{
		ScheduleMeta: ServiceScheduleMeta{
			Identity:               uid,
			InPassengerService:     true,
			TrainReportingIdentity: uid,
			DepartureDate:          "2026-05-11",
		},
		TemporalData: ServiceTemporalData{
			DisplayAs: "CALL",
			Departure: &TemporalPoint{
				ScheduleAdvertised: iso(depRT),
				RealtimeForecast:   iso(depRT),
			},
		},
	}
}

// --- handleIndex ---

func TestHandleIndex(t *testing.T) {
	srv := newTestServer(t, &rttAPIMock{})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.handleIndex(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Train Trip")
}

// --- handleDepartures ---

func TestHandleDepartures_MissingOrigin(t *testing.T) {
	srv := newTestServer(t, &rttAPIMock{})
	r := httptest.NewRequest(http.MethodGet, "/departures", nil)
	w := httptest.NewRecorder()
	srv.handleDepartures(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleDepartures_APIError(t *testing.T) {
	mock := &rttAPIMock{
		SearchDeparturesFunc: func(crs, date, fromTime string) (*SearchResponse, error) {
			return nil, errors.New("api down")
		},
	}
	srv := newTestServer(t, mock)
	r := httptest.NewRequest(http.MethodGet, "/departures?origin=SHF&date=20260511&time=0900", nil)
	w := httptest.NewRecorder()
	srv.handleDepartures(w, r)
	assert.Equal(t, http.StatusBadGateway, w.Code)
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

func TestHandleDepartures_ServiceFiltering(t *testing.T) {
	services := []Service{
		// non-passenger → filtered
		{ScheduleMeta: ServiceScheduleMeta{InPassengerService: false, TrainReportingIdentity: "NONPASS"},
			TemporalData: ServiceTemporalData{DisplayAs: "CALL", Departure: &TemporalPoint{ScheduleAdvertised: iso("0900"), RealtimeForecast: iso("0900")}}},
		// cancelled displayAs → filtered
		{ScheduleMeta: ServiceScheduleMeta{InPassengerService: true, TrainReportingIdentity: "CANCAS"},
			TemporalData: ServiceTemporalData{DisplayAs: "CANCELLED_CALL", Departure: &TemporalPoint{ScheduleAdvertised: iso("0900"), RealtimeForecast: iso("0900")}}},
		// nil departure → filtered
		{ScheduleMeta: ServiceScheduleMeta{InPassengerService: true, TrainReportingIdentity: "NILDEP"},
			TemporalData: ServiceTemporalData{DisplayAs: "CALL"}},
		// departure.IsCancelled → filtered
		{ScheduleMeta: ServiceScheduleMeta{InPassengerService: true, TrainReportingIdentity: "CANCDEP"},
			TemporalData: ServiceTemporalData{DisplayAs: "CALL", Departure: &TemporalPoint{ScheduleAdvertised: iso("0900"), RealtimeForecast: iso("0900"), IsCancelled: true}}},
		// too early (dep "0700" < minTime "0800") → filtered
		{ScheduleMeta: ServiceScheduleMeta{InPassengerService: true, TrainReportingIdentity: "EARLY", DepartureDate: "2026-05-11"},
			TemporalData: ServiceTemporalData{DisplayAs: "CALL", Departure: &TemporalPoint{ScheduleAdvertised: iso("0700"), RealtimeForecast: iso("0700")}}},
		// valid on-time service → included
		{ScheduleMeta: ServiceScheduleMeta{InPassengerService: true, TrainReportingIdentity: "ONTIME", Identity: "V1", DepartureDate: "2026-05-11"},
			TemporalData: ServiceTemporalData{DisplayAs: "CALL", Departure: &TemporalPoint{ScheduleAdvertised: iso("0900"), RealtimeForecast: iso("0900")}},
			Destination:  []StationStop{{Location: StopLocation{Description: "Manchester"}}}},
		// valid delayed service → included
		{ScheduleMeta: ServiceScheduleMeta{InPassengerService: true, TrainReportingIdentity: "DELAYED", Identity: "V2", DepartureDate: "2026-05-11"},
			TemporalData: ServiceTemporalData{DisplayAs: "CALL", Departure: &TemporalPoint{ScheduleAdvertised: iso("0900"), RealtimeForecast: iso("0905")}},
			Destination:  []StationStop{{Location: StopLocation{Description: "Leeds"}}}},
	}
	mock := &rttAPIMock{
		SearchDeparturesFunc: func(crs, date, fromTime string) (*SearchResponse, error) {
			return &SearchResponse{Services: services}, nil
		},
	}
	srv := newTestServer(t, mock)
	r := httptest.NewRequest(http.MethodGet, "/departures?origin=SHF&date=20260511&time=0800", nil)
	w := httptest.NewRecorder()
	srv.handleDepartures(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "ONTIME")
	assert.Contains(t, body, "DELAYED")
	assert.NotContains(t, body, "NONPASS")
	assert.NotContains(t, body, "CANCAS")
	assert.NotContains(t, body, "NILDEP")
	assert.NotContains(t, body, "CANCDEP")
	assert.NotContains(t, body, "EARLY")
}

// --- handleCallingPoints ---

func TestHandleCallingPoints_APIError(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return nil, errors.New("api down")
		},
	}
	srv := newTestServer(t, mock)
	req := httptest.NewRequest(http.MethodGet, "/calling-points/A1/20260511?originCRS=SHF", nil)
	req = mux.SetURLVars(req, map[string]string{"uid": "A1", "date": "20260511"})
	w := httptest.NewRecorder()
	srv.handleCallingPoints(w, req)
	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestHandleCallingPoints(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return &ServiceResponse{
				ScheduleMeta: ServiceScheduleMeta{
					TrainReportingIdentity: "1A23",
					Operator:               Operator{Name: "Test Rail"},
				},
				Locations: []ServiceLocation{
					svcLoc("SHF", "0900", "0900", "", ""), // origin — skipped by filter
					{Location: StopLocation{ShortCodes: []string{"TBD"}, Description: "Tebay"},
						TemporalData: ServiceTemporalData{DisplayAs: "PASS"}}, // PASS — skipped
					{Location: StopLocation{ShortCodes: []string{"MAN"}, Description: "Manchester Piccadilly"},
						TemporalData: ServiceTemporalData{DisplayAs: "CALL",
							Arrival: &TemporalPoint{ScheduleAdvertised: iso("0945"), RealtimeForecast: iso("0950")}}},
				},
			}, nil
		},
	}
	srv := newTestServer(t, mock)
	req := httptest.NewRequest(http.MethodGet, "/calling-points/A1/20260511?originCRS=SHF", nil)
	req = mux.SetURLVars(req, map[string]string{"uid": "A1", "date": "20260511"})
	w := httptest.NewRecorder()
	srv.handleCallingPoints(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Manchester Piccadilly")
	assert.NotContains(t, w.Body.String(), "Tebay") // PASS stops not shown
}

// --- handleJourney ---

func TestHandleCallingPoints_NoOriginFilter(t *testing.T) {
	// When originCRS is empty, all locations are shown (no skip-to-origin filtering).
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return &ServiceResponse{
				ScheduleMeta: ServiceScheduleMeta{TrainReportingIdentity: "1A23", Operator: Operator{Name: "Test Rail"}},
				Locations: []ServiceLocation{
					{Location: StopLocation{ShortCodes: []string{"SHF"}, Description: "Sheffield"},
						TemporalData: ServiceTemporalData{DisplayAs: "CALL",
							Arrival: &TemporalPoint{ScheduleAdvertised: iso("0900"), RealtimeForecast: iso("0900")}}},
				},
			}, nil
		},
	}
	srv := newTestServer(t, mock)
	req := httptest.NewRequest(http.MethodGet, "/calling-points/A1/20260511", nil) // no originCRS
	req = mux.SetURLVars(req, map[string]string{"uid": "A1", "date": "20260511"})
	w := httptest.NewRecorder()
	srv.handleCallingPoints(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Sheffield")
}

func TestHandleJourney_InvalidLegs(t *testing.T) {
	srv := newTestServer(t, &rttAPIMock{})
	r := httptest.NewRequest(http.MethodGet, "/journey?legs=", nil)
	w := httptest.NewRecorder()
	srv.handleJourney(w, r)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/", w.Header().Get("Location"))
}

func TestHandleJourney_Valid(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return testSvcResp("SHF", "MAN", "0900", "0900", "0945", "0945"), nil
		},
	}
	srv := newTestServer(t, mock)
	r := httptest.NewRequest(http.MethodGet, "/journey?legs=A1|20260511|SHF|MAN", nil)
	w := httptest.NewRecorder()
	srv.handleJourney(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Sheffield")
	assert.Contains(t, w.Body.String(), "Manchester")
}

// --- handleLeg ---

func TestHandleLeg_InvalidIndex(t *testing.T) {
	srv := newTestServer(t, &rttAPIMock{})
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
			return testSvcResp("SHF", "MAN", "0900", "0905", "0945", "0950"), nil
		},
	}
	srv := newTestServer(t, mock)
	req := httptest.NewRequest(http.MethodGet, "/leg/0?legs=A1|20260511|SHF|MAN", nil)
	req = mux.SetURLVars(req, map[string]string{"n": "0"})
	w := httptest.NewRecorder()
	srv.handleLeg(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- buildLegCards ---

func TestBuildLegCards_OnTimeStatus(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return testSvcResp("SHF", "MAN", "0900", "0900", "0945", "0945"), nil
		},
	}
	srv := newTestServer(t, mock)
	legs := []Leg{{UID: "A1", Date: "20260511", Origin: "SHF", Dest: "MAN"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 1)
	assert.Equal(t, "on-time", cards[0].Status)
}

func TestBuildLegCards_CancelledStatus(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			svc := testSvcResp("SHF", "MAN", "0900", "0900", "0945", "0945")
			svc.Locations[0].TemporalData.DisplayAs = "CANCELLED_CALL"
			return svc, nil
		},
	}
	srv := newTestServer(t, mock)
	legs := []Leg{{UID: "A1", Date: "20260511", Origin: "SHF", Dest: "MAN"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 1)
	assert.Equal(t, "cancelled", cards[0].Status)
}

func TestBuildLegCards_NoRealtimeFallback(t *testing.T) {
	// When RealtimeForecast is empty, times fall back to ScheduleAdvertised.
	svc := &ServiceResponse{
		ScheduleMeta: ServiceScheduleMeta{
			TrainReportingIdentity: "1A23",
			Operator:               Operator{Name: "Test Rail"},
		},
		Locations: []ServiceLocation{
			{Location: StopLocation{ShortCodes: []string{"SHF"}},
				TemporalData: ServiceTemporalData{DisplayAs: "CALL",
					Departure: &TemporalPoint{ScheduleAdvertised: iso("0900"), RealtimeForecast: ""}}},
			{Location: StopLocation{ShortCodes: []string{"MAN"}},
				TemporalData: ServiceTemporalData{DisplayAs: "CALL",
					Arrival: &TemporalPoint{ScheduleAdvertised: iso("0945"), RealtimeForecast: ""}}},
		},
	}
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) { return svc, nil },
	}
	srv := newTestServer(t, mock)
	legs := []Leg{{UID: "A1", Date: "20260511", Origin: "SHF", Dest: "MAN"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 1)
	assert.Equal(t, "09:00", cards[0].DepRealtime) // falls back to booked
	assert.Equal(t, "09:45", cards[0].ArrRealtime) // falls back to booked
}

func TestBuildLegCards_ServiceError(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return nil, errors.New("api down")
		},
	}
	srv := newTestServer(t, mock)
	legs := []Leg{{UID: "X1", Date: "20260511", Origin: "SHF", Dest: "MAN"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 1)
	assert.Equal(t, "error", cards[0].Status)
}

func TestBuildLegCards_WithLocations(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return testSvcResp("SHF", "MAN", "0900", "0905", "0945", "0950"), nil
		},
	}
	srv := newTestServer(t, mock)
	legs := []Leg{{UID: "A1", Date: "20260511", Origin: "SHF", Dest: "MAN"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))

	require.Len(t, cards, 1)
	c := cards[0]
	assert.Equal(t, "09:00", c.DepBooked)
	assert.Equal(t, "09:05", c.DepRealtime)
	assert.Equal(t, "09:45", c.ArrBooked)
	assert.Equal(t, "09:50", c.ArrRealtime)
	assert.Equal(t, 5, c.DelayMins)
	assert.Equal(t, "delayed", c.Status)
}

// TestAnnotateGoodConnection verifies two legs with >10-min interchange are NOT flagged tight.
func TestAnnotateGoodConnection(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			if uid == "LEG1" {
				return testSvcResp("SHF", "MAN", "0900", "0900", "0945", "0945"), nil
			}
			return testSvcResp("MAN", "LDS", "1010", "1010", "1100", "1100"), nil
		},
	}
	srv := newTestServer(t, mock)
	legs := []Leg{
		{UID: "LEG1", Date: "20260511", Origin: "SHF", Dest: "MAN"},
		{UID: "LEG2", Date: "20260511", Origin: "MAN", Dest: "LDS"},
	}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 2)
	assert.False(t, cards[1].TightConnection) // 25 min gap — not tight
}

// TestAnnotateTightConnection verifies two legs with a 5-min interchange are flagged tight
// and that a good alternative departure is surfaced in NextDeps.
func TestAnnotateTightConnection(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			if uid == "LEG1" {
				// SHF → MAN arriving 09:10
				return testSvcResp("SHF", "MAN", "0905", "0905", "0910", "0910"), nil
			}
			// MAN → LDS departing 09:15
			return testSvcResp("MAN", "LDS", "0915", "0915", "1000", "1000"), nil
		},
		SearchDeparturesFunc: func(crs, date, fromTime string) (*SearchResponse, error) {
			// Alternative from MAN at 09:25 (15 min buffer → good)
			return &SearchResponse{Services: []Service{passengerDep("NEXT", "0925")}}, nil
		},
	}
	srv := newTestServer(t, mock)
	legs := []Leg{
		{UID: "LEG1", Date: "20260511", Origin: "SHF", Dest: "MAN"},
		{UID: "LEG2", Date: "20260511", Origin: "MAN", Dest: "LDS"},
	}
	cards := srv.buildLegCards(legs, encodeLegs(legs))

	require.Len(t, cards, 2)
	assert.False(t, cards[0].TightConnection)
	assert.True(t, cards[1].TightConnection)
	assert.Equal(t, 5, cards[1].ConnectionMins)
	require.Len(t, cards[1].NextDeps, 1)
	assert.Equal(t, "09:25", cards[1].NextDeps[0].Time)
	assert.True(t, cards[1].NextDeps[0].IsGood)
}

// --- nextDeparturesAfter ---

func TestNextDeparturesAfter_Error(t *testing.T) {
	mock := &rttAPIMock{
		SearchDeparturesFunc: func(crs, date, fromTime string) (*SearchResponse, error) {
			return nil, errors.New("api down")
		},
	}
	srv := newTestServer(t, mock)
	deps := srv.nextDeparturesAfter("MAN", "20260511", "0910", "0915")
	assert.Nil(t, deps)
}

func TestNextDeparturesAfter_TightThenGood(t *testing.T) {
	// Returns: cancelled (filtered), non-passenger (filtered), tight dep, then good dep.
	mock := &rttAPIMock{
		SearchDeparturesFunc: func(crs, date, fromTime string) (*SearchResponse, error) {
			return &SearchResponse{Services: []Service{
				// cancelled → filtered
				{ScheduleMeta: ServiceScheduleMeta{InPassengerService: true},
					TemporalData: ServiceTemporalData{DisplayAs: "CANCELLED_CALL",
						Departure: &TemporalPoint{ScheduleAdvertised: iso("0912"), RealtimeForecast: iso("0912")}}},
				// non-passenger → filtered
				{ScheduleMeta: ServiceScheduleMeta{InPassengerService: false},
					TemporalData: ServiceTemporalData{DisplayAs: "CALL",
						Departure: &TemporalPoint{ScheduleAdvertised: iso("0913"), RealtimeForecast: iso("0913")}}},
				// dep <= skipHHMM ("0914" <= "0915") → filtered
				passengerDep("EARLY", "0914"),
				// no realtime → falls back to scheduled advertised at 0916
				{ScheduleMeta: ServiceScheduleMeta{InPassengerService: true},
					TemporalData: ServiceTemporalData{DisplayAs: "CALL",
						Departure: &TemporalPoint{ScheduleAdvertised: iso("0916"), RealtimeForecast: ""}}},
				// good dep → included, breaks loop
				passengerDep("GOOD", "0925"),
			}}, nil
		},
	}
	srv := newTestServer(t, mock)
	// arrHHMM="0910", skipHHMM="0915"
	deps := srv.nextDeparturesAfter("MAN", "20260511", "0910", "0915")
	require.Len(t, deps, 2)
	assert.Equal(t, "09:16", deps[0].Time)
	assert.False(t, deps[0].IsGood) // 6 min buffer — tight
	assert.Equal(t, "09:25", deps[1].Time)
	assert.True(t, deps[1].IsGood) // 15 min buffer — good
}

// --- direct helper tests ---

func TestIsActualDep(t *testing.T) {
	card := &LegCard{}

	withForecast := &ServiceResponse{Locations: []ServiceLocation{
		{Location: StopLocation{ShortCodes: []string{"SHF"}},
			TemporalData: ServiceTemporalData{Departure: &TemporalPoint{RealtimeForecast: iso("0900")}}},
	}}
	assert.True(t, card.isActualDep(withForecast, "SHF"))
	assert.False(t, card.isActualDep(withForecast, "MAN")) // CRS not found

	noForecast := &ServiceResponse{Locations: []ServiceLocation{
		{Location: StopLocation{ShortCodes: []string{"SHF"}},
			TemporalData: ServiceTemporalData{Departure: &TemporalPoint{RealtimeForecast: ""}}},
	}}
	assert.False(t, card.isActualDep(noForecast, "SHF"))
}

func TestIsCancelled(t *testing.T) {
	byDisplayAs := &ServiceResponse{Locations: []ServiceLocation{
		{Location: StopLocation{ShortCodes: []string{"SHF"}},
			TemporalData: ServiceTemporalData{DisplayAs: "CANCELLED_CALL"}},
	}}
	assert.True(t, isCancelled(byDisplayAs, "SHF"))
	assert.False(t, isCancelled(byDisplayAs, "MAN")) // CRS not found

	byFlag := &ServiceResponse{Locations: []ServiceLocation{
		{Location: StopLocation{ShortCodes: []string{"SHF"}},
			TemporalData: ServiceTemporalData{Departure: &TemporalPoint{IsCancelled: true}}},
	}}
	assert.True(t, isCancelled(byFlag, "SHF"))

	notCancelled := &ServiceResponse{Locations: []ServiceLocation{
		{Location: StopLocation{ShortCodes: []string{"SHF"}},
			TemporalData: ServiceTemporalData{DisplayAs: "CALL"}},
	}}
	assert.False(t, isCancelled(notCancelled, "SHF"))
}
