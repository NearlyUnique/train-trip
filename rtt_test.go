package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestCRS(t *testing.T) {
	with := ServiceLocation{Location: StopLocation{ShortCodes: []string{"SHF"}}}
	assert.Equal(t, "SHF", with.CRS())

	empty := ServiceLocation{Location: StopLocation{}}
	assert.Equal(t, "", empty.CRS())
}

func TestYmdHHMMtoISO(t *testing.T) {
	assert.Equal(t, "2026-05-11T09:00:00", ymdHHMMtoISO("20260511", "0900"))
	assert.Equal(t, "2026-05-11T23:59:00", ymdHHMMtoISO("20260511", "2359"))
	// empty hhmm falls back to current time — just check prefix
	result := ymdHHMMtoISO("20260511", "")
	assert.Contains(t, result, "2026-05-11T")
}

// newRTTTestServer starts a test HTTP server that handles token refresh and two API endpoints.
// tokenCalls counts how many times the token endpoint is hit (used to verify caching).
func newRTTTestServer(t *testing.T, tokenCalls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/get_access_token":
			if tokenCalls != nil {
				*tokenCalls++
			}
			assert.Equal(t, "Bearer test-refresh", r.Header.Get("Authorization"))
			json.NewEncoder(w).Encode(accessTokenResponse{
				Token:      "test-access",
				ValidUntil: time.Now().Add(10 * time.Minute).Format(time.RFC3339),
			})
		case "/gb-nr/location":
			assert.Equal(t, "Bearer test-access", r.Header.Get("Authorization"))
			json.NewEncoder(w).Encode(SearchResponse{})
		case "/gb-nr/service":
			assert.Equal(t, "Bearer test-access", r.Header.Get("Authorization"))
			json.NewEncoder(w).Encode(ServiceAPIResponse{
				Service: ServiceResponse{
					ScheduleMeta: ServiceScheduleMeta{TrainReportingIdentity: "1A23"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestRTTClient_SearchDepartures(t *testing.T) {
	ts := newRTTTestServer(t, nil)
	defer ts.Close()

	client := NewRTTClient(ts.URL, "test-refresh")
	sr, err := client.SearchDepartures("SHF", "20260511", "0900", "")
	require.NoError(t, err)
	assert.NotNil(t, sr)
}

func TestRTTClient_GetService(t *testing.T) {
	ts := newRTTTestServer(t, nil)
	defer ts.Close()

	client := NewRTTClient(ts.URL, "test-refresh")
	svc, err := client.GetService("A1", "20260511")
	require.NoError(t, err)
	assert.Equal(t, "1A23", svc.ScheduleMeta.TrainReportingIdentity)
}

func TestRTTClient_TokenCacheHit(t *testing.T) {
	// Two different search requests reuse the same bearer token (token cache hit in bearerToken).
	var tokenCalls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/get_access_token" {
			tokenCalls++
			json.NewEncoder(w).Encode(accessTokenResponse{
				Token:      "tok",
				ValidUntil: time.Now().Add(10 * time.Minute).Format(time.RFC3339),
			})
			return
		}
		json.NewEncoder(w).Encode(SearchResponse{})
	}))
	defer ts.Close()

	client := NewRTTClient(ts.URL, "test-refresh")
	_, err := client.SearchDepartures("SHF", "20260511", "0900", "") // fetches token
	require.NoError(t, err)
	_, err = client.SearchDepartures("MAN", "20260511", "0900", "") // reuses cached token
	require.NoError(t, err)
	assert.Equal(t, 1, tokenCalls)
}

func TestRTTClient_GetService_APIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get_access_token" {
			json.NewEncoder(w).Encode(accessTokenResponse{
				Token: "tok", ValidUntil: time.Now().Add(10 * time.Minute).Format(time.RFC3339),
			})
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	client := NewRTTClient(ts.URL, "ref")
	_, err := client.GetService("A1", "20260511")
	assert.Error(t, err)
}

func TestRTTClient_CacheHit(t *testing.T) {
	var hits int
	ts := newRTTTestServer(t, &hits)
	defer ts.Close()

	client := NewRTTClient(ts.URL, "test-refresh")
	_, err := client.SearchDepartures("SHF", "20260511", "0900", "")
	require.NoError(t, err)

	_, err = client.SearchDepartures("SHF", "20260511", "0900", "") // same request
	require.NoError(t, err)

	assert.Equal(t, 1, hits, "token should only be fetched once within TTL")
}

func TestRTTClient_TokenRefreshError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer ts.Close()

	client := NewRTTClient(ts.URL, "bad-token")
	_, err := client.SearchDepartures("SHF", "20260511", "0900", "")
	assert.Error(t, err)
}

func TestRTTClient_InvalidTokenJSON(t *testing.T) {
	// Token endpoint returns garbage → bearerToken JSON parse error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer ts.Close()

	client := NewRTTClient(ts.URL, "ref")
	_, err := client.SearchDepartures("SHF", "20260511", "0900", "")
	assert.Error(t, err)
}

func TestRTTClient_InvalidValidUntil(t *testing.T) {
	// Token is valid but ValidUntil can't be parsed → falls back to 5-min expiry, still works
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get_access_token" {
			json.NewEncoder(w).Encode(accessTokenResponse{Token: "tok", ValidUntil: "not-a-date"})
			return
		}
		json.NewEncoder(w).Encode(SearchResponse{})
	}))
	defer ts.Close()

	client := NewRTTClient(ts.URL, "ref")
	sr, err := client.SearchDepartures("SHF", "20260511", "0900", "")
	require.NoError(t, err)
	assert.NotNil(t, sr)
}

func TestRTTClient_InvalidSearchJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get_access_token" {
			json.NewEncoder(w).Encode(accessTokenResponse{
				Token: "tok", ValidUntil: time.Now().Add(10 * time.Minute).Format(time.RFC3339),
			})
			return
		}
		w.Write([]byte("not json"))
	}))
	defer ts.Close()

	client := NewRTTClient(ts.URL, "ref")
	_, err := client.SearchDepartures("SHF", "20260511", "0900", "")
	assert.Error(t, err)
}

func TestRTTClient_InvalidServiceJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get_access_token" {
			json.NewEncoder(w).Encode(accessTokenResponse{
				Token: "tok", ValidUntil: time.Now().Add(10 * time.Minute).Format(time.RFC3339),
			})
			return
		}
		w.Write([]byte("not json"))
	}))
	defer ts.Close()

	client := NewRTTClient(ts.URL, "ref")
	_, err := client.GetService("A1", "20260511")
	assert.Error(t, err)
}

func TestRTTClient_APIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get_access_token" {
			json.NewEncoder(w).Encode(accessTokenResponse{
				Token:      "tok",
				ValidUntil: time.Now().Add(10 * time.Minute).Format(time.RFC3339),
			})
			return
		}
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	client := NewRTTClient(ts.URL, "ref")
	_, err := client.SearchDepartures("SHF", "20260511", "0900", "")
	assert.Error(t, err)
}
