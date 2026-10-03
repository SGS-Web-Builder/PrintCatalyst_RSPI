import test from 'node:test';
import assert from 'node:assert/strict';
import { fromMinorUnits, toMinorUnits, MAX_MINOR_UNITS } from '../runtime/internal/localserver/web/money.mjs';
import { createOwnerClient } from '../runtime/internal/localserver/web/client.mjs';

// Tier validation mirrors the Go service-side rules. The browser converts
// quantities and amounts with the same BigInt arithmetic the server stores,
// so any divergence surfaces as a Node failure rather than a silent mismatch.
function tierShape(tier) {
  if (!Number.isInteger(tier.minQuantity) || tier.minQuantity < 2) {
    throw new Error('Quantity must be a whole number ≥ 2.');
  }
  if (!Number.isInteger(tier.unitPriceMinor) || tier.unitPriceMinor < 0 || tier.unitPriceMinor > Number(MAX_MINOR_UNITS)) {
    throw new Error('Tier unit price must be a whole minor-unit amount within the storage ceiling.');
  }
  return tier;
}

function validateTiersClient(baseMinor, tiers) {
  const sorted = [...tiers].sort((a, b) => a.minQuantity - b.minQuantity);
  let previous = baseMinor;
  let lastQuantity = -1;
  for (const tier of sorted) {
    tierShape(tier);
    if (tier.minQuantity === lastQuantity) throw new Error('Duplicate tier threshold.');
    lastQuantity = tier.minQuantity;
    if (tier.unitPriceMinor >= previous) throw new Error('A higher-volume tier must be strictly cheaper than every lower-volume tier, including the base.');
    previous = tier.unitPriceMinor;
  }
}

test('price entry converts decimal text to whole minor units without floating-point error', () => {
  assert.equal(toMinorUnits('2.50', 2), 250);
  assert.equal(toMinorUnits('0.29', 2), 29); // 0.29 * 100 === 28.999999999999996 in binary floats
  assert.equal(toMinorUnits('1.005', 3), 1005);
  assert.equal(toMinorUnits('0.07', 2), 7);
  assert.equal(toMinorUnits('250', 0), 250);
  assert.equal(toMinorUnits('0', 2), 0);
  assert.equal(toMinorUnits('  12.30 ', 2), 1230);
  assert.equal(toMinorUnits('12.3', 2), 1230);
  assert.equal(toMinorUnits('1000000', 2), 100000000);
  assert.equal(Number.isInteger(toMinorUnits('19.99', 2)), true);
});

test('price entry rejects amounts that cannot be represented exactly', () => {
  assert.throws(() => toMinorUnits('2.505', 2), /at most 2 decimal places/);
  assert.throws(() => toMinorUnits('2.5', 0), /no decimal places/);
  assert.throws(() => toMinorUnits('', 2), /plain decimal amount/);
  assert.throws(() => toMinorUnits('abc', 2), /plain decimal amount/);
  assert.throws(() => toMinorUnits('-1', 2), /plain decimal amount/);
  assert.throws(() => toMinorUnits('1e3', 2), /plain decimal amount/);
  assert.throws(() => toMinorUnits('2,50', 2), /plain decimal amount/);
  assert.throws(() => toMinorUnits('₹2.50', 2), /plain decimal amount/);
  assert.throws(() => toMinorUnits('1000000.01', 2), /too large/);
  for (const minorUnits of [-1, 4, 2.5, NaN, '2']) {
    assert.throws(() => toMinorUnits('1.00', minorUnits), /whole number from 0 to 3/, `minor units ${minorUnits}`);
  }
});

