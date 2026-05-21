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

func TestStationsJSONFields(t *testing.T) {
	js := stationsJSON([]Station{{Name: "Sheffield", CRSCode: "SHF"}})
	var result []map[string]any
	require.NoError(t, json.Unmarshal([]byte(js), &result))
	require.Len(t, result, 1)
	assert.Equal(t, "SHF", result[0]["crs"], "autocomplete.js reads s.crs")
	assert.Equal(t, "Sheffield", result[0]["name"], "autocomplete.js reads s.name")
}

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
		"stationsJSON": stationsJSON,
		"abs": func(n int) int {
			if n < 0 {
				return -n
			}
			return n
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

// passengerDep builds a minimal passenger Service with a departure time and
// destination CRS for SearchResponse use.
func passengerDep(uid, depRT, destCRS string) Service {
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
		Destination: []StationStop{
			{Location: StopLocation{ShortCodes: []string{destCRS}}},
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
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			return nil, errors.New("api down")
		},
	}
	srv := newTestServer(t, mock)
	r := httptest.NewRequest(http.MethodGet, "/departures?origin=SHF&date=20260511&time=0900", nil)
	w := httptest.NewRecorder()
	srv.handleDepartures(w, r)
	assert.Equal(t, http.StatusBadGateway, w.Code)
	assert.Contains(t, w.Body.String(), "Could not load departures")
	assert.Contains(t, w.Body.String(), `id="results"`)
}

func TestHandleDepartures_EmptyResults(t *testing.T) {
	mock := &rttAPIMock{
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
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
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			return &SearchResponse{Services: services}, nil
		},
	}
	srv := newTestServer(t, mock)
	r := httptest.NewRequest(http.MethodGet, "/departures?origin=SHF&date=20260511&time=0800", nil)
	w := httptest.NewRecorder()
	srv.handleDepartures(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "Manchester") // valid on-time service destination
	assert.Contains(t, body, "Leeds")       // valid delayed service destination
	assert.NotContains(t, body, "NONPASS")
	assert.NotContains(t, body, "CANCAS")
	assert.NotContains(t, body, "NILDEP")
	assert.NotContains(t, body, "CANCDEP")
	assert.NotContains(t, body, "EARLY")
}

func TestHandleDepartures_ToFilter_CallsAt(t *testing.T) {
	// A service terminating at Edinburgh should still appear when filtering for York,
	// because RTT already filters by calling point — the Go layer must not re-filter by destination name.
	services := []Service{
		{ScheduleMeta: ServiceScheduleMeta{InPassengerService: true, TrainReportingIdentity: "THRU", Identity: "T1", DepartureDate: "2026-05-11"},
			TemporalData: ServiceTemporalData{DisplayAs: "CALL", Departure: &TemporalPoint{ScheduleAdvertised: iso("0900"), RealtimeForecast: iso("0900")}},
			Destination:  []StationStop{{Location: StopLocation{Description: "Edinburgh"}}}},
	}
	mock := &rttAPIMock{
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			return &SearchResponse{Services: services}, nil
		},
	}
	srv := newTestServer(t, mock)
	r := httptest.NewRequest(http.MethodGet, "/departures?origin=SHF&date=20260511&time=0800&to=YRK", nil)
	w := httptest.NewRecorder()
	srv.handleDepartures(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Edinburgh") // service destination should appear
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
	assert.Contains(t, w.Body.String(), "Could not load service")
	assert.Contains(t, w.Body.String(), `id="results"`)
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
	assert.Contains(t, w.Body.String(), "Continue journey")
	assert.Contains(t, w.Body.String(), "A1|20260511|SHF|MAN")
}

func TestHandleJourney_ContinuePrefill(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return testSvcResp("SHF", "MAN", "0900", "0900", "0945", "0950"), nil
		},
	}
	srv := newTestServer(t, mock)
	r := httptest.NewRequest(http.MethodGet, "/journey?legs=A1|20260511|SHF|MAN", nil)
	w := httptest.NewRecorder()
	srv.handleJourney(w, r)
	body := w.Body.String()
	assert.Contains(t, body, `value="2026-05-11"`)
	assert.Contains(t, body, `value="09:50"`)
}

func TestBuildLegCards_RemoveLegsParam(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return testSvcResp("SHF", "MAN", "0900", "0900", "0945", "0945"), nil
		},
	}
	srv := newTestServer(t, mock)
	legs := []Leg{
		{UID: "A1", Date: "20260511", Origin: "SHF", Dest: "MAN"},
		{UID: "B2", Date: "20260511", Origin: "MAN", Dest: "LDS"},
	}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 2)
	// removing first leg leaves second
	assert.Equal(t, "B2|20260511|MAN|LDS", cards[0].RemoveLegsParam)
	// removing last leg leaves first
	assert.Equal(t, "A1|20260511|SHF|MAN", cards[1].RemoveLegsParam)
}

func TestBuildLegCards_RemoveLegsParam_Single(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return testSvcResp("SHF", "MAN", "0900", "0900", "0945", "0945"), nil
		},
	}
	srv := newTestServer(t, mock)
	legs := []Leg{{UID: "A1", Date: "20260511", Origin: "SHF", Dest: "MAN"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 1)
	assert.Equal(t, "", cards[0].RemoveLegsParam) // empty → link goes to /
}

