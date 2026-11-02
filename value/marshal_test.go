package value_test

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go/value"
)

func marshalOK(t *testing.T, in any) any {
	t.Helper()
	out, err := value.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal(%#v): %v", in, err)
	}
	return out
}

func TestMarshalExactNumericTypes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any
	}{
		{"int8 min", int8(math.MinInt8), int32(math.MinInt8)},
		{"int16 max", int16(math.MaxInt16), int32(math.MaxInt16)},
		{"int32 min", int32(math.MinInt32), int32(math.MinInt32)},
		{"int32 max", int32(math.MaxInt32), int32(math.MaxInt32)},
		{"int small", 7, int64(7)},
		{"int64 min", int64(math.MinInt64), int64(math.MinInt64)},
		{"int64 max", int64(math.MaxInt64), int64(math.MaxInt64)},
		{"uint8 max", uint8(math.MaxUint8), int64(math.MaxUint8)},
		{"uint16 max", uint16(math.MaxUint16), int64(math.MaxUint16)},
		{"uint32 max", uint32(math.MaxUint32), int64(math.MaxUint32)},
		{"uint64 fits", uint64(math.MaxInt64), int64(math.MaxInt64)},
		{"uint fits", uint(5), int64(5)},
		{"float32", float32(1.5), float64(1.5)},
		{"float64", 0.1, 0.1},
		{"float64 max", math.MaxFloat64, math.MaxFloat64},
		{"float64 smallest", math.SmallestNonzeroFloat64, math.SmallestNonzeroFloat64},
		{"infinity", math.Inf(-1), math.Inf(-1)},
	}
	for _, tc := range cases {
		got := marshalOK(t, tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Marshal = %#v, want %#v", tc.name, got, tc.want)
		}
	}
	if got := marshalOK(t, math.NaN()).(float64); !math.IsNaN(got) {
		t.Errorf("NaN became %v", got)
	}
	if got := marshalOK(t, math.Copysign(0, -1)).(float64); !math.Signbit(got) {
		t.Errorf("negative zero lost its sign")
	}
}

func TestMarshalRejectsUnsignedAboveInt64(t *testing.T) {
	for _, in := range []any{uint64(math.MaxInt64) + 1, uint64(math.MaxUint64), uint(math.MaxUint64)} {
		if _, err := value.Marshal(in); err == nil {
			t.Errorf("Marshal(%v) succeeded, want an error", in)
		}
	}
}

func TestMarshalScalarsAndValueTypes(t *testing.T) {
	when := time.Date(2026, 11, 2, 10, 30, 0, 123_000_000, time.UTC)
	dec := mustDecimal(t, "12.50")
	cases := []struct {
		name string
		in   any
		want any
	}{
		{"nil", nil, nil},
		{"bool", true, true},
		{"string", "héllo", "héllo"},
		{"time", when, when},
		{"bytes", []byte{1, 2}, []byte{1, 2}},
		{"binary", value.Binary{Subtype: 4, Data: []byte{9}}, value.Binary{Subtype: 4, Data: []byte{9}}},
		{"decimal", dec, dec},
		{"timestamp", value.Timestamp{Seconds: 7, Increment: 3}, value.Timestamp{Seconds: 7, Increment: 3}},
		{"code", value.Code{Source: "x + 1"}, value.Code{Source: "x + 1"}},
		{"object id", value.ObjectID{1, 2, 3}, value.ObjectID{1, 2, 3}},
		{"calendar date", value.CalendarDate{Year: 2024, Month: 2, Day: 29}, value.CalendarDate{Year: 2024, Month: 2, Day: 29}},
		{"nil pointer", (*int)(nil), nil},
	}
	for _, tc := range cases {
		if got := marshalOK(t, tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Marshal = %#v, want %#v", tc.name, got, tc.want)
		}
	}
	n := 5
	if got := marshalOK(t, &n); got != int64(5) {
		t.Errorf("pointer: %#v", got)
	}
}

func TestMarshalRejectsWhatCannotBeWritten(t *testing.T) {
	cases := map[string]any{
		"undefined":          value.Undefined,
		"nested undefined":   value.Doc{"a": value.Array{1, value.Undefined}},
		"min key":            value.MinKey{},
		"max key":            value.MaxKey{},
		"regexp":             value.RegExp{Pattern: "a", Flags: "i"},
		"month 13":           value.CalendarDate{Year: 2026, Month: 13, Day: 1},
		"february 30":        value.CalendarDate{Year: 2026, Month: 2, Day: 30},
		"year 10000":         value.CalendarDate{Year: 10000, Month: 1, Day: 1},
		"day zero":           value.CalendarDate{Year: 2026, Month: 1},
		"channel":            make(chan int),
		"func":               func() {},
		"integer map key":    map[int]string{1: "a"},
		"complex":            complex(1, 2),
		"nested bad element": []any{1, make(chan int)},
	}
	for name, in := range cases {
		if _, err := value.Marshal(in); err == nil {
			t.Errorf("%s: Marshal succeeded, want an error", name)
		}
	}
	_, err := value.Marshal(value.Doc{"a": value.Array{1, value.Undefined}})
	var verr *value.Error
	if !asError(err, &verr) || verr.Path != "a[1]" {
		t.Errorf("error %v does not carry the path a[1]", err)
	}
}

func TestMarshalContainers(t *testing.T) {
	got := marshalOK(t, value.Doc{
		"list":  []int{1, 2},
		"names": []string{"a"},
		"inner": map[string]int32{"x": 1},
		"arr":   [2]bool{true, false},
		"d":     value.D{{Key: "z", Value: uint8(1)}, {Key: "a", Value: nil}},
	})
	want := value.Doc{
		"list":  value.Array{int64(1), int64(2)},
		"names": value.Array{"a"},
		"inner": value.Doc{"x": int32(1)},
		"arr":   value.Array{true, false},
		"d":     value.D{{Key: "z", Value: int64(1)}, {Key: "a", Value: nil}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Marshal = %#v\nwant %#v", got, want)
	}
}

func TestMarshalDepthLimit(t *testing.T) {
	var deep any = int64(1)
	for i := 0; i < 200; i++ {
		deep = value.Array{deep}
	}
	if _, err := value.Marshal(deep); err == nil || !strings.Contains(err.Error(), "deep") {
		t.Errorf("deeply nested value: %v", err)
	}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if _, err := value.Marshal(cyclic); err == nil {
		t.Errorf("cyclic map succeeded")
	}
}

func asError(err error, target **value.Error) bool {
	for err != nil {
		if e, ok := err.(*value.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
