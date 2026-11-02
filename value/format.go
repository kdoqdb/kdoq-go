package value

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// maxSafeInteger is the largest integer a JavaScript number holds exactly;
// larger integers are printed as NumberLong, like the TypeScript client.
const maxSafeInteger = 1<<53 - 1

// Format returns readable, JSONX-like text for v in which typed values keep
// their type, such as NumberDecimal("1.5") or ISODate("2026-11-02T10:30:00.000Z").
// Each nesting level is indented by indent spaces; with indent 0 the text is
// on one line. Fields of a Doc and of a map are sorted, those of a D and of a
// struct keep their order. The output matches the TypeScript client.
func Format(v any, indent int) string {
	var out strings.Builder
	formatValue(&out, v, indent, 0)
	return out.String()
}

func formatValue(out *strings.Builder, v any, indent, depth int) {
	pad := func(level int) string {
		if indent <= 0 {
			return ""
		}
		return "\n" + strings.Repeat(" ", indent*level)
	}
	switch x := v.(type) {
	case nil:
		out.WriteString("null")
	case undefinedType:
		out.WriteString("undefined")
	case bool:
		out.WriteString(strconv.FormatBool(x))
	case string:
		out.WriteString(jsonString(x))
	case int:
		formatInteger(out, int64(x))
	case int8:
		formatInteger(out, int64(x))
	case int16:
		formatInteger(out, int64(x))
	case int32:
		formatInteger(out, int64(x))
	case int64:
		formatInteger(out, x)
	case uint:
		formatUnsigned(out, uint64(x))
	case uint8:
		formatUnsigned(out, uint64(x))
	case uint16:
		formatUnsigned(out, uint64(x))
	case uint32:
		formatUnsigned(out, uint64(x))
	case uint64:
		formatUnsigned(out, x)
	case float32:
		out.WriteString(jsNumber(float64(x)))
	case float64:
		out.WriteString(jsNumber(x))
	case time.Time:
		fmt.Fprintf(out, "ISODate(%q)", isoString(x))
	case CalendarDate:
		fmt.Fprintf(out, "Date(%q)", x.String())
	case Decimal128:
		fmt.Fprintf(out, "NumberDecimal(%q)", x.String())
	case Timestamp:
		fmt.Fprintf(out, "Timestamp(%d, %d)", x.Seconds, x.Increment)
	case Code:
		out.WriteString("Code(" + jsonString(x.Source) + ")")
	case ObjectID:
		fmt.Fprintf(out, "ObjectId(%q)", x.String())
	case RegExp:
		out.WriteString("/" + x.Pattern + "/" + x.Flags)
	case MinKey:
		out.WriteString("MinKey")
	case MaxKey:
		out.WriteString("MaxKey")
	case Binary:
		fmt.Fprintf(out, "BinData(%d, %q)", x.Subtype, hex.EncodeToString(x.Data))
	case []byte:
		fmt.Fprintf(out, "BinData(0, %q)", hex.EncodeToString(x))
	case Array:
		formatArray(out, x, indent, depth, pad)
	case D:
		formatFields(out, x, indent, depth, pad)
	case Doc:
		formatFields(out, sortedD(x), indent, depth, pad)
	default:
		converted, err := Marshal(v)
		if err != nil {
			fmt.Fprintf(out, "%v", v)
			return
		}
		formatValue(out, converted, indent, depth)
	}
}

func formatArray(out *strings.Builder, list Array, indent, depth int, pad func(int) string) {
	if len(list) == 0 {
		out.WriteString("[]")
		return
	}
	out.WriteString("[")
	for i, item := range list {
		if i > 0 {
			out.WriteString(",")
		}
		out.WriteString(pad(depth + 1))
		formatValue(out, item, indent, depth+1)
	}
	out.WriteString(pad(depth) + "]")
}

func formatFields(out *strings.Builder, fields D, indent, depth int, pad func(int) string) {
	if len(fields) == 0 {
		out.WriteString("{}")
		return
	}
	out.WriteString("{")
	for i, e := range fields {
		if i > 0 {
			out.WriteString(",")
		}
		out.WriteString(pad(depth+1) + jsonString(e.Key) + ": ")
		formatValue(out, e.Value, indent, depth+1)
	}
	out.WriteString(pad(depth) + "}")
}

func formatInteger(out *strings.Builder, v int64) {
	if v >= -maxSafeInteger && v <= maxSafeInteger {
		out.WriteString(strconv.FormatInt(v, 10))
		return
	}
	fmt.Fprintf(out, "NumberLong(%q)", strconv.FormatInt(v, 10))
}

func formatUnsigned(out *strings.Builder, v uint64) {
	if v <= maxSafeInteger {
		out.WriteString(strconv.FormatUint(v, 10))
		return
	}
	fmt.Fprintf(out, "NumberLong(%q)", strconv.FormatUint(v, 10))
}

// jsonString quotes s like JavaScript's JSON.stringify.
func jsonString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	text := strings.TrimSuffix(buf.String(), "\n")
	text = strings.ReplaceAll(text, ` `, " ")
	return strings.ReplaceAll(text, ` `, " ")
}

// jsNumber formats f like JavaScript's Number.prototype.toString.
func jsNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		if math.Signbit(f) {
			return "-0"
		}
		return "0"
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	mantissa, exp, _ := strings.Cut(strconv.FormatFloat(f, 'e', -1, 64), "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	e, _ := strconv.Atoi(exp)
	k, n := len(digits), e+1 // f = 0.digits * 10^n
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	exponent := fmt.Sprintf("e%+d", n-1)
	if k == 1 {
		return sign + digits + exponent
	}
	return sign + digits[:1] + "." + digits[1:] + exponent
}

// isoString formats t like JavaScript's Date.prototype.toISOString.
func isoString(t time.Time) string {
	u := t.UTC()
	year := u.Year()
	if year >= 0 && year <= 9999 {
		return u.Format("2006-01-02T15:04:05.000Z")
	}
	sign := "+"
	if year < 0 {
		sign, year = "-", -year
	}
	return fmt.Sprintf("%s%06d%s", sign, year, u.Format("-01-02T15:04:05.000Z"))
}
