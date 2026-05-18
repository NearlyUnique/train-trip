# Train journey leg card — HTML fragment template spec

## Purpose
A mobile-first card component displaying the status of a single leg of a rail journey. One card = one leg. Compose multiple cards vertically for multi-leg itineraries.

---

## Data model

```json
{
  "operator": {
    "name": "string",          // e.g. "South Western Railway"
    "brandColour": "string"    // hex, used for operator dot only
  },
  "status": "on_time | delayed | cancelled",
  "delayMinutes": "number | null",
  "origin": {
    "name": "string",
    "plannedDeparture": "HH:MM",
    "actualDeparture": "HH:MM | null",   // null if on time or cancelled
    "platform": "string | null"           // null = TBC
  },
  "destination": {
    "name": "string",
    "plannedArrival": "HH:MM",
    "estimatedArrival": "HH:MM | null",
    "platform": "string | null"
  },
  "trainPosition": {
    "state": "at_origin | en_route | at_intermediate | approaching_destination | arrived",
    "description": "string",             // human-readable, e.g. "Approaching Woking"
    "stopsRemaining": "number | null",
    "progressPercent": "number"          // 0–100, position on line
  },
  "disruption": {
    "reason": "string | null",           // short title, e.g. "Delayed — awaiting crew"
    "detail": "string | null"            // one or two sentence explanation
  }
}
```

---

## Card anatomy

```
┌─────────────────────────────────────────────┐
│ [●] Operator name            [STATUS BADGE] │  ← card-header
├─────────────────────────────────────────────┤
│ Origin name         ●        Destination    │
│ 09:42               │        11:33          │  ← route-section
│ Platform 3          ↓        Platform TBC   │
├─────────────────────────────────────────────┤
│ [DISRUPTION BANNER — only when delayed/     │  ← delay-banner (conditional)
│  cancelled]                                 │
├─────────────────────────────────────────────┤
│ Approaching Woking · 3 stops remaining      │
│ ████████░░░░░░  🚆                          │  ← progress-section (hide if cancelled)
│ Origin              Destination             │
└─────────────────────────────────────────────┘
```

---

## Status badge rules

| `status`    | Badge class         | Icon          | Text                |
|-------------|---------------------|---------------|---------------------|
| `on_time`   | `badge-on-time`     | ti-check      | "On time"           |
| `delayed`   | `badge-delayed`     | ti-clock      | "{N} min late"      |
| `cancelled` | `badge-cancelled`   | ti-x          | "Cancelled"         |

---

## Time display rules

- **On time**: show `time-planned` only (no strikethrough).
- **Delayed**: show `time-planned` with `class="time-planned strikethrough"` + `time-actual` in warning colour alongside.
- **Cancelled at origin**: show `time-planned` with strikethrough + `time-actual cancelled` displaying the word "Cancelled".
- **Platform unknown / TBC**: render `<span class="platform-num unknown">TBC</span>`.
- **Platform N/A (cancelled, no arrival)**: render `<span class="platform-num unknown">N/A</span>`.

---

## Progress bar rules

- Hide the entire `progress-section` when `status === "cancelled"`.
- `progressPercent` maps directly to the CSS `width` of `.progress-fill`.
- Minimum visible fill: 4% (keeps train icon at left edge when just departed).
- Train icon `🚆` is absolutely positioned at `left: {progressPercent}%` inside the bar wrapper.
- Bar fill colour:
  - On time → `var(--color-text-success)` at 70% opacity
  - Delayed → `var(--color-text-warning)` at 80% opacity
  - Cancelled → omit bar entirely

---

## Disruption banner rules

- Render only when `disruption.reason` is non-null.
- `status === "delayed"` → class `delay-banner warning`, icon `ti-alert-triangle`
- `status === "cancelled"` → class `delay-banner danger`, icon `ti-alert-circle`
- If `disruption.detail` is null, render `<strong>` title only (no body paragraph).

---

## Spine / route connector

The vertical spine between origin and destination uses:
- Top dot colour: success (on time), warning (delayed), danger (cancelled), secondary (not yet departed)
- Line: `var(--color-border-secondary)` always
- Chevron arrow at bottom: `var(--color-text-tertiary)` always

---

## HTML fragment template

Replace `{{tokens}}` with values from the data model. Conditional blocks are marked `{{#if condition}}…{{/if}}`.