func TestYyyymmddToDash(t *testing.T) {
	assert.Equal(t, "2026-05-11", yyyymmddToDash("20260511"))
	assert.Equal(t, "2026-05-11", yyyymmddToDash("2026-05-11")) // pass-through if wrong length
	assert.Equal(t, "", yyyymmddToDash(""))
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
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			return &SearchResponse{}, nil
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
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			return &SearchResponse{}, nil
		},
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
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			// Alternative from MAN at 09:25 (15 min buffer → good)
			return &SearchResponse{Services: []Service{passengerDep("NEXT", "0925", "LDS")}}, nil
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

func TestAnnotateTightConnection_MissedConnection(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			if uid == "LEG1" {
				// SHF → MAN arriving 09:10 (on time)
				return testSvcResp("SHF", "MAN", "0905", "0905", "0910", "0910"), nil
			}
			// MAN → LDS booked 09:08 — already departed before leg 1 arrives
			return testSvcResp("MAN", "LDS", "0908", "0908", "1000", "1000"), nil
		},
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			// Next good train from MAN at 09:25
			return &SearchResponse{Services: []Service{passengerDep("NEXT", "0925", "LDS")}}, nil
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
	assert.Equal(t, -2, cards[1].ConnectionMins)
	require.Len(t, cards[1].NextDeps, 1)
	assert.Equal(t, "09:25", cards[1].NextDeps[0].Time)
	assert.True(t, cards[1].NextDeps[0].IsGood)
	assert.False(t, cards[1].NextDeps[0].AutoLoad) // chips are user-initiated; no auto-load
}

// --- nextDeparturesAfter ---

func TestNextDeparturesAfter_Error(t *testing.T) {
	mock := &rttAPIMock{
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			return nil, errors.New("api down")
		},
	}
	srv := newTestServer(t, mock)
	deps := srv.nextDeparturesAfter("MAN", "20260511", "0910", "0915", "LDS", "")
	assert.Nil(t, deps)
}

func TestNextDeparturesAfter_AllStatuses(t *testing.T) {
	// Verifies: cancelled shown with scheduled time, departed shown before skipHHMM,
	// non-passenger filtered, duplicate minute not deduped, no early break.
	mock := &rttAPIMock{
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			return &SearchResponse{Services: []Service{
				// cancelled passenger service calling at dest → shown as "cancelled"
				{ScheduleMeta: ServiceScheduleMeta{InPassengerService: true},
					TemporalData: ServiceTemporalData{DisplayAs: "CANCELLED_CALL",
						Departure: &TemporalPoint{ScheduleAdvertised: iso("0912"), RealtimeForecast: iso("0912")}},
					Destination: []StationStop{{Location: StopLocation{ShortCodes: []string{"LDS"}}}}},
				// non-passenger → still filtered
				{ScheduleMeta: ServiceScheduleMeta{InPassengerService: false},
					TemporalData: ServiceTemporalData{DisplayAs: "CALL",
						Departure: &TemporalPoint{ScheduleAdvertised: iso("0913"), RealtimeForecast: iso("0913")}}},
				// dep <= skipHHMM → "departed"
				passengerDep("EARLY", "0914", "LDS"),
				// second service at same minute as EARLY — no dedup, also "departed"
				passengerDep("EARLY2", "0914", "LDS"),
				// no realtime → falls back to scheduled at 0916, tight
				{ScheduleMeta: ServiceScheduleMeta{InPassengerService: true},
					TemporalData: ServiceTemporalData{DisplayAs: "CALL",
						Departure: &TemporalPoint{ScheduleAdvertised: iso("0916"), RealtimeForecast: ""}},
					Destination: []StationStop{{Location: StopLocation{ShortCodes: []string{"LDS"}}}}},
				// good dep — loop must NOT break here
				passengerDep("GOOD", "0925", "LDS"),
				// second good dep — must appear because there's no early break
				passengerDep("GOOD2", "0935", "LDS"),
			}}, nil
		},
	}
	srv := newTestServer(t, mock)
	// arrHHMM="0910", skipHHMM="0915", destCRS="LDS"
	deps := srv.nextDeparturesAfter("MAN", "20260511", "0910", "0915", "LDS", "")
	require.Len(t, deps, 6)

	assert.Equal(t, "09:12", deps[0].Time)
	assert.Equal(t, "cancelled", deps[0].Status)
	assert.False(t, deps[0].IsGood)

	assert.Equal(t, "09:14", deps[1].Time)
	assert.Equal(t, "departed", deps[1].Status)

	assert.Equal(t, "09:14", deps[2].Time) // duplicate minute — both shown
	assert.Equal(t, "departed", deps[2].Status)

	assert.Equal(t, "09:16", deps[3].Time)
	assert.Equal(t, "on-time", deps[3].Status)
	assert.False(t, deps[3].IsGood) // 6 min buffer — tight

	assert.Equal(t, "09:25", deps[4].Time)
	assert.Equal(t, "on-time", deps[4].Status)
	assert.True(t, deps[4].IsGood) // 15 min buffer — good

	assert.Equal(t, "09:35", deps[5].Time) // shown because no early break
	assert.True(t, deps[5].IsGood)
}

