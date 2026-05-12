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

type server struct {
	rtt  *RTTClient
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
	After      string // HHMM — only show departures at/after this time
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
	Legs []LegCard
}

type LegCard struct {
	N               int
	LegsParam       string
	UID             string
	Date            string
	TrainID         string
	Operator        string
	OriginName      string
	DestName        string
	DepBooked       string
	DepRealtime     string
	ArrBooked       string
	ArrRealtime     string
	Platform        string
	DelayMins       int
	Status          string // "on-time" | "delayed" | "cancelled" | "missed"
	TightConnection bool   // true when gap to next leg is ≤ 10 min
	ConnectionMins  int    // minutes available for the connection
	NextTrainDep    string // next available departure from interchange (HH:MM)
}

// -- handlers --

// GET /
func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.render(w, "base.html", SearchPage{Stations: stations})
}

// GET /departures?origin=SHF&date=20260510&time=0900&after=0945&legs=...
func (s *server) handleDepartures(w http.ResponseWriter, r *http.Request) {
	origin := strings.ToUpper(r.URL.Query().Get("origin"))
	dateRaw := r.URL.Query().Get("date")
	timeRaw := r.URL.Query().Get("time")
	after := strings.ReplaceAll(r.URL.Query().Get("after"), ":", "")
	legsParam := r.URL.Query().Get("legs")

	if origin == "" {
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

	sr, err := s.rtt.SearchDepartures(origin, date, minTime)
	if err != nil {
		slog.Error("departures fetch", "origin", origin, "err", err)
		http.Error(w, "could not fetch departures", http.StatusBadGateway)
		return
	}

	rows := make([]ServiceRow, 0, 10)
	for _, svc := range sr.Services {
		if !svc.ScheduleMeta.InPassengerService {
			continue
		}
		td := svc.TemporalData
		if td.DisplayAs == "CANCELLED_CALL" || td.Departure == nil || td.Departure.IsCancelled {
			continue
		}
		booked := isoToHHMM(td.Departure.ScheduleAdvertised)
		dep := isoToHHMM(td.Departure.RealtimeForecast)
		if dep == "" {
			dep = booked
		}
		if dep < minTime {
			continue
		}
		delay := delayMins(booked, dep)
		status := statusLabel(td.DisplayAs, delay)
		destName := ""
		if len(svc.Destination) > 0 {
			destName = svc.Destination[0].Location.Description
		}
		runDate := runDateToYMD(svc.ScheduleMeta.DepartureDate)
		rows = append(rows, ServiceRow{
			UID:       svc.ScheduleMeta.Identity,
			Date:      runDate,
			TrainID:   svc.ScheduleMeta.TrainReportingIdentity,
			Operator:  svc.ScheduleMeta.Operator.Name,
			DestName:  destName,
			Platform:  svc.LocationMeta.Platform.Planned,
			Booked:    fmtTime(booked),
			Realtime:  fmtTime(dep),
			DelayMins: delay,
			Status:    status,
		})
		if len(rows) >= 8 {
			break
		}
	}

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
		if loc.TemporalData.DisplayAs == "PASS" {
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

// GET /journey?legs=uid|date|origin|dest,...
func (s *server) handleJourney(w http.ResponseWriter, r *http.Request) {
	legsParam := r.URL.Query().Get("legs")
	legs, err := parseLegs(legsParam)
	if err != nil || len(legs) == 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	cards := s.buildLegCards(legs, legsParam)
	s.render(w, "journey.html", JourneyPage{Legs: cards})
}

// GET /leg/{n}?legs=...
func (s *server) handleLeg(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	n, _ := strconv.Atoi(vars["n"])
	legsParam := r.URL.Query().Get("legs")
	legs, err := parseLegs(legsParam)
	if err != nil || n < 0 || n >= len(legs) {
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

		for _, loc := range svc.Locations {
			if loc.CRS() == leg.Origin {
				booked, rt := "", ""
				if loc.TemporalData.Departure != nil {
					booked = isoToHHMM(loc.TemporalData.Departure.ScheduleAdvertised)
					rt = isoToHHMM(loc.TemporalData.Departure.RealtimeForecast)
				}
				if rt == "" {
					rt = booked
				}
				card.DepBooked = fmtTime(booked)
				card.DepRealtime = fmtTime(rt)
				card.Platform = loc.LocationMeta.Platform.Planned
				card.DelayMins = delayMins(booked, rt)
			}
			if loc.CRS() == leg.Dest {
				booked, rt := "", ""
				if loc.TemporalData.Arrival != nil {
					booked = isoToHHMM(loc.TemporalData.Arrival.ScheduleAdvertised)
					rt = isoToHHMM(loc.TemporalData.Arrival.RealtimeForecast)
				}
				if rt == "" {
					rt = booked
				}
				card.ArrBooked = fmtTime(booked)
				card.ArrRealtime = fmtTime(rt)
			}
		}

		// Determine status
		depHHMM := strings.ReplaceAll(card.DepRealtime, ":", "")
		nowHHMM := now.Format("1504")
		switch {
		case isCancelled(svc, leg.Origin):
			card.Status = "cancelled"
		case depHHMM != "" && depHHMM < nowHHMM && !card.isActualDep(svc, leg.Origin):
			card.Status = "missed"
		case card.DelayMins > 0:
			card.Status = "delayed"
		default:
			card.Status = "on-time"
		}

		cards[i] = card
	}

	s.annotateTightConnections(cards, legs)
	return cards
}

func (s *server) annotateTightConnections(cards []LegCard, legs []Leg) {
	for i := 0; i < len(cards)-1; i++ {
		arr := strings.ReplaceAll(cards[i].ArrRealtime, ":", "")
		dep := strings.ReplaceAll(cards[i+1].DepRealtime, ":", "")
		connMins := hhmm2mins(dep) - hhmm2mins(arr)
		if arr == "" || dep == "" || connMins < 0 || connMins > 10 {
			continue
		}
		cards[i].TightConnection = true
		cards[i].ConnectionMins = connMins
		cards[i].NextTrainDep = s.nextDepartureAfter(legs[i].Dest, legs[i].Date, arr, dep)
	}
}

// nextDepartureAfter returns the HH:MM of the first departure from crs on date
// that arrives after arrHHMM but is strictly later than skipHHMM (the booked connection).
func (s *server) nextDepartureAfter(crs, date, arrHHMM, skipHHMM string) string {
	sr, err := s.rtt.SearchDepartures(crs, date, arrHHMM)
	if err != nil {
		return ""
	}
	for _, svc := range sr.Services {
		if !svc.ScheduleMeta.InPassengerService {
			continue
		}
		td := svc.TemporalData
		if td.DisplayAs == "CANCELLED_CALL" || td.Departure == nil || td.Departure.IsCancelled {
			continue
		}
		dep := isoToHHMM(td.Departure.RealtimeForecast)
		if dep == "" {
			dep = isoToHHMM(td.Departure.ScheduleAdvertised)
		}
		if dep != "" && dep > skipHHMM {
			return fmtTime(dep)
		}
	}
	return ""
}

func (c *LegCard) isActualDep(svc *ServiceResponse, crs string) bool {
	for _, loc := range svc.Locations {
		if loc.CRS() == crs {
			return loc.TemporalData.Departure != nil && loc.TemporalData.Departure.RealtimeForecast != ""
		}
	}
	return false
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