```html
<div class="train-card">

  <!-- Header -->
  <div class="card-header">
    <div class="operator-row">
      <div class="operator-dot" style="background:{{operator.brandColour}};"></div>
      <span class="operator-name">{{operator.name}}</span>
    </div>
    <span class="status-badge {{statusBadgeClass}}">
      <i class="ti {{statusIcon}}" aria-hidden="true"></i>{{statusText}}
    </span>
  </div>

  <!-- Route -->
  <div class="route-section">
    <div class="origin-dest">

      <div class="station-col">
        <div class="station-name">{{origin.name}}</div>
        <div class="time-row">
          <span class="time-planned {{#if isDelayedOrCancelled}}strikethrough{{/if}}">
            {{origin.plannedDeparture}}
          </span>
          {{#if origin.actualDeparture}}
          <span class="time-actual {{#if isCancelled}}cancelled{{/if}}">
            {{#if isCancelled}}Cancelled{{else}}{{origin.actualDeparture}}{{/if}}
          </span>
          {{/if}}
        </div>
        <div class="platform-tag">
          Platform
          <span class="platform-num {{#unless origin.platform}}unknown{{/unless}}">
            {{origin.platform ?? "TBC"}}
          </span>
        </div>
      </div>

      <div class="route-spine">
        <div class="spine-dot" style="color: {{spineColour}};"></div>
        <div class="spine-line" style="background: var(--color-border-secondary);"></div>
        <i class="ti ti-chevron-down spine-arrow" aria-hidden="true"></i>
      </div>

      <div class="station-col dest-col">
        <div class="station-name">{{destination.name}}</div>
        <div class="time-row" style="justify-content:flex-end;">
          <span class="time-planned {{#if isDelayedOrCancelled}}strikethrough{{/if}}">
            {{destination.plannedArrival}}
          </span>
          {{#if destination.estimatedArrival}}
          <span class="time-actual">{{destination.estimatedArrival}}</span>
          {{/if}}
        </div>
        <div class="platform-tag" style="justify-content:flex-end;">
          Platform
          <span class="platform-num {{#unless destination.platform}}unknown{{/unless}}">
            {{destination.platform ?? (isCancelled ? "N/A" : "TBC")}}
          </span>
        </div>
      </div>

    </div>
  </div>

  <!-- Disruption banner (conditional) -->
  {{#if disruption.reason}}
  <div class="delay-banner {{#if isCancelled}}danger{{else}}warning{{/if}}">
    <i class="ti {{#if isCancelled}}ti-alert-circle{{else}}ti-alert-triangle{{/if}}"
       aria-hidden="true" style="font-size:15px;"></i>
    <div class="reason">
      <strong>{{disruption.reason}}</strong>
      {{#if disruption.detail}}{{disruption.detail}}{{/if}}
    </div>
  </div>
  {{/if}}

  <!-- Progress (hidden when cancelled) -->
  {{#unless isCancelled}}
  <hr class="divider">
  <div class="progress-section" style="padding-top:14px;">
    <div class="progress-status-text">
      {{trainPosition.description}}
      {{#if trainPosition.stopsRemaining}}
        · <strong>{{trainPosition.stopsRemaining}} stop{{#if stopsRemaining > 1}}s{{/if}} remaining</strong>
      {{/if}}
    </div>
    <div class="progress-bar-wrap">
      <div class="progress-fill"
           style="width:{{trainPosition.progressPercent}}%; background:{{progressBarColour}}; opacity:{{progressBarOpacity}};"></div>
      <span class="progress-train-icon"
            style="left:{{max(trainPosition.progressPercent, 6)}}%;"
            aria-hidden="true">🚆</span>
    </div>
    <div class="progress-labels">
      <span>{{origin.name}}</span>
      <span>{{destination.name}}</span>
    </div>
  </div>
  {{/unless}}

</div>
```

---

## Required CSS (include once per page)