func TestNextDeparturesAfter_ExcludesBookedTrain(t *testing.T) {
	// The booked connecting train (dep == skipHHMM) must not appear in NextDeps.
	// Regression: it was showing as "departed" at 16:10 when skipHHMM was also 16:10.
	mock := &rttAPIMock{
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			return &SearchResponse{Services: []Service{
				passengerDep("BOOKED", "1610", "MAN"), // this IS the connecting train
				passengerDep("NEXT", "1645", "MAN"),   // this is the actual alternative
			}}, nil
		},
	}
	srv := newTestServer(t, mock)
	deps := srv.nextDeparturesAfter("NUN", "20260518", "1608", "1610", "MAN", "")
	require.Len(t, deps, 1)
	assert.Equal(t, "16:45", deps[0].Time)
	assert.Equal(t, "on-time", deps[0].Status)
}

// TestFilterDeps_LongCodeMatching verifies that filterDeps uses the RTT longCode
// (not the CRS shortCode) to match destinations, because the search response
// destination locations only carry longCodes (e.g. "MNCRPIC"), never shortCodes.
func TestFilterDeps_LongCodeMatching(t *testing.T) {
	manTrain := Service{
		ScheduleMeta: ServiceScheduleMeta{
			InPassengerService:     true,
			Identity:               "W34203",
			DepartureDate:          "2026-05-18",
			TrainReportingIdentity: "1H31",
		},
		TemporalData: ServiceTemporalData{
			DisplayAs: "CALL",
			Departure: &TemporalPoint{ScheduleAdvertised: iso("1710"), RealtimeForecast: iso("1710")},
		},
		Destination: []StationStop{
			{Location: StopLocation{LongCodes: []string{"MNCRPIC"}}}, // no shortCodes — matches real API shape
		},
	}
	euston := Service{
		ScheduleMeta: ServiceScheduleMeta{InPassengerService: true},
		TemporalData: ServiceTemporalData{
			DisplayAs: "CALL",
			Departure: &TemporalPoint{ScheduleAdvertised: iso("1611"), RealtimeForecast: iso("1611")},
		},
		Destination: []StationStop{
			{Location: StopLocation{LongCodes: []string{"EUSTON"}}},
		},
	}
	sr := &SearchResponse{Services: []Service{manTrain, euston}}
	deps := filterDeps(sr, "1610", "MAN", "MNCRPIC", hhmm2mins("1608"))
	require.Len(t, deps, 1, "only the MAN-bound train should pass")
	assert.Equal(t, "17:10", deps[0].Time)
	assert.Equal(t, "W34203", deps[0].UID)
	assert.Equal(t, "20260518", deps[0].Date)
	assert.True(t, deps[0].IsGood) // 62 min buffer ≥ 10
}

func TestNextDeparturesAfter_RequeriesWhenNoAltInFirstWindow(t *testing.T) {
	calls := 0
	mock := &rttAPIMock{
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			calls++
			if calls == 1 {
				// First window: only the booked train goes to MAN
				return &SearchResponse{Services: []Service{
					passengerDep("BOOKED", "1610", "MAN"),
				}}, nil
			}
			// Second window (from skipHHMM): next NUN→MAN train appears
			return &SearchResponse{Services: []Service{
				passengerDep("BOOKED", "1610", "MAN"),
				passengerDep("NEXT", "1740", "MAN"),
			}}, nil
		},
	}
	srv := newTestServer(t, mock)
	deps := srv.nextDeparturesAfter("NUN", "20260518", "1608", "1610", "MAN", "")
	assert.Equal(t, 2, calls)
	require.Len(t, deps, 1)
	assert.Equal(t, "17:40", deps[0].Time)
	assert.True(t, deps[0].IsGood)
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

	withActual := &ServiceResponse{Locations: []ServiceLocation{
		{Location: StopLocation{ShortCodes: []string{"SHF"}},
			TemporalData: ServiceTemporalData{Departure: &TemporalPoint{RealtimeActual: iso("0901")}}},
	}}
	assert.True(t, card.isActualDep(withActual, "SHF")) // actual field set, forecast empty

	noForecast := &ServiceResponse{Locations: []ServiceLocation{
		{Location: StopLocation{ShortCodes: []string{"SHF"}},
			TemporalData: ServiceTemporalData{Departure: &TemporalPoint{RealtimeForecast: ""}}},
	}}
	assert.False(t, card.isActualDep(noForecast, "SHF"))
}

func TestBuildLegCards_MissedStatus(t *testing.T) {
	// No realtime data at all — dep time in the past → missed.
	svc := &ServiceResponse{
		ScheduleMeta: ServiceScheduleMeta{TrainReportingIdentity: "1A23", Operator: Operator{Name: "Test Rail"}},
		Locations: []ServiceLocation{
			{Location: StopLocation{ShortCodes: []string{"SHF"}},
				TemporalData: ServiceTemporalData{DisplayAs: "CALL",
					Departure: &TemporalPoint{ScheduleAdvertised: iso("0800"), RealtimeForecast: "", RealtimeActual: ""}}},
			{Location: StopLocation{ShortCodes: []string{"MAN"}},
				TemporalData: ServiceTemporalData{DisplayAs: "CALL",
					Arrival: &TemporalPoint{ScheduleAdvertised: iso("0845"), RealtimeForecast: "", RealtimeActual: ""}}},
		},
	}
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) { return svc, nil },
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			return &SearchResponse{}, nil
		},
	}
	srv := newTestServer(t, mock)
	legs := []Leg{{UID: "A1", Date: "20260511", Origin: "SHF", Dest: "MAN"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 1)
	assert.Equal(t, "missed", cards[0].Status)
	assert.Equal(t, "MAN", cards[0].DestCRS)
}

