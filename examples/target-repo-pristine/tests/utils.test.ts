import { strict as assert } from "node:assert";
import { test } from "node:test";
import { formatDate, formatNumber, parseRange } from "../src/utils.ts";

test("formatDate returns YYYY-MM-DD in UTC", () => {
  const d = new Date(Date.UTC(2026, 3, 28));
  assert.equal(formatDate(d), "2026-04-28");
});

test("parseRange inclusive on both ends", () => {
  assert.deepEqual(parseRange("1-5"), [1, 2, 3, 4, 5]);
});

test("parseRange single value", () => {
  assert.deepEqual(parseRange("3-3"), [3]);
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
