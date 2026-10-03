// Package currency holds the ISO 4217 minor-unit exponent for each currency the
// on-premise product supports.
//
// The exponent is not display metadata. It decides what a stored integer amount
// means: under exponent 2 the integer 250 is 2.50, under exponent 0 it is 250.
// It is therefore derived from this table on the server, on every read and every
// write, and never accepted from a merchant, a client or a request body.
//
// The table is an allowlist. A currency that is absent is rejected rather than
// guessed, because a wrong exponent silently multiplies or divides every price a
// merchant has stored. Extend it deliberately, and keep every exponent within
// the 0..3 range that migration 003 enforces on pricing_book.
package currency

import (
	"sort"
	"strings"
)

// Unsupported is returned for a currency this build has no exponent for. It is
// negative so it can never be mistaken for a usable precision.
const Unsupported = -1

// exponents maps an ISO 4217 alphabetic code to its minor-unit exponent.
// Accounting units that ISO defines with four decimal places (CLF, UYW) and
// non-circulating codes (XDR, the precious metals, XSU, XXX) are deliberately
// absent: they are not a price a print shop charges, and supporting them would
// need a wider CHECK constraint on pricing_book.currency_minor_units.
var exponents = map[string]int{
	// Zero decimal places.
	"BIF": 0, "CLP": 0, "DJF": 0, "GNF": 0, "ISK": 0, "JPY": 0,
	"KMF": 0, "KRW": 0, "PYG": 0, "RWF": 0, "UGX": 0, "UYI": 0,
	"VND": 0, "VUV": 0, "XAF": 0, "XOF": 0, "XPF": 0,

	// Three decimal places.
	"BHD": 3, "IQD": 3, "JOD": 3, "KWD": 3, "LYD": 3, "OMR": 3, "TND": 3,
}

// twoDecimal is kept separate so the common case reads as one auditable block.
// MGA and MRU are listed by ISO 4217 with exponent 2 even though their
// subdivision is base five; the published exponent is what is used here.
var twoDecimal = []string{
	"AED", "AFN", "ALL", "AMD", "ANG", "AOA", "ARS", "AUD", "AWG", "AZN",
	"BAM", "BBD", "BDT", "BGN", "BMD", "BND", "BOB", "BRL", "BSD", "BTN",
	"BWP", "BYN", "BZD", "CAD", "CDF", "CHF", "CNY", "COP", "CRC", "CUP",
	"CVE", "CZK", "DKK", "DOP", "DZD", "EGP", "ERN", "ETB", "EUR", "FJD",
	"FKP", "GBP", "GEL", "GHS", "GIP", "GMD", "GTQ", "GYD", "HKD", "HNL",
	"HTG", "HUF", "IDR", "ILS", "INR", "IRR", "JMD", "KES", "KGS", "KHR",
	"KPW", "KYD", "KZT", "LAK", "LBP", "LKR", "LRD", "LSL", "MAD", "MDL",
	"MGA", "MKD", "MMK", "MNT", "MOP", "MRU", "MUR", "MVR", "MWK", "MXN",
	"MYR", "MZN", "NAD", "NGN", "NIO", "NOK", "NPR", "NZD", "PAB", "PEN",
	"PGK", "PHP", "PKR", "PLN", "QAR", "RON", "RSD", "RUB", "SAR", "SBD",
	"SCR", "SDG", "SEK", "SGD", "SHP", "SLE", "SOS", "SRD", "SSP", "STN",
	"SVC", "SYP", "SZL", "THB", "TJS", "TMT", "TOP", "TRY", "TTD", "TWD",
	"TZS", "UAH", "USD", "UYU", "UZS", "VES", "WST", "XCD", "YER", "ZAR",
	"ZMW", "ZWG",
}

// MaxExponent is the widest precision in the table, and the widest the pricing
// schema can store.
const MaxExponent = 3

func init() {
	for _, code := range twoDecimal {
		if _, exists := exponents[code]; exists {
			panic("currency: " + code + " has a duplicate exponent")
		}
		exponents[code] = 2
	}
}

// Normalize upper-cases and trims a currency code the way the business profile
// stores it.
func Normalize(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

// MinorUnits returns the ISO 4217 exponent for code, or Unsupported if the code
// is not in the allowlist.
func MinorUnits(code string) int {
	exponent, ok := exponents[Normalize(code)]
	if !ok {
		return Unsupported
	}
	return exponent
}

// Supported reports whether code has a known exponent in this build.
func Supported(code string) bool { return MinorUnits(code) != Unsupported }

// Codes returns every supported code in ascending order, for diagnostics and
// tests. It is not exposed on any customer-facing endpoint.
func Codes() []string {
	codes := make([]string, 0, len(exponents))
	for code := range exponents {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}