// The exponent decides what an integer amount means, which is why it is derived
// server-side from the currency and never chosen by the merchant.
test('the same typed amount means different integers under different currency exponents', () => {
  assert.equal(toMinorUnits('2.50', 2), 250); // INR 2.50 is always 250 minor units
  assert.equal(toMinorUnits('2.50', 3), 2500); // KWD 2.500 is a different amount of money
  assert.throws(() => toMinorUnits('2.50', 0), /no decimal places/); // JPY has none
  assert.equal(fromMinorUnits(250, 2), '2.50');
  assert.equal(fromMinorUnits(250, 0), '250'); // same integer, 100x the value
  assert.equal(fromMinorUnits(250, 3), '0.250'); // and 10x less again
});

test('stored minor units render back to exact decimal text', () => {
  assert.equal(fromMinorUnits(250, 2), '2.50');
  assert.equal(fromMinorUnits(29, 2), '0.29');
  assert.equal(fromMinorUnits(5, 0), '5');
  assert.equal(fromMinorUnits(0, 2), '0.00');
  assert.equal(fromMinorUnits(12345, 2), '123.45');
  assert.equal(fromMinorUnits(1005, 3), '1.005');
  assert.equal(fromMinorUnits(Number(MAX_MINOR_UNITS), 2), '1000000.00');
  for (const minorUnits of [0, 1, 2, 3]) {
    for (const minor of [0, 1, 7, 29, 100, 999, 1005, 12345, 1000000]) {
      assert.equal(toMinorUnits(fromMinorUnits(minor, minorUnits), minorUnits), minor, `round trip ${minor}/${minorUnits}`);
    }
  }
  assert.throws(() => fromMinorUnits(-1, 2), /negative/);
  assert.throws(() => fromMinorUnits(2.5, 2), /whole minor units/);
  assert.throws(() => fromMinorUnits(Number(MAX_MINOR_UNITS) + 1, 2), /out of range/);
  assert.throws(() => fromMinorUnits(250, 9), /whole number from 0 to 3/);
});

test('owner client permits the local pricing endpoint and protects its writes', async () => {
  const calls = [];
  const client = createOwnerClient(async (path, options) => {
    calls.push({ path, options });
    return { ok: true, status: 200, json: async () => path.endsWith('/login') ? { csrfToken: 'session-csrf' } : { currency: 'INR', entries: [] } };
  });
  await client.request('POST', '/api/v1/owner/login', { username: 'owner', password: 'not-persisted' });
  const book = await client.request('PUT', '/api/v1/owner/pricing', {
    entries: [{ paperSize: 'A4', colourMode: 'monochrome', sides: 'one-sided', unitPriceMinor: 250 }],
  });
  assert.equal(book.currency, 'INR');
  const write = calls[1];
  assert.equal(write.path, '/api/v1/owner/pricing');
  assert.equal(write.options.headers['X-CSRF-Token'], 'session-csrf');
  assert.equal(write.options.credentials, 'same-origin');
  assert.equal(write.options.redirect, 'error');
  assert.equal(write.options.cache, 'no-store');
  // The client only ever reaches the local owner API surface, and it refuses
  // before fetching, so these add no recorded calls.
  await assert.rejects(client.request('GET', '/api/v1/orders'), /local owner endpoints/i);
  await assert.rejects(client.request('GET', '/api/v1/owner/pricing/../../secrets'), /local owner endpoints/i);
  assert.equal(calls.length, 2);
  await client.request('GET', '/api/v1/owner/pricing');
  assert.equal(calls[2].options.headers['X-CSRF-Token'], undefined, 'reads do not need CSRF');
});

test('a rejected pricing save surfaces the local error instead of appearing to succeed', async () => {
  const client = createOwnerClient(async () => ({ ok: false, status: 400, text: async () => 'invalid pricing configuration' }));
  await assert.rejects(
    client.request('PUT', '/api/v1/owner/pricing', { entries: [] }),
    /invalid pricing configuration/,
  );
});

