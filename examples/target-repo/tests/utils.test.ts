import { strict as assert } from "node:assert";
import { test } from "node:test";
import { formatDate, formatNumber, parseRange } from "../src/utils";

test("formatDate returns YYYY-MM-DD in UTC when timezone is omitted", () => {
  const d = new Date(Date.UTC(2026, 3, 28, 23, 30));
  assert.equal(formatDate(d), "2026-04-28");
});

test("formatDate returns YYYY-MM-DD in UTC when timezone is provided", () => {
  const d = new Date(Date.UTC(2026, 3, 28, 23, 30));
  assert.equal(formatDate(d, "UTC"), "2026-04-28");
});

test("formatDate formats in the provided IANA timezone", () => {
  const d = new Date(Date.UTC(2026, 3, 28, 23, 30));
  assert.equal(formatDate(d, "Asia/Tokyo"), "2026-04-29");
});

test("parseRange inclusive on both ends", () => {
  assert.deepEqual(parseRange("1-5"), [1, 2, 3, 4, 5]);
});

test("parseRange single value", () => {
  assert.deepEqual(parseRange("3-3"), [3]);
});

test("parseRange negative start", () => {
  assert.deepEqual(parseRange("-2-2"), [-2, -1, 0, 1, 2]);
});

test("parseRange negative both", () => {
  assert.deepEqual(parseRange("-5--3"), [-5, -4, -3]);
});

test("formatNumber positive", () => {
  assert.equal(formatNumber(1234567), "1,234,567");
});

test("formatNumber zero", () => {
  assert.equal(formatNumber(0), "0");
});

test("formatNumber negative integer", () => {
  assert.equal(formatNumber(-1234), "-1,234");
});

test("formatNumber negative integer in scientific notation", () => {
  assert.equal(formatNumber(-1e21), "-1,000,000,000,000,000,000,000");
});

test("formatNumber negative fractional in scientific notation", () => {
  assert.equal(formatNumber(-1e-7), "-0.0000001");
});
