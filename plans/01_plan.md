# Train Trip Web App - Implementation Plan

## Context

A journey companion for use *during* a specific trip - not for ticket buying. The user builds their journey step-by-step directly from live RTT departure data: pick an origin, see which trains are leaving, select calling points as "change here" or "this is my destination." No external routing API - RTT calling points are the router.

---

## Journey Builder UX (HTMX-driven, zero JS)

```
1. GET /
   → origin input (datalist autocomplete from embedded station list)
   → date + time inputs (empty = now)
   → [Find trains] button

2. GET /departures?origin={crs}&date={date}&time={time}
   → HTMX fragment: list of next N departures from origin
   → each departure shows: headcode, destination, platform, scheduled time, status

3. User selects a departure → hx-get="/calling-points/{uid}/{date}"
   → HTMX fragment: the full calling-point list for that service
   → each calling point has two buttons:
       [Change here]          → hx-get="/departures?origin={crs}&after={arrival}&date={date}"
       [This is my destination]  → hx-get="/start-journey?legs=..."

4. If "Change here":
   → HTMX fragment: departures from that station after the arrival time
   → user picks next train, sees its calling points, repeats step 3

5. If "This is my destination":
   → full-page redirect to /journey?legs={encoded leg list}
   → active journey tracking begins
```

State accumulates in URL query params (`legs` = comma-separated `uid:date:origin:dest` tuples). No server-side session.

---

## File Structure (flat)

```
train-trip/
├── main.go          # server, config (PORT, RTT_TOKEN, DATA_URL), gorilla/mux routes
├── rtt.go           # RTT API client - search + service; in-memory cache
├── stations.go      # embedded UK station list (CRS → name) for datalist
├── handlers.go      # HTTP handlers - all HTMX fragments and full pages
├── templates.go     # go:embed of templates/
├── Makefile
├── static/
│   ├── uchu.css     # self-hosted uchu color_expanded.css
│   ├── style.css    # --uchu-* variable-based styles
│   └── htmx.min.js  # self-hosted HTMX
├── templates/
│   ├── base.html         # page shell
│   ├── search.html       # origin + date/time form with <datalist>
│   ├── departures.html   # HTMX fragment: departure list
│   ├── calling.html      # HTMX fragment: calling points with action buttons
│   ├── journey.html      # active journey full page
│   └── leg.html          # HTMX fragment: single leg card (polled every 30 s)
├── plans/
│   └── 01_plan.md
├── .env             # RTT_TOKEN, DATA_URL, PORT - no journey data
└── readme.md
```

## Dependencies

| Package | Justification |
|---|---|
| `github.com/gorilla/mux` | URL param routing - user-approved |
| All other: stdlib | `net/http`, `encoding/json`, `html/template`, `slog`, `sync`, `time` |

No other 3rd-party packages.

## Station Autocomplete

`stations.go` embeds a static list of UK station names + CRS codes (publicly available, ~2600 entries) as a Go string slice, rendered server-side into an HTML `<datalist>`. Input accepts station name; form value submits CRS code. No JavaScript needed - native browser `<datalist>` behaviour.

## RTT API Endpoints

Auth: `Authorization: Bearer $RTT_TOKEN`. Base URL: `$DATA_URL`.

| Purpose | Endpoint |
|---|---|
| Departures from a station (optionally time-windowed) | `GET /v1/json/search/{crs}` |
| Service calling points + realtime status | `GET /v1/json/service/{serviceUid}/{YYYY}/{MM}/{DD}` |

Key fields: `.services[].serviceUid`, `.runDate`, `.locationDetail.realtimeDeparture`, `.platform`, `.displayAs`, `.callingAt[].crs`, `.callingAt[].realtimeArrival`.

## In-Memory Cache

`sync.Mutex` + `map[string]{body []byte, at time.Time}`, TTL 25 s. Prevents duplicate RTT calls across HTMX interactions and open tabs. Stays inside the 30 req/min rate limit.

## HTTP Routes

| Route | Description |
|---|---|
| `GET /` | Search form (origin, date, time) |
| `GET /departures?origin={crs}&date={d}&time={t}[&after={t}&legs={...}]` | Departure list fragment |
| `GET /calling-points/{uid}/{date}?legs={...}` | Calling points for a service |
| `GET /journey?legs={...}` | Active journey tracking page |
| `GET /leg/{n}?legs={...}` | Single leg card fragment (polled 30 s) |
| `GET /static/*` | Static file server |

## Active Journey View

- Current time prominent at top
- One card per leg: train, platform, scheduled vs actual departure/arrival, delay badge
- Status colours:
  - On time → `--uchu-green-5`
  - Delayed (connection still makeable) → `--uchu-yellow-5`
  - Missed or cancelled → `--uchu-red-5` + next available departure shown
- Each card: `hx-get="/leg/{n}?..." hx-trigger="every 30s" hx-swap="outerHTML"`
- Zero custom JavaScript

## Build & Deploy

```makefile
build:
	go build -o bin/train-trip .

build-arm:
	GOOS=linux GOARCH=arm GOARM=6 go build -o bin/train-trip-arm .

run:
	go run .
```

Nginx proxies localhost:`$PORT`. `.env` never served.

## Verification

1. `go run .` → form loads; datalist shows station names with autocomplete
2. Enter origin + time → departure list renders; services show real RTT data
3. Select a service → calling points appear with action buttons
4. Press "Change here" → departure list from change station loads inline
5. Press "This is my destination" → redirects to `/journey?legs=...`
6. Journey view: leg cards render and auto-refresh every 30 s
7. Slog output shows cache hits on second poll (no duplicate RTT calls)
8. No credentials appear in page source
9. `make build-arm` succeeds
