const assert = require('node:assert/strict');
const { test } = require('node:test');
const { nextDelayMs, parseClock } = require('./poller.js');

const MIN = 60 * 1000;
const BASE = 5 * MIN;

test('nextDelayMs: no events gives base interval', () => {
  assert.equal(nextDelayMs(0, []), BASE);
});

test('nextDelayMs: far event gives base interval', () => {
  assert.equal(nextDelayMs(0, [30 * MIN]), BASE);
});

test('nextDelayMs: event within base polls midway', () => {
  assert.equal(nextDelayMs(0, [4 * MIN]), 2 * MIN);
});

test('nextDelayMs: midway is floored at one minute', () => {
  assert.equal(nextDelayMs(0, [90 * 1000]), MIN);
});

test('nextDelayMs: uses earliest future event', () => {
  assert.equal(nextDelayMs(0, [20 * MIN, 4 * MIN, -10 * MIN]), 2 * MIN);
});

test('nextDelayMs: just-passed event polls after the minimum', () => {
  assert.equal(nextDelayMs(10 * 1000, [0]), MIN);
});

test('nextDelayMs: long-passed event is ignored', () => {
  assert.equal(nextDelayMs(10 * MIN, [0]), BASE);
});

test('parseClock: accepts HH:MM and HHMM', () => {
  const now = new Date(2026, 9, 7, 9, 0).getTime();
  const want = new Date(2026, 9, 7, 9, 30).getTime();
  assert.equal(parseClock('09:30', now), want);
  assert.equal(parseClock('0930', now), want);
});

test('parseClock: empty or invalid gives null', () => {
  assert.equal(parseClock('', 0), null);
  assert.equal(parseClock('soon', 0), null);
});

test('parseClock: early-morning time after late evening rolls to next day', () => {
  const now = new Date(2026, 9, 7, 23, 50).getTime();
  const want = new Date(2026, 9, 8, 0, 10).getTime();
  assert.equal(parseClock('00:10', now), want);
});