```css
.train-card {
  background: var(--color-background-primary);
  border: 0.5px solid var(--color-border-tertiary);
  border-radius: var(--border-radius-lg);
  overflow: hidden;
  max-width: 360px;
}
.card-header {
  padding: 14px 16px 10px;
  border-bottom: 0.5px solid var(--color-border-tertiary);
  display: flex; align-items: center; justify-content: space-between;
}
.operator-row { display: flex; align-items: center; gap: 8px; }
.operator-dot { width: 10px; height: 10px; border-radius: 50%; flex-shrink: 0; }
.operator-name { font-size: 12px; color: var(--color-text-secondary); font-weight: 500; }
.status-badge {
  font-size: 11px; font-weight: 500; padding: 3px 8px;
  border-radius: 99px; letter-spacing: 0.02em;
}
.badge-on-time   { background: var(--color-background-success); color: var(--color-text-success); }
.badge-delayed   { background: var(--color-background-warning); color: var(--color-text-warning); }
.badge-cancelled { background: var(--color-background-danger);  color: var(--color-text-danger); }
.route-section { padding: 14px 16px 10px; }
.origin-dest { display: flex; align-items: stretch; }
.station-col { flex: 1; }
.dest-col { text-align: right; }
.station-name { font-size: 17px; font-weight: 500; color: var(--color-text-primary); margin-bottom: 4px; }
.time-row { display: flex; align-items: baseline; gap: 6px; flex-wrap: wrap; }
.time-planned { font-size: 22px; font-weight: 500; color: var(--color-text-primary); }
.time-planned.strikethrough {
  text-decoration: line-through; color: var(--color-text-tertiary); font-size: 18px;
}
.time-actual { font-size: 22px; font-weight: 500; color: var(--color-text-warning); }
.time-actual.cancelled { color: var(--color-text-danger); }
.platform-tag {
  font-size: 12px; color: var(--color-text-secondary); margin-top: 4px;
  display: flex; align-items: center; gap: 4px;
}
.platform-num {
  background: var(--color-background-secondary);
  border: 0.5px solid var(--color-border-secondary);
  border-radius: 4px; padding: 1px 6px;
  font-weight: 500; font-size: 12px; color: var(--color-text-primary);
}
.platform-num.unknown { color: var(--color-text-tertiary); border-color: var(--color-border-tertiary); }
.route-spine {
  display: flex; flex-direction: column; align-items: center;
  padding: 4px 14px 0; min-width: 48px; gap: 3px;
}
.spine-dot { width: 8px; height: 8px; border-radius: 50%; border: 2px solid currentColor; flex-shrink: 0; }
.spine-line { width: 2px; flex: 1; min-height: 24px; background: var(--color-border-secondary); }
.spine-arrow { font-size: 10px; color: var(--color-text-tertiary); }
.progress-section { padding: 0 16px 14px; }
.progress-status-text { font-size: 12px; color: var(--color-text-secondary); margin-bottom: 8px; }
.progress-status-text strong { color: var(--color-text-primary); font-weight: 500; }
.progress-bar-wrap {
  position: relative; height: 6px;
  background: var(--color-background-secondary); border-radius: 99px;
  overflow: visible; margin-bottom: 8px;
}
.progress-fill { height: 100%; border-radius: 99px; }
.progress-train-icon {
  position: absolute; top: 50%;
  transform: translate(-50%, -50%);
  font-size: 16px; line-height: 1;
}
.progress-labels { display: flex; justify-content: space-between; font-size: 11px; color: var(--color-text-tertiary); }
.delay-banner {
  margin: 0 16px 14px; padding: 10px 12px;
  border-radius: var(--border-radius-md);
  font-size: 12px; line-height: 1.5;
  display: flex; gap: 8px; align-items: flex-start;
}
.delay-banner.warning { background: var(--color-background-warning); color: var(--color-text-warning); }
.delay-banner.danger  { background: var(--color-background-danger);  color: var(--color-text-danger); }
.delay-banner .reason strong { display: block; font-weight: 500; margin-bottom: 2px; }
.divider { border: none; border-top: 0.5px solid var(--color-border-tertiary); }
```

---

## Scenarios reference

| Scenario | `status`    | Has disruption banner | Has progress bar | Time display           |
|----------|-------------|----------------------|------------------|------------------------|
| A        | `on_time`   | No                   | Yes              | Planned times only     |
| B        | `delayed`   | Yes (warning)        | Yes              | Strikethrough + actual |
| C        | `cancelled` | Yes (danger)         | No               | Strikethrough + "Cancelled" |
| D        | `on_time`   | No                   | Yes (at 4%)      | Planned times, "at platform" note |
