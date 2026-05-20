package main

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

type rttAPI interface {
	SearchDepartures(crs, date, fromTime, to string) (*SearchResponse, error)
	GetService(uid, date string) (*ServiceResponse, error)
}

type server struct {
	rtt  rttAPI
	tmpl *template.Template
}

// -- helpers --

func (s *server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("template error", "name", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func todayYMD() string {
	return time.Now().Format("20060102")
}

func nowHHMM() string {
	return time.Now().Format("1504")
}

// parseDate converts "2006-01-02" or "20060102" to "20060102".
func parseDate(s string) string {
	s = strings.ReplaceAll(s, "-", "")
	if len(s) == 8 {
		return s
	}
	return todayYMD()
}

// parseLeg decodes one "uid|runDate|originCRS|destCRS" string.
type Leg struct {
	UID    string
	Date   string // YYYYMMDD
	Origin string // CRS
	Dest   string // CRS
}

func parseLeg(s string) (Leg, error) {
	parts := strings.SplitN(s, "|", 4)
	if len(parts) != 4 {
		return Leg{}, fmt.Errorf("invalid leg %q", s)
	}
	return Leg{UID: parts[0], Date: parts[1], Origin: parts[2], Dest: parts[3]}, nil
}

func parseLegs(raw string) ([]Leg, error) {
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	legs := make([]Leg, 0, len(parts))
	for _, p := range parts {
		l, err := parseLeg(strings.TrimSpace(p))
		if err != nil {
			return nil, err
		}
		legs = append(legs, l)
	}
	return legs, nil
}

func encodeLeg(l Leg) string {
	return l.UID + "|" + l.Date + "|" + l.Origin + "|" + l.Dest
}

func encodeLegs(legs []Leg) string {
	parts := make([]string, len(legs))
	for i, l := range legs {
		parts[i] = encodeLeg(l)
	}
	return strings.Join(parts, ",")
}

// -- view models --

type SearchPage struct {
	Stations []Station
}

type DeparturesFragment struct {
	Origin     string
	OriginName string
	Date       string
	After      string // HHMM - only show departures at/after this time
	Legs       string // accumulated legs param
	Services   []ServiceRow
}

type ServiceRow struct {
	UID           string
	Date          string
	TrainID       string
	Operator      string
	DestName      string
	Platform      string
	Booked        string // HHMM
	Realtime      string // HHMM
	DelayMins     int
	Status        string // "on-time" | "delayed" | "cancelled"
}

type CallingFragment struct {
	UID         string
	Date        string
	TrainID     string
	Operator    string
	OriginCRS   string // current leg origin
	Legs        string // accumulated legs param (does NOT yet include current leg)
	Points      []CallPoint
}

type CallPoint struct {
	CRS        string
	Name       string
	PublicTime string // HHMM scheduled
	Realtime   string // HHMM realtime
	DisplayAs  string
}

type JourneyPage struct {
	Legs         []LegCard
	LegsParam    string
	Stations     []Station
	ContinueDate string // YYYY-MM-DD, pre-filled from last leg arrival
	ContinueTime string // HH:MM, pre-filled from last leg arrival
}

// NextDep is an alternative departure time for a tight-connection leg.
type NextDep struct {
	UID      string // service UID, for leg card rendering
	Date     string // YYYYMMDD run date
	Time     string // HH:MM
	IsGood   bool   // true if ≥10 min buffer from interchange arrival
	AutoLoad bool   // auto-trigger HTMX fetch on page render
	Status   string // "on-time", "cancelled", "departed"
}

type LegCard struct {
	N                int
	LegsParam        string
	UID              string
	Date             string
	TrainID          string
	Operator         string
	OriginName       string
	DestName         string
	DestCRS          string // CRS code of destination, for departure filtering
	DepBooked        string
	DepRealtime      string
	ArrBooked        string
	ArrRealtime      string
	Platform         string
	DelayMins        int
	Status           string // "on-time" | "delayed" | "cancelled" | "missed"
	CancelReason     string // human-readable cancellation reason, if known
	Completed        bool   // true when arrival time has passed
	TightConnection  bool      // true when gap from prior leg is ≤ 10 min
	ConnectionMins   int       // minutes available for the connection
	NextDeps         []NextDep // alternative departures from interchange
	RemoveLegsParam  string    // encoded legs without this one, empty if last leg
	InterchangeCRS   string    // CRS of interchange station (origin of this leg)
	InterchangeDate  string
	InterchangeAfter string // HHMM arrival of prior leg at interchange
	PriorLegsParam   string // encoded legs before this one, for departure navigation
	DestLongCode     string // RTT longCode for dest, for departure filtering
	DestPlatform     string
	InTransit        InTransitInfo
}

type InTransitInfo struct {
	Active            bool
	LastStopName      string
	NextStopName      string
	NextStopTime      string
	NextStopDelayMins int    // negative = early vs schedule; positive = late
	NextStopPlatform  string
	RunningDelayMins  int    // lateness at last actual stop; negative = early
	StopsRemaining    int    // from next stop to destination, inclusive
	ProgressPercent   int    // 4–100, based on actual dep vs scheduled arr
}

// -- handlers --

// GET /
func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.render(w, "base.html", SearchPage{Stations: stations})
}

func serviceToRow(svc Service, minTime, toName string) (ServiceRow, bool) {
	if !svc.ScheduleMeta.InPassengerService {
		return ServiceRow{}, false
	}
	td := svc.TemporalData
	if td.DisplayAs == "CANCELLED_CALL" || td.Departure == nil || td.Departure.IsCancelled {
		return ServiceRow{}, false
	}
	booked := isoToHHMM(td.Departure.ScheduleAdvertised)
	dep := isoToHHMM(td.Departure.RealtimeForecast)
	if dep == "" {
		dep = booked
	}
	if dep < minTime {
		return ServiceRow{}, false
	}
	destName := ""
	if len(svc.Destination) > 0 {
		destName = svc.Destination[0].Location.Description
	}
	if toName != "" && strings.ToLower(destName) != toName {
		return ServiceRow{}, false
	}
	delay := delayMins(booked, dep)
	return ServiceRow{
		UID:       svc.ScheduleMeta.Identity,
		Date:      runDateToYMD(svc.ScheduleMeta.DepartureDate),
		TrainID:   svc.ScheduleMeta.TrainReportingIdentity,
		Operator:  svc.ScheduleMeta.Operator.Name,
		DestName:  destName,
		Platform:  svc.LocationMeta.Platform.Planned,
		Booked:    fmtTime(booked),
		Realtime:  fmtTime(dep),
		DelayMins: delay,
		Status:    statusLabel(td.DisplayAs, delay),
	}, true
}

func buildServiceRows(services []Service, minTime, to string) []ServiceRow {
	toName := strings.ToLower(stationName(to)) // "" when no dest filter
	rows := make([]ServiceRow, 0, 10)
	for _, svc := range services {
		row, ok := serviceToRow(svc, minTime, toName)
		if !ok {
			continue
		}
		rows = append(rows, row)
		if len(rows) >= 8 {
			break
		}
	}
	return rows
}

// GET /departures?origin=SHF&date=20260510&time=0900&after=0945&legs=...
func (s *server) handleDepartures(w http.ResponseWriter, r *http.Request) {
	origin := strings.ToUpper(r.URL.Query().Get("origin"))
	dateRaw := r.URL.Query().Get("date")
	timeRaw := r.URL.Query().Get("time")
	after := strings.ReplaceAll(r.URL.Query().Get("after"), ":", "")
	legsParam := r.URL.Query().Get("legs")
	to := strings.ToUpper(r.URL.Query().Get("to"))

	if origin == "" {
		slog.Warn("departures: missing origin", "url", r.URL.String())
		http.Error(w, "origin required", http.StatusBadRequest)
		return
	}

	date := parseDate(dateRaw)
	if date == "" {
		date = todayYMD()
	}

	// Use "after" as the minimum departure time; fall back to time param or now.
	minTime := after
	if minTime == "" {
		minTime = strings.ReplaceAll(timeRaw, ":", "")
		if minTime == "" {
			minTime = nowHHMM()
		}
	}

	sr, err := s.rtt.SearchDepartures(origin, date, minTime, to)
	if err != nil {
		slog.Error("departures fetch", "origin", origin, "err", err)
		http.Error(w, "could not fetch departures", http.StatusBadGateway)
		return
	}

	rows := buildServiceRows(sr.Services, minTime, to)

	s.render(w, "departures.html", DeparturesFragment{
		Origin:     origin,
		OriginName: stationName(origin),
		Date:       date,
		After:      fmtTime(after),
		Legs:       legsParam,
		Services:   rows,
	})
}

// GET /calling-points/{uid}/{date}?originCRS=SHF&legs=...
func (s *server) handleCallingPoints(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	uid := vars["uid"]
	date := vars["date"]
	originCRS := strings.ToUpper(r.URL.Query().Get("originCRS"))
	legsParam := r.URL.Query().Get("legs")

	svc, err := s.rtt.GetService(uid, date)
	if err != nil {
		slog.Error("service fetch", "uid", uid, "err", err)
		http.Error(w, "could not fetch service", http.StatusBadGateway)
		return
	}

	// Only show calling points *after* the origin.
	started := originCRS == ""
	points := make([]CallPoint, 0, len(svc.Locations))
	for _, loc := range svc.Locations {
		if !started {
			if loc.CRS() == originCRS {
				started = true
			}
			continue
		}
		switch loc.TemporalData.DisplayAs {
		case "PASS", "CANCELLED", "CANCELLED_CALL":
			continue
		}
		booked, rt := "", ""
		if loc.TemporalData.Arrival != nil {
			booked = isoToHHMM(loc.TemporalData.Arrival.ScheduleAdvertised)
			rt = isoToHHMM(loc.TemporalData.Arrival.RealtimeForecast)
		}
		if rt == "" {
			rt = booked
		}
		points = append(points, CallPoint{
			CRS:        loc.CRS(),
			Name:       loc.Location.Description,
			PublicTime: fmtTime(booked),
			Realtime:   fmtTime(rt),
			DisplayAs:  loc.TemporalData.DisplayAs,
		})
	}

	s.render(w, "calling.html", CallingFragment{
		UID:       uid,
		Date:      date,
		TrainID:   svc.ScheduleMeta.TrainReportingIdentity,
		Operator:  svc.ScheduleMeta.Operator.Name,
		OriginCRS: originCRS,
		Legs:      legsParam,
		Points:    points,
	})
}

// yyyymmddToDash converts "20260511" to "2026-05-11" for HTML date inputs.
func yyyymmddToDash(s string) string {
	if len(s) == 8 {
		return s[:4] + "-" + s[4:6] + "-" + s[6:]
	}
	return s
}

// GET /journey?legs=uid|date|origin|dest,...
func (s *server) handleJourney(w http.ResponseWriter, r *http.Request) {
	legsParam := r.URL.Query().Get("legs")
	legs, err := parseLegs(legsParam)
	if err != nil || len(legs) == 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	cards := s.buildLegCards(legs, legsParam)
	page := JourneyPage{Legs: cards, LegsParam: legsParam, Stations: stations}
	if last := cards[len(cards)-1]; last.ArrRealtime != "" {
		page.ContinueDate = yyyymmddToDash(legs[len(legs)-1].Date)
		page.ContinueTime = last.ArrRealtime
	}
	s.render(w, "journey.html", page)
}

// GET /leg/{n}?legs=...
func (s *server) handleLeg(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	n, err := strconv.Atoi(vars["n"])
	legsParam := r.URL.Query().Get("legs")
	legs, parseErr := parseLegs(legsParam)
	if err != nil || parseErr != nil || n < 0 || n >= len(legs) {
		http.Error(w, "invalid leg", http.StatusBadRequest)
		return
	}
	cards := s.buildLegCards(legs, legsParam)
	s.render(w, "leg.html", cards[n])
}

func (s *server) buildLegCards(legs []Leg, legsParam string) []LegCard {
	cards := make([]LegCard, len(legs))
	now := time.Now()
	for i, leg := range legs {
		svc, err := s.rtt.GetService(leg.UID, leg.Date)
		card := LegCard{
			N:         i,
			LegsParam: legsParam,
			UID:       leg.UID,
			Date:      leg.Date,
		}
		if err != nil {
			slog.Error("leg fetch", "uid", leg.UID, "err", err)
			card.Status = "error"
			cards[i] = card
			continue
		}

		card.TrainID = svc.ScheduleMeta.TrainReportingIdentity
		card.Operator = svc.ScheduleMeta.Operator.Name
		card.OriginName = stationName(leg.Origin)
		card.DestName = stationName(leg.Dest)
		card.DestCRS = leg.Dest

		for _, loc := range svc.Locations {
			if loc.CRS() == leg.Origin {
				booked, rt := "", ""
				if loc.TemporalData.Departure != nil {
					booked = isoToHHMM(loc.TemporalData.Departure.ScheduleAdvertised)
					rt = bestTime(loc.TemporalData.Departure)
				}
				if rt == "" {
					rt = booked
				}
				card.DepBooked = fmtTime(booked)
				card.DepRealtime = fmtTime(rt)
				card.Platform = bestPlatform(loc.LocationMeta)
				card.DelayMins = delayMins(booked, rt)
			}
			if loc.CRS() == leg.Dest {
				booked, rt := "", ""
				if loc.TemporalData.Arrival != nil {
					booked = isoToHHMM(loc.TemporalData.Arrival.ScheduleAdvertised)
					rt = bestTime(loc.TemporalData.Arrival)
				}
				if rt == "" {
					rt = booked
				}
				card.ArrBooked = fmtTime(booked)
				card.ArrRealtime = fmtTime(rt)
				card.DestPlatform = bestPlatform(loc.LocationMeta)
				if len(loc.Location.LongCodes) > 0 {
					card.DestLongCode = loc.Location.LongCodes[0]
				}
			}
		}

		card.InTransit = buildInTransitInfo(svc, leg.Origin, leg.Dest)

		// Determine status
		depHHMM := strings.ReplaceAll(card.DepRealtime, ":", "")
		currentTime := now.Format("1504")
		switch {
		case isCancelled(svc, leg.Origin) || isDestCancelled(svc, leg.Dest):
			card.Status = "cancelled"
			card.CancelReason = cancelReason(svc)
		case depHHMM != "" && depHHMM < currentTime && !card.isActualDep(svc, leg.Origin):
			card.Status = "missed"
		case card.DelayMins > 0:
			card.Status = "delayed"
		default:
			card.Status = "on-time"
		}

		cards[i] = card
	}

	for i := range cards {
		other := make([]Leg, 0, len(legs)-1)
		other = append(other, legs[:i]...)
		other = append(other, legs[i+1:]...)
		cards[i].RemoveLegsParam = encodeLegs(other)
	}

	s.annotateTightConnections(cards, legs)
	s.annotateMissedLegs(cards, legs, now.Format("1504"))
	return cards
}

func (s *server) annotateTightConnections(cards []LegCard, legs []Leg) {
	for i := 0; i < len(cards)-1; i++ {
		arr := strings.ReplaceAll(cards[i].ArrRealtime, ":", "")
		dep := strings.ReplaceAll(cards[i+1].DepRealtime, ":", "")
		connMins := hhmm2mins(dep) - hhmm2mins(arr)
		if arr == "" || dep == "" || connMins > 10 {
			continue
		}
		cards[i+1].TightConnection = true
		cards[i+1].ConnectionMins = connMins
		cards[i+1].InterchangeCRS = legs[i+1].Origin
		cards[i+1].InterchangeDate = legs[i+1].Date
		cards[i+1].InterchangeAfter = arr
		cards[i+1].PriorLegsParam = encodeLegs(legs[:i+1])
		cards[i+1].NextDeps = s.nextDeparturesAfter(legs[i+1].Origin, legs[i+1].Date, arr, dep, legs[i+1].Dest, cards[i+1].DestLongCode)
	}
}

// annotateMissedLegs populates NextDeps for legs that are "missed" but not
// already handled as tight connections, so the template can display the next
// available train to the destination below the card.
func (s *server) annotateMissedLegs(cards []LegCard, legs []Leg, nowHHMM string) {
	for i := range cards {
		if (cards[i].Status != "missed" && cards[i].Status != "cancelled") || cards[i].TightConnection {
			continue
		}
		depHHMM := strings.ReplaceAll(cards[i].DepRealtime, ":", "")
		cards[i].InterchangeCRS = legs[i].Origin
		cards[i].InterchangeDate = legs[i].Date
		cards[i].InterchangeAfter = nowHHMM
		if i > 0 {
			cards[i].PriorLegsParam = encodeLegs(legs[:i])
		}
		deps := s.nextDeparturesAfter(legs[i].Origin, legs[i].Date, nowHHMM, depHHMM, legs[i].Dest, cards[i].DestLongCode)
		// For a missed standalone leg the first on-time train is the right choice.
		for j := range deps {
			if deps[j].Status == "on-time" {
				deps[j].IsGood = true
				break
			}
		}
		cards[i].NextDeps = deps
	}
}

// nextDeparturesAfter returns alternative departures from crs after arrHHMM toward destCRS.
// Cancelled trains are included with Status="cancelled"; trains that departed before skipHHMM
// get Status="departed". If the initial window contains no alternatives (e.g. the booked train
// was the only match), a second query from skipHHMM finds the next available train.
func (s *server) nextDeparturesAfter(crs, date, arrHHMM, skipHHMM, destCRS, destLongCode string) []NextDep {
	sr, err := s.rtt.SearchDepartures(crs, date, arrHHMM, destCRS)
	if err != nil {
		return nil
	}
	arrMins := hhmm2mins(arrHHMM)
	deps := filterDeps(sr, skipHHMM, destCRS, destLongCode, arrMins)
	if len(deps) == 0 && skipHHMM != "" {
		// Initial window had no alternatives — re-query from the booked departure to find the next train.
		sr2, err := s.rtt.SearchDepartures(crs, date, skipHHMM, destCRS)
		if err == nil {
			deps = filterDeps(sr2, skipHHMM, destCRS, destLongCode, arrMins)
		}
	}
	return deps
}

func filterDeps(sr *SearchResponse, skipHHMM, destCRS, destLongCode string, arrMins int) []NextDep {
	var deps []NextDep
	for _, svc := range sr.Services {
		if !svc.ScheduleMeta.InPassengerService || !serviceCallsAt(svc, destCRS, destLongCode) {
			continue
		}
		dep, status := depStatus(svc.TemporalData)
		if dep == "" || dep == skipHHMM {
			continue
		}
		if status != "cancelled" && dep < skipHHMM {
			status = "departed"
		}
		buffer := hhmm2mins(dep) - arrMins
		deps = append(deps, NextDep{
			UID:    svc.ScheduleMeta.Identity,
			Date:   runDateToYMD(svc.ScheduleMeta.DepartureDate),
			Time:   fmtTime(dep),
			IsGood: buffer >= 10 && status == "on-time",
			Status: status,
		})
	}
	return deps
}

// serviceCallsAt reports whether svc's final destination matches destCRS (via shortCodes)
// or destLongCode (via longCodes). The RTT search response omits shortCodes from destination
// locations, so longCode matching is the primary path for real API data.
func serviceCallsAt(svc Service, destCRS, destLongCode string) bool {
	for _, stop := range svc.Destination {
		for _, code := range stop.Location.ShortCodes {
			if code == destCRS {
				return true
			}
		}
		if destLongCode != "" {
			for _, code := range stop.Location.LongCodes {
				if code == destLongCode {
					return true
				}
			}
		}
	}
	return false
}

// depStatus returns the departure HHMM and status ("on-time" or "cancelled")
// for a service. Cancelled services return their scheduled time so they remain
// visible to the user.
func depStatus(td ServiceTemporalData) (string, string) {
	if td.DisplayAs == "CANCELLED_CALL" || (td.Departure != nil && td.Departure.IsCancelled) {
		if td.Departure == nil {
			return "", ""
		}
		return isoToHHMM(td.Departure.ScheduleAdvertised), "cancelled"
	}
	if td.Departure == nil {
		return "", ""
	}
	if dep := isoToHHMM(td.Departure.RealtimeForecast); dep != "" {
		return dep, "on-time"
	}
	return isoToHHMM(td.Departure.ScheduleAdvertised), "on-time"
}

// effectiveDep returns the realtime departure HHMM for a service at the
// queried station, or "" if the service is cancelled or has no departure.
func effectiveDep(td ServiceTemporalData) string {
	if td.DisplayAs == "CANCELLED_CALL" || td.Departure == nil || td.Departure.IsCancelled {
		return ""
	}
	if dep := isoToHHMM(td.Departure.RealtimeForecast); dep != "" {
		return dep
	}
	return isoToHHMM(td.Departure.ScheduleAdvertised)
}


func (c *LegCard) isActualDep(svc *ServiceResponse, crs string) bool {
	for _, loc := range svc.Locations {
		if loc.CRS() == crs {
			dep := loc.TemporalData.Departure
			return dep != nil && (dep.RealtimeActual != "" || dep.RealtimeForecast != "")
		}
	}
	return false
}

func bestTime(tp *TemporalPoint) string {
	if tp == nil {
		return ""
	}
	if t := isoToHHMM(tp.RealtimeActual); t != "" {
		return t
	}
	return isoToHHMM(tp.RealtimeForecast)
}

func isCancelled(svc *ServiceResponse, crs string) bool {
	for _, loc := range svc.Locations {
		if loc.CRS() == crs {
			return loc.TemporalData.DisplayAs == "CANCELLED_CALL" ||
				(loc.TemporalData.Departure != nil && loc.TemporalData.Departure.IsCancelled)
		}
	}
	return false
}

func isDestCancelled(svc *ServiceResponse, crs string) bool {
	for _, loc := range svc.Locations {
		if loc.CRS() == crs {
			return loc.TemporalData.DisplayAs == "CANCELLED" ||
				loc.TemporalData.DisplayAs == "CANCELLED_CALL" ||
				(loc.TemporalData.Arrival != nil && loc.TemporalData.Arrival.IsCancelled) ||
				// Train terminated early here: departure is cancelled but arrival was not (short-forming)
				(loc.TemporalData.DisplayAs == "TERMINATES" && loc.TemporalData.Departure != nil && loc.TemporalData.Departure.IsCancelled)
		}
	}
	return false
}

func cancelReason(svc *ServiceResponse) string {
	if len(svc.Reasons) > 0 {
		if svc.Reasons[0].LongText != "" {
			return svc.Reasons[0].LongText
		}
		return delayCauses[svc.Reasons[0].Code]
	}
	return ""
}

func hasActualTime(loc ServiceLocation) bool {
	dep, arr := loc.TemporalData.Departure, loc.TemporalData.Arrival
	return (dep != nil && dep.RealtimeActual != "") || (arr != nil && arr.RealtimeActual != "")
}

func firstActualTime(loc ServiceLocation) string {
	if t := bestTime(loc.TemporalData.Arrival); t != "" {
		return fmtTime(t)
	}
	return fmtTime(bestTime(loc.TemporalData.Departure))
}

// findSegment returns the slice of locations from originCRS to destCRS (inclusive),
// or nil if either CRS is not found or origin comes after dest.
func findSegment(svc *ServiceResponse, originCRS, destCRS string) []ServiceLocation {
	originIdx, destIdx := -1, -1
	for i, loc := range svc.Locations {
		if loc.CRS() == originCRS {
			originIdx = i
		}
		if loc.CRS() == destCRS {
			destIdx = i
		}
	}
	if originIdx < 0 || destIdx < 0 || originIdx >= destIdx {
		return nil
	}
	return svc.Locations[originIdx : destIdx+1]
}

// signedRunningDelay returns the departure (or arrival) lateness in minutes for
// a location where actual times are recorded. Negative means early.
func signedRunningDelay(loc ServiceLocation) int {
	tp := loc.TemporalData.Departure
	if tp == nil {
		tp = loc.TemporalData.Arrival
	}
	if tp == nil {
		return 0
	}
	b := hhmm2mins(isoToHHMM(tp.ScheduleAdvertised))
	r := hhmm2mins(isoToHHMM(tp.RealtimeActual))
	if b < 0 || r < 0 {
		return 0
	}
	return r - b
}

// signedArrivalDelay returns the arrival lateness in minutes for an upcoming
// stop using the forecast time vs schedule. Negative means early.
func signedArrivalDelay(loc ServiceLocation) int {
	arr := loc.TemporalData.Arrival
	if arr == nil {
		return 0
	}
	b := hhmm2mins(isoToHHMM(arr.ScheduleAdvertised))
	rt := hhmm2mins(bestTime(arr))
	if b < 0 || rt < 0 {
		return 0
	}
	return rt - b
}

func buildInTransitInfo(svc *ServiceResponse, originCRS, destCRS string) InTransitInfo {
	segment := findSegment(svc, originCRS, destCRS)
	if segment == nil {
		return InTransitInfo{}
	}

	// Not in transit if origin hasn't actually departed.
	originDep := segment[0].TemporalData.Departure
	if originDep == nil || originDep.RealtimeActual == "" {
		return InTransitInfo{}
	}
	// Not in transit if destination has already recorded actual arrival.
	destArr := segment[len(segment)-1].TemporalData.Arrival
	if destArr != nil && destArr.RealtimeActual != "" {
		return InTransitInfo{}
	}

	// Walk segment to find the last stop with any actual time recorded.
	lastIdx := 0
	for i, loc := range segment {
		if hasActualTime(loc) {
			lastIdx = i
		}
	}

	info := InTransitInfo{
		Active:           true,
		LastStopName:     segment[lastIdx].Location.Description,
		RunningDelayMins: signedRunningDelay(segment[lastIdx]),
	}

	nextIdx := lastIdx + 1
	if nextIdx >= len(segment) {
		return info
	}
	next := segment[nextIdx]
	info.NextStopName = next.Location.Description
	info.NextStopTime = firstActualTime(next)
	info.NextStopPlatform = bestPlatform(next.LocationMeta)
	info.NextStopDelayMins = signedArrivalDelay(next)
	info.StopsRemaining = len(segment) - nextIdx

	depMins := hhmm2mins(isoToHHMM(originDep.RealtimeActual))
	if depMins >= 0 && destArr != nil {
		arrTime := isoToHHMM(destArr.RealtimeForecast)
		if arrTime == "" {
			arrTime = isoToHHMM(destArr.ScheduleAdvertised)
		}
		if arrMins := hhmm2mins(arrTime); arrMins > depMins {
			now := time.Now()
			pct := (now.Hour()*60+now.Minute()-depMins) * 100 / (arrMins - depMins)
			if pct < 4 {
				pct = 4
			} else if pct > 100 {
				pct = 100
			}
			info.ProgressPercent = pct
		}
	}

	return info
}

// delayMins computes delay in minutes between booked and realtime HHMM strings.
func delayMins(booked, realtime string) int {
	b := hhmm2mins(booked)
	r := hhmm2mins(realtime)
	if b < 0 || r < 0 {
		return 0
	}
	d := r - b
	if d < 0 {
		return 0
	}
	return d
}

func hhmm2mins(hhmm string) int {
	hhmm = strings.ReplaceAll(hhmm, ":", "")
	if len(hhmm) != 4 {
		return -1
	}
	h, err1 := strconv.Atoi(hhmm[:2])
	m, err2 := strconv.Atoi(hhmm[2:])
	if err1 != nil || err2 != nil {
		return -1
	}
	return h*60 + m
}

func statusLabel(displayAs string, delayMins int) string {
	if displayAs == "CANCELLED_CALL" {
		return "cancelled"
	}
	if delayMins > 0 {
		return "delayed"
	}
	return "on-time"
}

// fmtTime formats "HHMM" as "HH:MM", passing through anything already formatted.
func fmtTime(t string) string {
	t = strings.ReplaceAll(t, ":", "")
	if len(t) != 4 {
		return t
	}
	return t[:2] + ":" + t[2:]
}
