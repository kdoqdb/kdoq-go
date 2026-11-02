package value

import (
	"math"
	"reflect"
)

// 2^63 and 2^64 as float64: the first values above the int64 and uint64 ranges.
const (
	twoTo63 = float64(1 << 63)
	twoTo64 = 2 * twoTo63
)

func decodeNumber(src any, dv reflect.Value) error {
	sv := reflect.ValueOf(src)
	var (
		signed   int64
		unsigned uint64
		float    float64
		class    reflect.Kind // Int64, Uint64 or Float64
	)
	switch sv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		signed, class = sv.Int(), reflect.Int64
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		unsigned, class = sv.Uint(), reflect.Uint64
	case reflect.Float32, reflect.Float64:
		float, class = sv.Float(), reflect.Float64
	default:
		return mismatch(src, dv)
	}
	if !storeNumber(class, signed, unsigned, float, dv) {
		return failHere("%v overflows %s", src, dv.Type())
	}
	return nil
}

func integral(float float64) bool { return float == math.Trunc(float) && !math.IsInf(float, 0) }

// storeNumber stores the number, held in the field of its class (Int64,
// Uint64 or Float64), in the integer or floating-point value dv. It reports
// false when dv cannot hold the number exactly.
func storeNumber(class reflect.Kind, signed int64, unsigned uint64, float float64, dv reflect.Value) bool {
	switch dv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var v int64
		switch class {
		case reflect.Int64:
			v = signed
		case reflect.Uint64:
			if unsigned > math.MaxInt64 {
				return false
			}
			v = int64(unsigned)
		default:
			if !integral(float) || float < -twoTo63 || float >= twoTo63 {
				return false
			}
			v = int64(float)
		}
		if dv.OverflowInt(v) {
			return false
		}
		dv.SetInt(v)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		var v uint64
		switch class {
		case reflect.Int64:
			if signed < 0 {
				return false
			}
			v = uint64(signed)
		case reflect.Uint64:
			v = unsigned
		default:
			if !integral(float) || float < 0 || float >= twoTo64 {
				return false
			}
			v = uint64(float)
		}
		if dv.OverflowUint(v) {
			return false
		}
		dv.SetUint(v)
	default:
		var v float64
		switch class {
		case reflect.Int64:
			v = float64(signed)
			if v >= twoTo63 || int64(v) != signed {
				return false
			}
		case reflect.Uint64:
			v = float64(unsigned)
			if v >= twoTo64 || uint64(v) != unsigned {
				return false
			}
		default:
			v = float
		}
		if dv.OverflowFloat(v) || (class != reflect.Float64 && dv.Kind() == reflect.Float32 && float64(float32(v)) != v) {
			return false
		}
		dv.SetFloat(v)
	}
	return true
}