func TestAnnotateMissedLegs_NextDepDisplayed(t *testing.T) {
	// Missed first leg — annotateMissedLegs should populate NextDeps with AutoLoad.
	svc := &ServiceResponse{
		ScheduleMeta: ServiceScheduleMeta{TrainReportingIdentity: "1A23", Operator: Operator{Name: "Test Rail"}},
		Locations: []ServiceLocation{
			{Location: StopLocation{ShortCodes: []string{"SHF"}},
				TemporalData: ServiceTemporalData{DisplayAs: "CALL",
					Departure: &TemporalPoint{ScheduleAdvertised: iso("0800"), RealtimeForecast: "", RealtimeActual: ""}}},
			{Location: StopLocation{ShortCodes: []string{"MAN"}},
				TemporalData: ServiceTemporalData{DisplayAs: "CALL",
					Arrival: &TemporalPoint{ScheduleAdvertised: iso("0845"), RealtimeForecast: "", RealtimeActual: ""}}},
		},
	}
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) { return svc, nil },
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			assert.Equal(t, "SHF", crs)
			assert.Equal(t, "MAN", to)
			return &SearchResponse{Services: []Service{passengerDep("NEXT", "0925", "MAN")}}, nil
		},
	}
	srv := newTestServer(t, mock)
	legs := []Leg{{UID: "A1", Date: "20260511", Origin: "SHF", Dest: "MAN"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 1)
	assert.Equal(t, "missed", cards[0].Status)
	assert.False(t, cards[0].TightConnection)
	assert.Equal(t, "SHF", cards[0].InterchangeCRS)
	assert.Equal(t, "MAN", cards[0].DestCRS)
	require.Len(t, cards[0].NextDeps, 1)
	assert.Equal(t, "09:25", cards[0].NextDeps[0].Time)
	assert.True(t, cards[0].NextDeps[0].IsGood)
	assert.False(t, cards[0].NextDeps[0].AutoLoad) // chips are user-initiated; no auto-load
}

