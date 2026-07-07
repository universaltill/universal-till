// Package money defines a distinct type for monetary amounts.
//
// Money is an amount in integer minor units (e.g. cents). It is a named int64,
// so:
//   - the compiler prevents accidentally mixing money with unrelated integers
//     (quantities, basis-point rates, counts) — enforcing the "money is always
//     integer minor units" rule at compile time;
//   - it marshals to/from JSON as the same integer as before (wire-compatible);
//   - it round-trips through database/sql as an integer.
//
// Convert to/from raw int64 only at boundaries (DB rows, wire DTOs of other
// packages) via FromMinor / Minor.
package money

import (
	"database/sql/driver"
	"fmt"
	"math"
	"strconv"
)

// Money is a monetary amount in integer minor units.
type Money int64

// Zero is the additive identity.
const Zero Money = 0

// FromMinor wraps a raw minor-unit value (use at DB / external DTO boundaries).
func FromMinor(v int64) Money { return Money(v) }

// Minor returns the raw minor-unit value (use at DB / external DTO boundaries).
func (m Money) Minor() int64 { return int64(m) }

// Add returns m + n.
func (m Money) Add(n Money) Money { return m + n }

// Sub returns m - n.
func (m Money) Sub(n Money) Money { return m - n }

// Neg returns -m.
func (m Money) Neg() Money { return -m }

// Scale multiplies by an integer count (e.g. a whole quantity).
func (m Money) Scale(n int64) Money { return Money(int64(m) * n) }

// MulQty multiplies a unit price by a (possibly fractional) quantity, rounding
// half away from zero — matches the previous AmountForQuantity behavior.
func (m Money) MulQty(qty float64) Money {
	return Money(math.Round(float64(m) * qty))
}

// MulDiv returns m * num / den with half-up rounding (num/den is a ratio, not
// money). Returns 0 when den == 0.
func (m Money) MulDiv(num, den int64) Money {
	if den == 0 {
		return 0
	}
	return Money((int64(m)*num + den/2) / den)
}

// IsZero reports whether the amount is exactly zero.
func (m Money) IsZero() bool { return m == 0 }

// IsPositive reports whether the amount is greater than zero.
func (m Money) IsPositive() bool { return m > 0 }

// IsNegative reports whether the amount is less than zero.
func (m Money) IsNegative() bool { return m < 0 }

// Format renders the amount as a fixed two-decimal string (1234 -> "12.34").
func (m Money) Format() string {
	v := int64(m)
	neg := v < 0
	if neg {
		v = -v
	}
	s := strconv.FormatInt(v/100, 10) + "." + fmt.Sprintf("%02d", v%100)
	if neg {
		return "-" + s
	}
	return s
}

// String implements fmt.Stringer (two-decimal form).
func (m Money) String() string { return m.Format() }

// Value implements driver.Valuer — persisted as an integer.
func (m Money) Value() (driver.Value, error) { return int64(m), nil }

// Scan implements sql.Scanner — reads an integer column.
func (m *Money) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*m = 0
	case int64:
		*m = Money(v)
	case int:
		*m = Money(v)
	case []byte:
		n, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			return fmt.Errorf("money: scan %q: %w", v, err)
		}
		*m = Money(n)
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("money: scan %q: %w", v, err)
		}
		*m = Money(n)
	default:
		return fmt.Errorf("money: cannot scan %T into Money", src)
	}
	return nil
}

// Sum adds any number of amounts.
func Sum(amounts ...Money) Money {
	var total Money
	for _, a := range amounts {
		total += a
	}
	return total
}
