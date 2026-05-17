package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)


const cacheTTL = 25 * time.Second

type RTTClient struct {
	dataURL      string
	refreshToken string // long-lived token from .env, used only to obtain access tokens
	debug        bool

	tokenMu     sync.Mutex
	accessToken string
	tokenExpiry time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	body []byte
	at   time.Time
}

func NewRTTClient(dataURL, refreshToken string, debug bool) *RTTClient {
	return &RTTClient{
		dataURL:      dataURL,
		refreshToken: refreshToken,
		debug:        debug,
		cache:        make(map[string]cacheEntry),
	}
}

// accessTokenResponse matches GET /api/get_access_token response.
// Spec: https://realtimetrains.github.io/api-specification/specification/main.yml
type accessTokenResponse struct {
	Token      string `json:"token"`
	ValidUntil string `json:"validUntil"` // ISO 8601
}

// bearerToken returns a valid short-lived access token, refreshing if needed.
// Spec: https://realtimetrains.github.io/api-specification/specification/main.yml
//
//	GET /api/get_access_token  — Authorization: Bearer {refreshToken}
func (c *RTTClient) bearerToken() (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.accessToken != "" && time.Until(c.tokenExpiry) > 60*time.Second {
		return c.accessToken, nil
	}

	req, err := http.NewRequest(http.MethodGet, c.dataURL+"/api/get_access_token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.refreshToken)
	req.Header.Set("Accept", "application/json")

	slog.Debug("RTT token refresh")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token refresh: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token refresh %s: %s", resp.Status, body)
	}

	var tr accessTokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("token parse: %w", err)
	}

	expiry, err := time.Parse(time.RFC3339, tr.ValidUntil)
	if err != nil {
		expiry = time.Now().Add(5 * time.Minute)
	}
	c.accessToken = tr.Token
	c.tokenExpiry = expiry
	slog.Debug("RTT token refreshed", "validUntil", tr.ValidUntil)
	return c.accessToken, nil
}

func (c *RTTClient) get(path string) ([]byte, error) {
	c.mu.Lock()
	if e, ok := c.cache[path]; ok && time.Since(e.at) < cacheTTL {
		c.mu.Unlock()
		slog.Debug("cache hit", "path", path)
		return e.body, nil
	}
	c.mu.Unlock()

	token, err := c.bearerToken()
	if err != nil {
		return nil, err
	}

	reqURL := c.dataURL + path
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	slog.Debug("RTT fetch", "url", reqURL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("RTT %s: %s: %s", reqURL, resp.Status, body)
	}

	c.mu.Lock()
	c.cache[path] = cacheEntry{body: body, at: time.Now()}
	c.mu.Unlock()

	return body, nil
}

// SearchDepartures returns departures from a station.
// fromTime is HHMM (e.g. "0900"); defaults to now if empty.
// Spec: https://realtimetrains.github.io/api-specification/specification/main.yml line ~993
//
//	GET /gb-nr/location?code={crs}&timeFrom={ISO8601}
func (c *RTTClient) SearchDepartures(crs, date, fromTime, to string) (*SearchResponse, error) {
	q := url.Values{}
	q.Set("code", crs)
	q.Set("timeFrom", ymdHHMMtoISO(date, fromTime))
	if to != "" {
		q.Set("to", to)
	}
	body, err := c.get("/gb-nr/location?" + q.Encode())
	if err != nil {
		return nil, err
	}
	if c.debug {
		os.WriteFile("debug.json", body, 0644)
	}
	var sr SearchResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, err
	}
	return &sr, nil
}

// GetService returns full calling-point detail for a service.
// Spec: https://realtimetrains.github.io/api-specification/specification/main.yml line ~1146
//
//	GET /gb-nr/service?identity={uid}&departureDate={YYYY-MM-DD}
func (c *RTTClient) GetService(uid, date string) (*ServiceResponse, error) {
	q := url.Values{}
	q.Set("identity", uid)
	q.Set("departureDate", ymdToDash(date))
	body, err := c.get("/gb-nr/service?" + q.Encode())
	if err != nil {
		return nil, err
	}
	if c.debug {
		os.WriteFile("debug_service.json", body, 0644)
	}
	var wrapper ServiceAPIResponse
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil, err
	}
	return &wrapper.Service, nil
}