func TestBuildLegCards_ActualDepartedNotMissed(t *testing.T) {
	// RealtimeForecast empty but RealtimeActual set — train has departed, not missed.
	svc := &ServiceResponse{
		ScheduleMeta: ServiceScheduleMeta{TrainReportingIdentity: "1A23", Operator: Operator{Name: "Test Rail"}},
		Locations: []ServiceLocation{
			{Location: StopLocation{ShortCodes: []string{"SHF"}},
				TemporalData: ServiceTemporalData{DisplayAs: "CALL",
					Departure: &TemporalPoint{ScheduleAdvertised: iso("0800"), RealtimeForecast: "", RealtimeActual: iso("0801")}}},
			{Location: StopLocation{ShortCodes: []string{"MAN"}},
				TemporalData: ServiceTemporalData{DisplayAs: "CALL",
					Arrival: &TemporalPoint{ScheduleAdvertised: iso("0845"), RealtimeForecast: "", RealtimeActual: iso("0846")}}},
		},
	}
	mock := &rttAPIMock{GetServiceFunc: func(uid, date string) (*ServiceResponse, error) { return svc, nil }}
	srv := newTestServer(t, mock) // nowTime = "09:00", past both times
	legs := []Leg{{UID: "A1", Date: "20260511", Origin: "SHF", Dest: "MAN"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 1)
	assert.Equal(t, "delayed", cards[0].Status)
	assert.Equal(t, "08:01", cards[0].DepRealtime) // shows actual departure time
	assert.Equal(t, "08:46", cards[0].ArrRealtime)
	assert.Equal(t, 1, cards[0].DelayMins)
}

func makeSvcForTransit() *ServiceResponse {
	return &ServiceResponse{
		Locations: []ServiceLocation{
			{Location: StopLocation{ShortCodes: []string{"CMB"}, Description: "Cambridge North"},
				TemporalData: ServiceTemporalData{Departure: &TemporalPoint{ScheduleAdvertised: iso("1210"), RealtimeActual: iso("1210")}}},
			{Location: StopLocation{ShortCodes: []string{"CBG"}, Description: "Cambridge"},
				TemporalData: ServiceTemporalData{
					Arrival:   &TemporalPoint{ScheduleAdvertised: iso("1216"), RealtimeActual: iso("1216")},
					Departure: &TemporalPoint{ScheduleAdvertised: iso("1219"), RealtimeActual: iso("1218")}}}, // 1m early
			{Location: StopLocation{ShortCodes: []string{"AUD"}, Description: "Audley End"},
				LocationMeta: ServiceLocationMeta{Platform: struct {
					Planned  string `json:"planned"`
					Actual   string `json:"actual"`
					Forecast string `json:"forecast"`
				}{Forecast: "1"}},
				TemporalData: ServiceTemporalData{
					Arrival:   &TemporalPoint{ScheduleAdvertised: iso("1239"), RealtimeForecast: iso("1238")}, // 1m early
					Departure: &TemporalPoint{ScheduleAdvertised: iso("1239"), RealtimeForecast: iso("1239")}}},
			{Location: StopLocation{ShortCodes: []string{"BIS"}, Description: "Bishops Stortford"},
				TemporalData: ServiceTemporalData{
					Arrival:   &TemporalPoint{ScheduleAdvertised: iso("1257"), RealtimeForecast: iso("1257")},
					Departure: &TemporalPoint{ScheduleAdvertised: iso("1257"), RealtimeForecast: iso("1257")}}},
			{Location: StopLocation{ShortCodes: []string{"LST"}, Description: "London Liverpool Street"},
				LocationMeta: ServiceLocationMeta{Platform: struct {
					Planned  string `json:"planned"`
					Actual   string `json:"actual"`
					Forecast string `json:"forecast"`
				}{Forecast: "3"}},
				TemporalData: ServiceTemporalData{
					Arrival: &TemporalPoint{ScheduleAdvertised: iso("1342"), RealtimeForecast: iso("1341")}}},
		},
	}
}

func TestBuildInTransitInfo(t *testing.T) {
	t.Run("in transit between intermediate stops", func(t *testing.T) {
		info := buildInTransitInfo(makeSvcForTransit(), "CMB", "LST")
		assert.True(t, info.Active)
		assert.Equal(t, "Cambridge", info.LastStopName)
		assert.Equal(t, -1, info.RunningDelayMins)    // Cambridge departed 1m early
		assert.Equal(t, "Audley End", info.NextStopName)
		assert.Equal(t, "12:38", info.NextStopTime)
		assert.Equal(t, -1, info.NextStopDelayMins)   // Audley End forecast 1m early
		assert.Equal(t, "1", info.NextStopPlatform)   // platform.forecast = "1"
		assert.Equal(t, 3, info.StopsRemaining)       // AUD, BIS, LST
	})

	t.Run("not in transit — origin not yet departed", func(t *testing.T) {
		svc := makeSvcForTransit()
		svc.Locations[0].TemporalData.Departure = &TemporalPoint{RealtimeForecast: iso("1210")}
		info := buildInTransitInfo(svc, "CMB", "LST")
		assert.False(t, info.Active)
	})

	t.Run("not in transit — destination already arrived", func(t *testing.T) {
		svc := makeSvcForTransit()
		svc.Locations[4].TemporalData.Arrival = &TemporalPoint{RealtimeActual: iso("1341")}
		info := buildInTransitInfo(svc, "CMB", "LST")
		assert.False(t, info.Active)
	})

	t.Run("in transit between origin and first stop", func(t *testing.T) {
		svc := makeSvcForTransit()
		// Remove actual times from Cambridge — train has only left Cambridge North.
		svc.Locations[1].TemporalData.Arrival = &TemporalPoint{RealtimeForecast: iso("1216")}
		svc.Locations[1].TemporalData.Departure = &TemporalPoint{RealtimeForecast: iso("1218")}
		info := buildInTransitInfo(svc, "CMB", "LST")
		assert.True(t, info.Active)
		assert.Equal(t, "Cambridge North", info.LastStopName)
		assert.Equal(t, "Cambridge", info.NextStopName)
		assert.Equal(t, "12:16", info.NextStopTime)
		assert.Equal(t, 4, info.StopsRemaining) // CBG, AUD, BIS, LST
	})

	t.Run("unknown CRS returns inactive", func(t *testing.T) {
		info := buildInTransitInfo(makeSvcForTransit(), "XYZ", "LST")
		assert.False(t, info.Active)
	})
}

func TestBuildLegCards_DestPlatform(t *testing.T) {
	svc := makeSvcForTransit()
	mock := &rttAPIMock{GetServiceFunc: func(uid, date string) (*ServiceResponse, error) { return svc, nil }}
	srv := newTestServer(t, mock)
	legs := []Leg{{UID: "A1", Date: "20260511", Origin: "CMB", Dest: "LST"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 1)
	assert.Equal(t, "3", cards[0].DestPlatform) // forecast platform on LST
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

func TestIsDestCancelled(t *testing.T) {
	byCancelled := &ServiceResponse{Locations: []ServiceLocation{
		{Location: StopLocation{ShortCodes: []string{"MIA"}},
			TemporalData: ServiceTemporalData{DisplayAs: "CANCELLED"}},
	}}
	assert.True(t, isDestCancelled(byCancelled, "MIA"))
	assert.False(t, isDestCancelled(byCancelled, "MAN")) // CRS not found

	byArrFlag := &ServiceResponse{Locations: []ServiceLocation{
		{Location: StopLocation{ShortCodes: []string{"MIA"}},
			TemporalData: ServiceTemporalData{
				DisplayAs: "CANCELLED",
				Arrival:   &TemporalPoint{IsCancelled: true},
			}},
	}}
	assert.True(t, isDestCancelled(byArrFlag, "MIA"))

	// TERMINATES with no cancelled departure = scheduled terminus, not cancelled
	terminates := &ServiceResponse{Locations: []ServiceLocation{
		{Location: StopLocation{ShortCodes: []string{"MCO"}},
			TemporalData: ServiceTemporalData{DisplayAs: "TERMINATES"}},
	}}
	assert.False(t, isDestCancelled(terminates, "MCO"))

	// TERMINATES with cancelled departure = train short-formed here, treat as cancelled
	shortFormed := &ServiceResponse{Locations: []ServiceLocation{
		{Location: StopLocation{ShortCodes: []string{"MCO"}},
			TemporalData: ServiceTemporalData{
				DisplayAs: "TERMINATES",
				Departure: &TemporalPoint{IsCancelled: true},
			}},
	}}
	assert.True(t, isDestCancelled(shortFormed, "MCO"))
}

func TestCancelReason_APIText(t *testing.T) {
	svc := &ServiceResponse{
		Reasons: []Reason{{Code: "IB", LongText: "a points failure"}},
	}
	assert.Equal(t, "a points failure", cancelReason(svc))
}

func TestCancelReason_Fallback(t *testing.T) {
	svc := &ServiceResponse{
		Reasons: []Reason{{Code: "IB", LongText: ""}},
	}
	assert.Equal(t, "Points failure (including no fault found)", cancelReason(svc))
}

func TestCancelReason_NoReasons(t *testing.T) {
	assert.Equal(t, "", cancelReason(&ServiceResponse{}))
}

// buildTerminatesService returns a ServiceResponse shaped like debug_service.json:
// Liverpool (CALL) → Manchester Oxford Road (TERMINATES) → Manchester Airport (CANCELLED).
func buildTerminatesService() *ServiceResponse {
	return &ServiceResponse{
		ScheduleMeta: ServiceScheduleMeta{
			TrainReportingIdentity: "2A90",
			Operator:               Operator{Name: "Northern"},
		},
		Locations: []ServiceLocation{
			{
				Location:     StopLocation{ShortCodes: []string{"LIV"}},
				TemporalData: ServiceTemporalData{DisplayAs: "CALL", Departure: &TemporalPoint{ScheduleAdvertised: iso("1830"), RealtimeActual: iso("1927")}},
			},
			{
				Location: StopLocation{ShortCodes: []string{"MCO"}},
				TemporalData: ServiceTemporalData{
					DisplayAs: "TERMINATES",
					Arrival:   &TemporalPoint{ScheduleAdvertised: iso("1926"), RealtimeActual: iso("2031")},
					Departure: &TemporalPoint{ScheduleAdvertised: iso("1927"), IsCancelled: true},
				},
			},
			{
				Location: StopLocation{ShortCodes: []string{"MIA"}},
				TemporalData: ServiceTemporalData{DisplayAs: "CANCELLED",
					Arrival: &TemporalPoint{ScheduleAdvertised: iso("1953"), IsCancelled: true}},
			},
		},
		Reasons: []Reason{{Code: "IB", LongText: "a points failure"}},
	}
}

func TestBuildLegCards_DestCancelled(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return buildTerminatesService(), nil
		},
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			return &SearchResponse{}, nil
		},
	}
	srv := newTestServer(t, mock)
	// Leg to Manchester Airport — service terminated early at Oxford Road
	legs := []Leg{{UID: "G90090", Date: "20260517", Origin: "LIV", Dest: "MIA"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 1)
	assert.Equal(t, "cancelled", cards[0].Status)
	assert.Equal(t, "a points failure", cards[0].CancelReason)
}

func TestBuildLegCards_ShortFormedAtDest(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return buildTerminatesService(), nil
		},
		SearchDeparturesFunc: func(crs, date, fromTime, to string) (*SearchResponse, error) {
			return &SearchResponse{}, nil
		},
	}
	srv := newTestServer(t, mock)
	// Leg terminates exactly at the short-formed stop — should be cancelled, not delayed
	legs := []Leg{{UID: "G90090", Date: "20260517", Origin: "LIV", Dest: "MCO"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 1)
	assert.Equal(t, "cancelled", cards[0].Status)
	assert.Equal(t, "a points failure", cards[0].CancelReason)
}

