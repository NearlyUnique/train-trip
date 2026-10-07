var POLL_BASE_MS = 5 * 60 * 1000;
var POLL_MIN_MS = 60 * 1000;
var POLL_TIMEOUT_MS = 20 * 1000;

// parseClock converts "HH:MM" or "HHMM" to epoch ms near nowMs, or null if invalid.
function parseClock(str, nowMs) {
  var m = /^(\d{2}):?(\d{2})$/.exec(str || '');
  if (!m) { return null; }
  var d = new Date(nowMs);
  d.setHours(+m[1], +m[2], 0, 0);
  var t = d.getTime();
  var halfDay = 12 * 60 * 60 * 1000;
  if (t < nowMs - halfDay) { t += 2 * halfDay; }
  return t;
}

// nextDelayMs returns the wait before the next poll: the base interval, or
// halfway to the earliest upcoming event when it is nearer than the base.
// An event that has just passed gives the minimum so the display catches up.
function nextDelayMs(nowMs, eventTimesMs) {
  var delay = POLL_BASE_MS;
  eventTimesMs.forEach(function (t) {
    var ahead = t - nowMs;
    if (ahead >= 0 && ahead < POLL_BASE_MS) {
      delay = Math.min(delay, Math.max(POLL_MIN_MS, ahead / 2));
    } else if (ahead < 0 && -ahead < POLL_MIN_MS) {
      delay = Math.min(delay, POLL_MIN_MS);
    }
  });
  return delay;
}

function eventTimesFromDom(nowMs) {
  var times = [];
  document.querySelectorAll('.leg-card[data-next-event]').forEach(function (el) {
    var t = parseClock(el.dataset.nextEvent, nowMs);
    if (t !== null) { times.push(t); }
  });
  return times;
}

function initPoller() {
  var timer = null;
  var lastPoll = Date.now();
  var inFlight = 0;
  var indicator = document.getElementById('poll-indicator');

  function setIndicator() {
    if (indicator) { indicator.classList.toggle('polling', inFlight > 0); }
  }
  function isLeg(e) {
    return e.detail && e.detail.elt && e.detail.elt.classList.contains('leg-card');
  }
  function stop() {
    clearTimeout(timer);
    timer = null;
  }
  function schedule() {
    stop();
    var now = Date.now();
    timer = setTimeout(poll, nextDelayMs(now, eventTimesFromDom(now)));
  }
  function poll() {
    lastPoll = Date.now();
    document.querySelectorAll('.leg-card').forEach(function (el) {
      htmx.trigger(el, 'refresh');
    });
    schedule();
  }

  htmx.config.timeout = POLL_TIMEOUT_MS;
  document.addEventListener('htmx:beforeRequest', function (e) {
    if (isLeg(e)) { inFlight++; setIndicator(); }
  });
  document.addEventListener('htmx:afterRequest', function (e) {
    if (isLeg(e)) { inFlight = Math.max(0, inFlight - 1); setIndicator(); }
  });
  // Swapped-in cards carry fresh event times, so re-plan the next poll.
  document.addEventListener('htmx:afterSwap', function (e) {
    if (isLeg(e) && timer !== null) { schedule(); }
  });
  document.addEventListener('visibilitychange', function () {
    if (document.hidden) {
      stop();
    } else if (Date.now() - lastPoll > POLL_MIN_MS) {
      poll();
    } else {
      schedule();
    }
  });
  window.addEventListener('pageshow', function (e) {
    if (e.persisted) { poll(); }
  });

  if (!document.hidden) { schedule(); }
}

if (typeof module !== 'undefined') {
  module.exports = { nextDelayMs, parseClock };
}
