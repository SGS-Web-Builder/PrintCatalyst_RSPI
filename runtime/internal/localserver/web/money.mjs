// Exact decimal <-> minor-unit conversion for merchant price entry.
//
// Money is never held in a binary floating-point value here: the merchant's
// typed decimal string is parsed with BigInt and sent to the local service as a
// whole number of minor units. That keeps 0.29, 1.005 and similar values exact
// and matches the integer storage in SQLite.

// Mirrors the CHECK ceiling on pricing_rules.unit_price_minor.
export const MAX_MINOR_UNITS = 100000000n;
const AMOUNT_PATTERN = /^\d+(\.\d+)?$/;

function assertMinorUnits(minorUnits) {
  if (!Number.isInteger(minorUnits) || minorUnits < 0 || minorUnits > 3) {
    throw new Error('Decimal places must be a whole number from 0 to 3.');
  }
}

// toMinorUnits('2.50', 2) === 250; toMinorUnits('250', 0) === 250
export function toMinorUnits(amount, minorUnits) {
  assertMinorUnits(minorUnits);
  const text = String(amount ?? '').trim();
  if (!AMOUNT_PATTERN.test(text)) {
    throw new Error('Enter a plain decimal amount such as 2.50, with no currency symbol.');
  }
  const [whole, fraction = ''] = text.split('.');
  if (fraction.length > minorUnits) {
    throw new Error(minorUnits === 0
      ? 'This currency has no decimal places. Enter whole minor units.'
      : `This currency uses at most ${minorUnits} decimal place${minorUnits === 1 ? '' : 's'}.`);
  }
  const minor = BigInt(whole + fraction.padEnd(minorUnits, '0'));
  if (minor > MAX_MINOR_UNITS) {
    throw new Error('That amount is too large for a local price entry.');
  }
  // The ceiling above is far below 2**53, so this conversion stays exact.
  return Number(minor);
}

// fromMinorUnits(250, 2) === '2.50'; fromMinorUnits(250, 0) === '250'
export function fromMinorUnits(minor, minorUnits) {
  assertMinorUnits(minorUnits);
  if (!Number.isInteger(minor)) throw new Error('Stored prices must be whole minor units.');
  const value = BigInt(minor);
  if (value < 0n) throw new Error('Stored prices cannot be negative.');
  if (value > MAX_MINOR_UNITS) throw new Error('Stored price is out of range.');
  const digits = value.toString().padStart(minorUnits + 1, '0');
  const whole = digits.slice(0, digits.length - minorUnits);
  const fraction = digits.slice(digits.length - minorUnits);
  return fraction ? `${whole}.${fraction}` : whole;
}

// Order totals may exceed the individual price-entry limit.
export function formatMoney(minor, minorUnits) {
  assertMinorUnits(minorUnits);
  if (!Number.isSafeInteger(minor)) throw new Error('Stored totals must be safe whole minor units.');
  const value = BigInt(minor);
  const sign = value < 0n ? '-' : '';
  const digits = (value < 0n ? -value : value).toString().padStart(minorUnits + 1, '0');
  if (minorUnits === 0) return sign + digits;
  return sign + digits.slice(0, -minorUnits) + '.' + digits.slice(-minorUnits);
}