func TestCallingPoints_CancelledFiltered(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return buildTerminatesService(), nil
		},
	}
	srv := newTestServer(t, mock)
	req := httptest.NewRequest("GET", "/calling/G90090/20260517?originCRS=LIV", nil)
	req = mux.SetURLVars(req, map[string]string{"uid": "G90090", "date": "20260517"})
	w := httptest.NewRecorder()
	srv.handleCallingPoints(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	// TERMINATES stop (MCO) should appear
	assert.Contains(t, body, "MCO")
	// CANCELLED stop (MIA) should NOT appear
	assert.NotContains(t, body, "MIA")
}

// --- humanTrainStatus ---

func TestHumanTrainStatus(t *testing.T) {
	cases := []struct{ in, want string }{
		{"APPROACHING", "Approaching"},
		{"ARRIVING", "Arriving"},
		{"AT_PLATFORM", "At platform"},
		{"DEPART_PREPARING", "Ready to depart"},
		{"DEPART_READY", "Ready to depart"},
		{"DEPARTING", "Departing"},
		{"", ""},
		{"UNKNOWN_VALUE", ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, humanTrainStatus(tc.in), "input: %q", tc.in)
	}
}

// --- terminalStation ---

func TestTerminalStation_Normal(t *testing.T) {
	svc := &ServiceResponse{
		Locations: []ServiceLocation{
			{Location: StopLocation{Description: "Sheffield"}, TemporalData: ServiceTemporalData{DisplayAs: "CALL"}},
			{Location: StopLocation{Description: "Doncaster"}, TemporalData: ServiceTemporalData{DisplayAs: "CALL"}},
			{Location: StopLocation{Description: "York"}, TemporalData: ServiceTemporalData{DisplayAs: "CALL"}},
		},
	}
	assert.Equal(t, "York", terminalStation(svc))
}

