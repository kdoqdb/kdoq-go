package value_test

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go/value"
)

func TestUnmarshalWidensIntegers(t *testing.T) {
	type target struct {
		I8  int8
		I16 int16
		I32 int32
		I64 int64
		Int int
		U8  uint8
		U32 uint32
		U64 uint64
		F64 float64
		F32 float32
	}
	src := value.Doc{
		"I8": int32(-128), "I16": int32(32767), "I32": int32(math.MinInt32), "I64": int32(5), "Int": int64(math.MaxInt64),
		"U8": uint32(255), "U32": uint32(math.MaxUint32), "U64": uint64(math.MaxUint64),
		"F64": int32(3), "F32": float64(1.5),
	}
	var got target
	if err := value.Unmarshal(src, &got); err != nil {
		t.Fatal(err)
	}
	want := target{-128, 32767, math.MinInt32, 5, math.MaxInt64, 255, math.MaxUint32, math.MaxUint64, 3, 1.5}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestUnmarshalFailsOnOverflow(t *testing.T) {
	cases := []struct {
		name string
		src  any
		dst  any
	}{
		{"int32 into int8", int32(128), new(int8)},
		{"int32 below int8", int32(-129), new(int8)},
		{"int64 into int32", int64(math.MaxInt32) + 1, new(int32)},
		{"int64 into int16", int64(40000), new(int16)},
		{"negative into uint8", int32(-1), new(uint8)},
		{"negative into uint64", int64(-1), new(uint64)},
		{"uint32 into uint8", uint32(256), new(uint8)},
		{"uint64 into int64", uint64(math.MaxInt64) + 1, new(int64)},
		{"uint64 into uint32", uint64(math.MaxUint32) + 1, new(uint32)},
		{"double into int32", float64(1 << 40), new(int32)},
		{"fractional double into int", 1.5, new(int)},
		{"nan into int", math.NaN(), new(int)},
		{"double above float32", math.MaxFloat64, new(float32)},
		{"string into int", "7", new(int)},
		{"int into string", int32(7), new(string)},
		{"int into bool", int32(1), new(bool)},
		{"doc into slice", value.Doc{}, new([]int)},
		{"array into struct", value.Array{}, new(struct{ A int })},
	}
	for _, tc := range cases {
		if err := value.Unmarshal(tc.src, tc.dst); err == nil {
			t.Errorf("%s: no error, got %v", tc.name, reflect.ValueOf(tc.dst).Elem())
		}
	}
	// 2^63 as a double is not an int64.
	if err := value.Unmarshal(float64(1<<63), new(int64)); err == nil {
		t.Error("2^63 double accepted as int64")
	}
	var f float64
	if err := value.Unmarshal(int64(1<<53+1), &f); err == nil {
		t.Error("an int64 that float64 cannot hold exactly was accepted")
	}
	var i int64
	if err := value.Unmarshal(float64(3), &i); err != nil || i != 3 {
		t.Errorf("integral double: %d %v", i, err)
	}
}

func TestUnmarshalErrorNamesThePath(t *testing.T) {
	var dst struct{ Inner struct{ List []int8 } }
	err := value.Unmarshal(value.Doc{"Inner": value.Doc{"List": value.Array{int32(1), int32(300)}}}, &dst)
	var verr *value.Error
	if !asError(err, &verr) || verr.Path != "Inner.List[1]" {
		t.Errorf("error %v", err)
	}
}

func TestUnmarshalEveryValueType(t *testing.T) {
	when := time.Date(2026, 11, 2, 10, 30, 0, 123_000_000, time.UTC)
	dec := mustDecimal(t, "-1.5E+3")
	src := value.Doc{
		"Null": nil, "Flag": true, "Text": "héllo", "When": when,
		"Bytes": []byte{1, 2}, "Bin": value.Binary{Subtype: 4, Data: []byte{7}}, "Dec": dec,
		"TS": value.Timestamp{Seconds: 9, Increment: 1}, "Code": value.Code{Source: "x"},
		"ID": value.ObjectID{1}, "Day": value.CalendarDate{Year: 2026, Month: 11, Day: 2},
		"Min": value.MinKey{}, "Max": value.MaxKey{}, "Re": value.RegExp{Pattern: "^a", Flags: "i"},
		"Undef": value.Undefined, "Any": int32(5), "AnyDoc": value.Doc{"k": "v"},
		"Ordered": value.Doc{"b": int32(2), "a": int32(1)},
		"List":    value.Array{int32(1), "x"}, "Fixed": value.Array{true, false},
		"Map": value.Doc{"a": int32(1)}, "Ptr": int32(8), "NilPtr": nil, "BinBytes": value.Binary{Data: []byte{5, 6}},
	}
	type target struct {
		Null     string
		Flag     bool
		Text     string
		When     time.Time
		Bytes    []byte
		Bin      value.Binary
		Dec      value.Decimal128
		TS       value.Timestamp
		Code     value.Code
		ID       value.ObjectID
		Day      value.CalendarDate
		Min      value.MinKey
		Max      value.MaxKey
		Re       value.RegExp
		Undef    any
		Any      any
		AnyDoc   value.Doc
		Ordered  value.D
		List     value.Array
		Fixed    [2]bool
		Map      map[string]int
		Ptr      *int
		NilPtr   *int
		BinBytes []byte
	}
	var got target
	if err := value.Unmarshal(src, &got); err != nil {
		t.Fatal(err)
	}
	eight := 8
	want := target{
		Flag: true, Text: "héllo", When: when, Bytes: []byte{1, 2}, Bin: value.Binary{Subtype: 4, Data: []byte{7}}, Dec: dec,
		TS: value.Timestamp{Seconds: 9, Increment: 1}, Code: value.Code{Source: "x"}, ID: value.ObjectID{1},
		Day: value.CalendarDate{Year: 2026, Month: 11, Day: 2}, Re: value.RegExp{Pattern: "^a", Flags: "i"},
		Undef: value.Undefined, Any: int32(5), AnyDoc: value.Doc{"k": "v"},
		Ordered: value.D{{Key: "a", Value: int32(1)}, {Key: "b", Value: int32(2)}},
		List:    value.Array{int32(1), "x"}, Fixed: [2]bool{true, false}, Map: map[string]int{"a": 1}, Ptr: &eight,
		BinBytes: []byte{5, 6},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestUnmarshalIntoAnyKeepsTheReadType(t *testing.T) {
	var got any
	src := value.Doc{"a": int32(1), "b": uint64(2)}
	if err := value.Unmarshal(src, &got); err != nil || !reflect.DeepEqual(got, src) {
		t.Errorf("got %#v %v", got, err)
	}
	var doc value.Doc
	if err := value.Unmarshal(src, &doc); err != nil || !reflect.DeepEqual(doc, src) {
		t.Errorf("Doc: %#v %v", doc, err)
	}
	var ordered value.D
	if err := value.Unmarshal(value.D{{Key: "z", Value: nil}}, &ordered); err != nil || len(ordered) != 1 {
		t.Errorf("D: %#v %v", ordered, err)
	}
}

func TestUnmarshalNeedsAPointer(t *testing.T) {
	for _, dst := range []any{nil, 5, struct{}{}, (*int)(nil)} {
		if err := value.Unmarshal(int32(1), dst); err == nil {
			t.Errorf("destination %#v accepted", dst)
		}
	}
}

func TestUnmarshalIgnoresUnknownFieldsAndMissingOnes(t *testing.T) {
	type target struct {
		Keep  string `kdoq:"kept"`
		Other int    `json:"other"`
		Left  string
	}
	dst := target{Left: "stay"}
	err := value.Unmarshal(value.Doc{"kept": "k", "other": int32(2), "unknown": value.Doc{"deep": int32(1)}}, &dst)
	if err != nil || dst != (target{Keep: "k", Other: 2, Left: "stay"}) {
		t.Errorf("got %+v %v", dst, err)
	}
	err = value.Unmarshal(value.D{{Key: "kept", Value: "d"}}, &dst)
	if err != nil || dst.Keep != "d" {
		t.Errorf("ordered source: %+v %v", dst, err)
	}
}

func TestUnmarshalKeyedFillsTheKeyField(t *testing.T) {
	var dst tagged
	err := value.UnmarshalKeyed(value.Doc{"Plain": "p", "ID": "from body", "kdoq_wins": "b"}, "\xffk", &dst)
	if err != nil || dst.ID != "\xffk" || dst.Plain != "p" || dst.Both != "b" {
		t.Errorf("got %+v %v", dst, err)
	}
	var plain tagged
	if err := value.Unmarshal(value.Doc{"Plain": "p", "ID": "ignored"}, &plain); err != nil || plain.ID != "" {
		t.Errorf("Unmarshal touched the key: %+v %v", plain, err)
	}
	var pp *tagged
	if err := value.UnmarshalKeyed(value.Doc{}, "k", &pp); err != nil || pp == nil || pp.ID != "k" {
		t.Errorf("pointer destination: %+v %v", pp, err)
	}
	var doc value.Doc
	if err := value.UnmarshalKeyed(value.Doc{"a": int32(1)}, "k", &doc); err != nil || len(doc) != 1 {
		t.Errorf("document destination: %v %v", doc, err)
	}
}

func TestMarshalUnmarshalRoundTripsAStruct(t *testing.T) {
	type address struct {
		Street string
		Zip    *string `kdoq:",omitempty"`
	}
	type person struct {
		ID      Key `kdoq:",key"`
		Name    string
		Age     int32
		Score   float64
		Tags    []string
		Address address
		Extra   map[string]int64
		Born    value.CalendarDate
		Seen    time.Time
	}
	zip := "12345"
	in := person{
		ID: "p1", Name: "Ada", Age: 36, Score: 9.5, Tags: []string{"a", "b"}, Address: address{Street: "Main", Zip: &zip},
		Extra: map[string]int64{"x": math.MinInt64}, Born: value.CalendarDate{Year: 1815, Month: 12, Day: 10},
		Seen: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	body := marshalOK(t, in)
	key, ok, err := value.KeyOf(in)
	if err != nil || !ok {
		t.Fatal(key, ok, err)
	}
	var out person
	if err := value.UnmarshalKeyed(body, key, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip\n in %+v\nout %+v", in, out)
	}
}

// Unmarshal hooks.

type fahrenheit float64

func (f *fahrenheit) UnmarshalKdoq(v any) error {
	doc, ok := v.(value.Doc)
	if !ok {
		return fmt.Errorf("want a document, got %T", v)
	}
	deg, _ := doc["degrees"].(float64)
	*f = fahrenheit(deg*9/5 + 32)
	return nil
}

func TestUnmarshalHonorsUnmarshalersForNestedValues(t *testing.T) {
	type target struct {
		One  fahrenheit
		List []fahrenheit
		Map  map[string]fahrenheit
		Ptr  *fahrenheit
		Nil  *fahrenheit
	}
	deg := func(v float64) value.Doc { return value.Doc{"degrees": v} }
	var got target
	err := value.Unmarshal(value.Doc{
		"One": deg(100), "List": value.Array{deg(0)}, "Map": value.Doc{"k": deg(-40)}, "Ptr": deg(10), "Nil": nil,
	}, &got)
	if err != nil {
		t.Fatal(err)
	}
	if got.One != 212 || got.List[0] != 32 || got.Map["k"] != -40 || got.Ptr == nil || *got.Ptr != 50 || got.Nil != nil {
		t.Errorf("got %+v", got)
	}
	err = value.Unmarshal(value.Doc{"One": "hot"}, &got)
	var verr *value.Error
	if !asError(err, &verr) || verr.Path != "One" || !strings.Contains(err.Error(), "want a document") {
		t.Errorf("hook error %v", err)
	}
	var top fahrenheit
	if err := value.Unmarshal(deg(100), &top); err != nil || top != 212 {
		t.Errorf("top-level hook: %v %v", top, err)
	}
}
