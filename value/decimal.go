package value

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

const (
	decimalBias        = 6176
	decimalMaxExponent = 6111
)

var (
	decimalMaxCoefficient = new(big.Int).Sub(new(big.Int).Exp(big.NewInt(10), big.NewInt(34), nil), big.NewInt(1))
	decimalMask113        = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 113), big.NewInt(1))
	decimalText           = regexp.MustCompile(`^(\d*)(?:\.(\d*))?(?:[eE]([+-]?\d+))?$`)
)

// Decimal128 is an IEEE 754-2008 decimal128 value as its sixteen BSON (BID)
// bytes, least significant first. There is no arithmetic.
type Decimal128 [16]byte

// Bytes returns a copy of the sixteen bytes.
func (d Decimal128) Bytes() []byte {
	out := make([]byte, 16)
	copy(out, d[:])
	return out
}

// ParseDecimal128 parses decimal text exactly; text that would need rounding
// is rejected.
func ParseDecimal128(text string) (Decimal128, error) {
	bad := func() (Decimal128, error) {
		return Decimal128{}, &Error{Message: fmt.Sprintf("invalid or inexact Decimal128 text %q", text)}
	}
	s := strings.TrimSpace(text)
	negative := strings.HasPrefix(s, "-")
	body := strings.TrimLeft(s, "+-")
	if len(s)-len(body) > 1 {
		return bad()
	}
	bits := new(big.Int)
	if negative {
		bits.SetBit(bits, 127, 1)
	}
	switch strings.ToLower(body) {
	case "inf", "infinity":
		bits.Or(bits, new(big.Int).Lsh(big.NewInt(0b11110), 122))
	case "nan":
		bits = new(big.Int).Lsh(big.NewInt(0b11111), 122)
	default:
		m := decimalText.FindStringSubmatch(body)
		if m == nil || (m[1] == "" && m[2] == "") {
			return bad()
		}
		digitsText := strings.TrimLeft(m[1]+m[2], "0")
		coefficient, _ := new(big.Int).SetString("0"+digitsText, 10)
		exponent := -len(m[2])
		if m[3] != "" {
			e, err := strconv.Atoi(m[3])
			if err != nil {
				return bad()
			}
			exponent += e
		}
		digits := len(digitsText)
		ten := big.NewInt(10)
		for exponent > decimalMaxExponent && digits < 34 {
			coefficient.Mul(coefficient, ten)
			exponent--
			digits++
		}
		// Trailing zeros that do not fit are dropped when that stays exact.
		remainder := new(big.Int)
		for coefficient.Sign() != 0 && (coefficient.Cmp(decimalMaxCoefficient) > 0 || exponent < -decimalBias) {
			quotient, r := new(big.Int).QuoRem(coefficient, ten, remainder)
			if r.Sign() != 0 {
				break
			}
			coefficient = quotient
			exponent++
		}
		if coefficient.Sign() == 0 {
			exponent = max(-decimalBias, min(decimalMaxExponent, exponent))
		}
		if coefficient.Cmp(decimalMaxCoefficient) > 0 || exponent > decimalMaxExponent || exponent < -decimalBias {
			return bad()
		}
		bits.Or(bits, new(big.Int).Lsh(big.NewInt(int64(exponent+decimalBias)), 113))
		bits.Or(bits, coefficient)
	}
	var out Decimal128
	buf := make([]byte, 16)
	bits.FillBytes(buf)
	for i := range out {
		out[i] = buf[15-i]
	}
	return out, nil
}

// String returns the canonical decimal text, such as "12.50", "1.23E+20",
// "-Infinity" or "NaN".
func (d Decimal128) String() string {
	buf := make([]byte, 16)
	for i := range buf {
		buf[15-i] = d[i]
	}
	bits := new(big.Int).SetBytes(buf)
	sign := ""
	if bits.Bit(127) == 1 {
		sign = "-"
	}
	top5 := new(big.Int).Rsh(bits, 122).Int64() & 0x1f
	switch top5 {
	case 0b11111:
		return "NaN"
	case 0b11110:
		return sign + "Infinity"
	}
	var exponent int
	coefficient := new(big.Int)
	if new(big.Int).Rsh(bits, 125).Int64()&3 == 3 {
		exponent = int(new(big.Int).Rsh(bits, 111).Int64()&0x3fff) - decimalBias
	} else {
		exponent = int(new(big.Int).Rsh(bits, 113).Int64()&0x3fff) - decimalBias
		coefficient.And(bits, decimalMask113)
		if coefficient.Cmp(decimalMaxCoefficient) > 0 {
			coefficient.SetInt64(0)
		}
	}
	digits := coefficient.String()
	adjusted := exponent + len(digits) - 1
	if exponent > 0 || adjusted < -6 {
		rest := ""
		if len(digits) > 1 {
			rest = "." + digits[1:]
		}
		plus := ""
		if adjusted >= 0 {
			plus = "+"
		}
		return fmt.Sprintf("%s%s%sE%s%d", sign, digits[:1], rest, plus, adjusted)
	}
	if exponent == 0 {
		return sign + digits
	}
	point := len(digits) + exponent
	if point > 0 {
		return sign + digits[:point] + "." + digits[point:]
	}
	return sign + "0." + strings.Repeat("0", -point) + digits
}