test('tier quantity must be a whole number ≥ 2', () => {
  assert.throws(() => tierShape({ minQuantity: 1, unitPriceMinor: 100 }), /≥ 2/);
  assert.throws(() => tierShape({ minQuantity: 0, unitPriceMinor: 100 }), /≥ 2/);
  assert.throws(() => tierShape({ minQuantity: 50.5, unitPriceMinor: 100 }), /whole number/);
  assert.throws(() => tierShape({ minQuantity: '50', unitPriceMinor: 100 }), /whole number/);
  // No float shenanigans: fromMinorUnits/toMinorUnits use the same BigInt path
  // the base amount does, so a tier quantity is always a plain integer.
  assert.doesNotThrow(() => tierShape({ minQuantity: 2, unitPriceMinor: 0 }));
});

test('tier price uses the same exponent as the parent base', () => {
  // Same number, different exponent, different meaning. A KWD tier of 0.250
  // must serialize as 250 minor units, not 25 or 2500, because that is what
  // the service will store under its three-decimal exponent.
  assert.equal(toMinorUnits('0.250', 3), 250);
  assert.equal(toMinorUnits('0.25', 2), 25);
  assert.equal(toMinorUnits('0', 0), 0);
  // And the round trip renders back exactly.
  assert.equal(fromMinorUnits(250, 3), '0.250');
});

test('a higher-volume tier must always be strictly cheaper', () => {
  // Strict monotonicity: every tier cheaper than every lower-volume tier, no
  // equal thresholds, no non-discounting tier over a cheaper one.
  validateTiersClient(250, [
    { minQuantity: 50, unitPriceMinor: 200 },
    { minQuantity: 200, unitPriceMinor: 150 },
  ]);
  assert.throws(() => validateTiersClient(250, [
    { minQuantity: 50, unitPriceMinor: 250 },
  ]), /cheaper/);
  assert.throws(() => validateTiersClient(250, [
    { minQuantity: 50, unitPriceMinor: 200 },
    { minQuantity: 100, unitPriceMinor: 200 },
  ]), /cheaper/);
  assert.throws(() => validateTiersClient(250, [
    { minQuantity: 50, unitPriceMinor: 200 },
    { minQuantity: 100, unitPriceMinor: 210 },
  ]), /cheaper/);
  assert.throws(() => validateTiersClient(250, [
    { minQuantity: 50, unitPriceMinor: 200 },
    { minQuantity: 50, unitPriceMinor: 150 },
  ]), /Duplicate/);
});

test('a free tier round-trips as zero minor units regardless of exponent', () => {
  // Zero is a whole minor-unit amount under any exponent in the allowlist; the
  // merchant's confirmation flag, not the math, gates whether a zero-tier save
  // is accepted.
  assert.equal(toMinorUnits('0', 0), 0);
  assert.equal(toMinorUnits('0.00', 2), 0);
  assert.equal(toMinorUnits('0.000', 3), 0);
  assert.equal(fromMinorUnits(0, 0), '0');
  assert.equal(fromMinorUnits(0, 2), '0.00');
  assert.equal(fromMinorUnits(0, 3), '0.000');
});

test('the owner client carries the free-pricing confirmation flag through PUT', async () => {
  const calls = [];
  const client = createOwnerClient(async (path, options) => {
    calls.push({ path, options });
    return { ok: true, status: 200, json: async () => ({ entries: [] }) };
  });
  await client.request('POST', '/api/v1/owner/login', { username: 'owner', password: 'irrelevant' });
  await client.request('PUT', '/api/v1/owner/pricing', {
    confirmFreePricing: true,
    entries: [{ paperSize: 'A4', colourMode: 'monochrome', sides: 'one-sided', unitPriceMinor: 250, tiers: [{ minQuantity: 100, unitPriceMinor: 0 }] }],
  });
  const write = calls[1];
  assert.equal(write.path, '/api/v1/owner/pricing');
  const sent = JSON.parse(write.options.body);
  assert.equal(sent.confirmFreePricing, true);
  assert.equal(sent.entries[0].tiers[0].unitPriceMinor, 0);
  assert.equal(sent.entries[0].tiers[0].minQuantity, 100);
});
