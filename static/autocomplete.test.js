const assert = require('node:assert/strict');
const { test } = require('node:test');
const { wordBoundaryMatch, buildResults } = require('./autocomplete.js');

const STATIONS = [
  { crs: 'SHF', name: 'Sheffield' },
  { crs: 'MAN', name: 'Manchester Piccadilly' },
  { crs: 'LDS', name: 'Leeds' },
  { crs: 'NCL', name: 'Newcastle' },
  { crs: 'EDB', name: 'Edinburgh' },
  { crs: 'GLC', name: 'Glasgow Central' },
  { crs: 'BHM', name: 'Birmingham New Street' },
  { crs: 'LIV', name: 'Liverpool Lime Street' },
  { crs: 'BRI', name: 'Bristol Temple Meads' },
  { crs: 'EXD', name: 'Exeter St Davids' },
  { crs: 'NRW', name: 'Norwich' },
];

test('wordBoundaryMatch: matches from start of name', () => {
  assert.ok(wordBoundaryMatch('Sheffield', 'she'));
  assert.ok(wordBoundaryMatch('Sheffield', 'sheffield'));
});

test('wordBoundaryMatch: matches from word boundary', () => {
  assert.ok(wordBoundaryMatch('Manchester Piccadilly', 'picc'));
  assert.ok(wordBoundaryMatch('Birmingham New Street', 'new'));
  assert.ok(wordBoundaryMatch('Liverpool Lime Street', 'lime'));
});

test('wordBoundaryMatch: lowercases the station name', () => {
  // q is expected to be pre-lowercased (buildResults does this); the function lowercases the name
  assert.ok(wordBoundaryMatch('SHEFFIELD', 'she'));
  assert.ok(wordBoundaryMatch('Glasgow Central', 'gla'));
});

test('wordBoundaryMatch: no match', () => {
  assert.ok(!wordBoundaryMatch('Sheffield', 'zzz'));
  assert.ok(!wordBoundaryMatch('Sheffield', 'ield')); // mid-word, not boundary
});

test('buildResults: empty query returns empty', () => {
  assert.deepEqual(buildResults(STATIONS, ''), []);
  assert.deepEqual(buildResults(STATIONS, '   '), []);
});

test('buildResults: CRS hits ranked before name hits', () => {
  // 'man' matches CRS MAN and name 'Manchester'
  const results = buildResults(STATIONS, 'man');
  assert.equal(results[0].crs, 'MAN'); // CRS hit first
  assert.ok(results.some(s => s.name === 'Manchester Piccadilly'));
});

test('buildResults: name match', () => {
  const results = buildResults(STATIONS, 'she');
  assert.ok(results.some(s => s.crs === 'SHF'));
});

test('buildResults: word boundary match within name', () => {
  const results = buildResults(STATIONS, 'picc');
  assert.ok(results.some(s => s.crs === 'MAN'));
});

test('buildResults: capped at 10 results', () => {
  // All 11 stations have names; query 'e' likely matches many
  const results = buildResults(STATIONS, 'e');
  assert.ok(results.length <= 10);
});

test('buildResults: no match returns empty', () => {
  const results = buildResults(STATIONS, 'zzzzz');
  assert.deepEqual(results, []);
});