func TestTerminalStation_Terminates(t *testing.T) {
	svc := &ServiceResponse{
		Locations: []ServiceLocation{
			{Location: StopLocation{Description: "Sheffield"}, TemporalData: ServiceTemporalData{DisplayAs: "CALL"}},
			{Location: StopLocation{Description: "York"}, TemporalData: ServiceTemporalData{DisplayAs: "TERMINATES"}},
		},
	}
	assert.Equal(t, "York", terminalStation(svc))
}

func TestTerminalStation_SkipsPass(t *testing.T) {
	svc := &ServiceResponse{
		Locations: []ServiceLocation{
			{Location: StopLocation{Description: "Sheffield"}, TemporalData: ServiceTemporalData{DisplayAs: "CALL"}},
			{Location: StopLocation{Description: "Doncaster"}, TemporalData: ServiceTemporalData{DisplayAs: "PASS"}},
		},
	}
	assert.Equal(t, "Sheffield", terminalStation(svc))
}

func TestTerminalStation_Empty(t *testing.T) {
	assert.Equal(t, "", terminalStation(&ServiceResponse{}))
}

// --- ServiceRow.ArrBooked ---

func TestServiceToRow_ArrBooked_MatchesTerminal(t *testing.T) {
	svc := Service{
		ScheduleMeta: ServiceScheduleMeta{
			Identity:           "X1",
			InPassengerService: true,
			DepartureDate:      "2026-05-11",
		},
		TemporalData: ServiceTemporalData{
			DisplayAs: "CALL",
			Departure: &TemporalPoint{
				ScheduleAdvertised: iso("0900"),
				RealtimeForecast:   iso("0900"),
			},
		},
		Destination: []StationStop{
			{
				Location:     StopLocation{ShortCodes: []string{"CBG"}, Description: "Cambridge"},
				TemporalData: StopTemporal{ScheduleAdvertised: iso("1015")},
			},
		},
	}
	row, ok := serviceToRow(svc, "0800", "CBG")
	require.True(t, ok)
	assert.Equal(t, "10:15", row.ArrBooked)
}

func TestServiceToRow_ArrBooked_NoMatchIntermediate(t *testing.T) {
	svc := Service{
		ScheduleMeta: ServiceScheduleMeta{
			Identity:           "X2",
			InPassengerService: true,
			DepartureDate:      "2026-05-11",
		},
		TemporalData: ServiceTemporalData{
			DisplayAs: "CALL",
			Departure: &TemporalPoint{
				ScheduleAdvertised: iso("0900"),
				RealtimeForecast:   iso("0900"),
			},
		},
		Destination: []StationStop{
			{
				Location:     StopLocation{ShortCodes: []string{"EDB"}, Description: "Edinburgh"},
				TemporalData: StopTemporal{ScheduleAdvertised: iso("1300")},
			},
		},
	}
	// filtering to an intermediate stop that is not the terminal
	row, ok := serviceToRow(svc, "0800", "YRK")
	require.True(t, ok)
	assert.Equal(t, "", row.ArrBooked)
}

func TestServiceToRow_ArrBooked_EmptyTo(t *testing.T) {
	svc := Service{
		ScheduleMeta: ServiceScheduleMeta{
			Identity:           "X3",
			InPassengerService: true,
			DepartureDate:      "2026-05-11",
		},
		TemporalData: ServiceTemporalData{
			DisplayAs: "CALL",
			Departure: &TemporalPoint{
				ScheduleAdvertised: iso("0900"),
				RealtimeForecast:   iso("0900"),
			},
		},
		Destination: []StationStop{
			{
				Location:     StopLocation{ShortCodes: []string{"CBG"}},
				TemporalData: StopTemporal{ScheduleAdvertised: iso("1015")},
			},
		},
	}
	row, ok := serviceToRow(svc, "0800", "")
	require.True(t, ok)
	assert.Equal(t, "", row.ArrBooked)
}

// --- ServiceRow.TrainStatus ---

func TestServiceToRow_TrainStatus(t *testing.T) {
	svc := Service{
		ScheduleMeta: ServiceScheduleMeta{
			Identity:           "X4",
			InPassengerService: true,
			DepartureDate:      "2026-05-11",
		},
		TemporalData: ServiceTemporalData{
			DisplayAs: "CALL",
			Status:    "AT_PLATFORM",
			Departure: &TemporalPoint{
				ScheduleAdvertised: iso("0900"),
				RealtimeForecast:   iso("0900"),
			},
		},
	}
	row, ok := serviceToRow(svc, "0800", "")
	require.True(t, ok)
	assert.Equal(t, "At platform", row.TrainStatus)
}

// --- handleCallingPoints auto-redirect ---

func TestCallingPoints_AutoRedirect_WhenToInRoute(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return &ServiceResponse{
				ScheduleMeta: ServiceScheduleMeta{TrainReportingIdentity: "1A23", Operator: Operator{Name: "Test Rail"}},
				Locations: []ServiceLocation{
					svcLoc("SHF", "0900", "0900", "", ""),
					{Location: StopLocation{ShortCodes: []string{"MAN"}, Description: "Manchester Piccadilly"},
						TemporalData: ServiceTemporalData{DisplayAs: "CALL",
							Arrival: &TemporalPoint{ScheduleAdvertised: iso("0945"), RealtimeForecast: iso("0950")}}},
				},
			}, nil
		},
	}
	srv := newTestServer(t, mock)
	req := httptest.NewRequest(http.MethodGet, "/calling-points/A1/20260511?originCRS=SHF&to=MAN", nil)
	req = mux.SetURLVars(req, map[string]string{"uid": "A1", "date": "20260511"})
	w := httptest.NewRecorder()
	srv.handleCallingPoints(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	redirect := w.Header().Get("HX-Redirect")
	assert.Contains(t, redirect, "/journey")
	assert.Contains(t, redirect, "A1|20260511|SHF|MAN")
}