// ymdToDash converts "YYYYMMDD" → "YYYY-MM-DD".
func ymdToDash(date string) string {
	if len(date) != 8 {
		return date
	}
	return date[:4] + "-" + date[4:6] + "-" + date[6:]
}

// ymdHHMMtoISO converts "YYYYMMDD" + "HHMM" → "YYYY-MM-DDTHH:MM:00".
// If hhmm is empty it uses the current time.
func ymdHHMMtoISO(date, hhmm string) string {
	d := ymdToDash(date)
	if len(hhmm) == 4 {
		return d + "T" + hhmm[:2] + ":" + hhmm[2:] + ":00"
	}
	return d + "T" + time.Now().Format("15:04:05")
}

// --- RTT JSON types ---

type SearchResponse struct {
	Query    SearchQuery `json:"query"`
	Services []Service   `json:"services"`
}

type SearchQuery struct {
	Location QueryLocation `json:"location"`
}

type QueryLocation struct {
	Description string   `json:"description"`
	ShortCodes  []string `json:"shortCodes"`
}

type Service struct {
	TemporalData ServiceTemporalData `json:"temporalData"`
	LocationMeta ServiceLocationMeta `json:"locationMetadata"`
	ScheduleMeta ServiceScheduleMeta `json:"scheduleMetadata"`
	Origin       []StationStop       `json:"origin"`
	Destination  []StationStop       `json:"destination"`
}

type ServiceTemporalData struct {
	Arrival   *TemporalPoint `json:"arrival"`
	Departure *TemporalPoint `json:"departure"`
	DisplayAs string         `json:"displayAs"`
}

type TemporalPoint struct {
	ScheduleAdvertised string `json:"scheduleAdvertised"`
	RealtimeForecast   string `json:"realtimeForecast"`
	RealtimeActual     string `json:"realtimeActual"`
	IsCancelled        bool   `json:"isCancelled"`
}

type ServiceLocationMeta struct {
	Platform struct {
		Planned  string `json:"planned"`
		Actual   string `json:"actual"`
		Forecast string `json:"forecast"`
	} `json:"platform"`
}

func bestPlatform(meta ServiceLocationMeta) string {
	if meta.Platform.Actual != "" {
		return meta.Platform.Actual
	}
	if meta.Platform.Forecast != "" {
		return meta.Platform.Forecast
	}
	return meta.Platform.Planned
}

type ServiceScheduleMeta struct {
	Identity               string   `json:"identity"`
	DepartureDate          string   `json:"departureDate"`
	Operator               Operator `json:"operator"`
	InPassengerService     bool     `json:"inPassengerService"`
	TrainReportingIdentity string   `json:"trainReportingIdentity"`
}

type Operator struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type StationStop struct {
	Location     StopLocation `json:"location"`
	TemporalData StopTemporal `json:"temporalData"`
}

type StopLocation struct {
	Description string   `json:"description"`
	ShortCodes  []string `json:"shortCodes"`
	LongCodes   []string `json:"longCodes"`
}

type StopTemporal struct {
	ScheduleAdvertised string `json:"scheduleAdvertised"`
}

// isoToHHMM extracts "HHMM" from an ISO 8601 datetime like "2026-05-11T09:00:00".
func isoToHHMM(iso string) string {
	if len(iso) >= 16 {
		return iso[11:13] + iso[14:16]
	}
	return ""
}

type ServiceAPIResponse struct {
	Service ServiceResponse `json:"service"`
}

type Reason struct {
	Type      string `json:"type"`
	Code      string `json:"code"`
	ShortText string `json:"shortText"`
	LongText  string `json:"longText"`
}

type ServiceResponse struct {
	ScheduleMeta ServiceScheduleMeta `json:"scheduleMetadata"`
	Locations    []ServiceLocation   `json:"locations"`
	Reasons      []Reason            `json:"reasons"`
}

type ServiceLocation struct {
	TemporalData ServiceTemporalData `json:"temporalData"`
	LocationMeta ServiceLocationMeta `json:"locationMetadata"`
	Location     StopLocation        `json:"location"`
}

func (l ServiceLocation) CRS() string {
	if len(l.Location.ShortCodes) > 0 {
		return l.Location.ShortCodes[0]
	}
	return ""
}

// runDateToYMD converts "2026-05-10" to "20260510".
func runDateToYMD(runDate string) string {
	if len(runDate) == 10 {
		return runDate[:4] + runDate[5:7] + runDate[8:]
	}
	return runDate
}