func TestCallingPoints_NoRedirect_WhenToNotInRoute(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return &ServiceResponse{
				ScheduleMeta: ServiceScheduleMeta{TrainReportingIdentity: "1A23", Operator: Operator{Name: "Test Rail"}},
				Locations: []ServiceLocation{
					svcLoc("SHF", "0900", "0900", "", ""),
					{Location: StopLocation{ShortCodes: []string{"MAN"}, Description: "Manchester Piccadilly"},
						TemporalData: ServiceTemporalData{DisplayAs: "CALL",
							Arrival: &TemporalPoint{ScheduleAdvertised: iso("0945"), RealtimeForecast: iso("0950")}}},
				},
			}, nil
		},
	}
	srv := newTestServer(t, mock)
	req := httptest.NewRequest(http.MethodGet, "/calling-points/A1/20260511?originCRS=SHF&to=EDB", nil)
	req = mux.SetURLVars(req, map[string]string{"uid": "A1", "date": "20260511"})
	w := httptest.NewRecorder()
	srv.handleCallingPoints(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, w.Header().Get("HX-Redirect"))
	assert.Contains(t, w.Body.String(), "Manchester Piccadilly")
}

func TestCallingPoints_AutoRedirect_CarriesExistingLegs(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return &ServiceResponse{
				ScheduleMeta: ServiceScheduleMeta{TrainReportingIdentity: "1A23", Operator: Operator{Name: "Test Rail"}},
				Locations: []ServiceLocation{
					svcLoc("MAN", "1000", "1000", "", ""),
					{Location: StopLocation{ShortCodes: []string{"LDS"}, Description: "Leeds"},
						TemporalData: ServiceTemporalData{DisplayAs: "CALL",
							Arrival: &TemporalPoint{ScheduleAdvertised: iso("1100"), RealtimeForecast: iso("1100")}}},
				},
			}, nil
		},
	}
	srv := newTestServer(t, mock)
	existingLegs := "A1|20260511|SHF|MAN"
	req := httptest.NewRequest(http.MethodGet, "/calling-points/B2/20260511?originCRS=MAN&to=LDS&legs="+existingLegs, nil)
	req = mux.SetURLVars(req, map[string]string{"uid": "B2", "date": "20260511"})
	w := httptest.NewRecorder()
	srv.handleCallingPoints(w, req)
	redirect := w.Header().Get("HX-Redirect")
	assert.Contains(t, redirect, existingLegs)
	assert.Contains(t, redirect, "B2|20260511|MAN|LDS")
}

// --- buildLegCards FinalDest ---

func TestBuildLegCards_TrainStatus(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			svc := testSvcResp("SHF", "MAN", "0900", "0900", "0945", "0945")
			for i := range svc.Locations {
				if svc.Locations[i].CRS() == "SHF" {
					svc.Locations[i].TemporalData.Status = "AT_PLATFORM"
				}
			}
			return svc, nil
		},
	}
	srv := newTestServer(t, mock)
	legs := []Leg{{UID: "A1", Date: "20260511", Origin: "SHF", Dest: "MAN"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 1)
	assert.Equal(t, "At platform", cards[0].TrainStatus)
}

func TestBuildLegCards_FinalDest_WhenDifferentFromDest(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return &ServiceResponse{
				ScheduleMeta: ServiceScheduleMeta{
					TrainReportingIdentity: "1A23",
					Operator:               Operator{Name: "Test Rail"},
				},
				Locations: []ServiceLocation{
					svcLoc("SHF", "0900", "0900", "", ""),
					svcLoc("MAN", "", "", "0945", "0945"),
					svcLoc("LDS", "", "", "1030", "1030"),
				},
			}, nil
		},
	}
	srv := newTestServer(t, mock)
	legs := []Leg{{UID: "A1", Date: "20260511", Origin: "SHF", Dest: "MAN"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 1)
	// Terminal is Leeds (LDS), destination is Manchester (MAN) — FinalDest should be set
	assert.Equal(t, stationName("LDS"), cards[0].FinalDest)
}

func TestBuildLegCards_FinalDest_EmptyWhenSameAsDest(t *testing.T) {
	mock := &rttAPIMock{
		GetServiceFunc: func(uid, date string) (*ServiceResponse, error) {
			return testSvcResp("SHF", "MAN", "0900", "0900", "0945", "0945"), nil
		},
	}
	srv := newTestServer(t, mock)
	legs := []Leg{{UID: "A1", Date: "20260511", Origin: "SHF", Dest: "MAN"}}
	cards := srv.buildLegCards(legs, encodeLegs(legs))
	require.Len(t, cards, 1)
	// Terminal is Manchester — same as destination, so FinalDest should be empty
	assert.Equal(t, "", cards[0].FinalDest)
}
