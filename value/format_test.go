package value_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go/value"
)

type formatCase struct {
	Name     string          `json:"name"`
	Indent   int             `json:"indent"`
	Value    json.RawMessage `json:"value"`
	Expected string          `json:"expected"`
}

// format_cases.json holds the same values as the TypeScript formatter of the
// test console formats; "expected" is its output (see fill_format_cases.ts).
func TestFormatMatchesTheTypeScriptFormatter(t *testing.T) {
	data, err := os.ReadFile("testdata/format_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []formatCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 80 {
		t.Fatalf("only %d cases", len(cases))
	}
	for _, tc := range cases {
		if tc.Expected == "" {
			t.Fatalf("%s: no expected output; run fill_format_cases.ts", tc.Name)
		}
		v := revive(t, tc.Value)
		if got := value.Format(v, tc.Indent); got != tc.Expected {
			t.Errorf("%s:\n got %q\nwant %q", tc.Name, got, tc.Expected)
		}
	}
}

func TestFormatPlainGoValues(t *testing.T) {
	type person struct {
		ID   Key `kdoq:",key"`
		Name string
		Age  int32
		Raw  []byte
	}
	cases := []struct {
		in     any
		indent int
		want   string
	}{
		{int(5), 0, "5"},
		{int8(-5), 0, "-5"},
		{uint8(200), 0, "200"},
		{uint64(math.MaxUint64), 0, `NumberLong("18446744073709551615")`},
		{float32(0.5), 0, "0.5"},
		{Key("k"), 0, `"k"`},
		{[]string{"a", "b"}, 0, `["a","b"]`},
		{map[string]int{"b": 1, "a": 2}, 0, `{"a": 2,"b": 1}`},
		{person{ID: "k", Name: "Ada", Age: 3, Raw: []byte{0xab}}, 0, `{"Name": "Ada","Age": 3,"Raw": BinData(0, "ab")}`},
		{&person{Name: "n"}, 0, `{"Name": "n","Age": 0,"Raw": null}`},
		{make(chan int), 0, ""},
	}
	for _, tc := range cases {
		got := value.Format(tc.in, tc.indent)
		if tc.want == "" {
			if got == "" {
				t.Errorf("%T: empty output", tc.in)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("%T: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// revive builds Go values from the tagged JSON of format_cases.json, keeping
// the order of object fields (D).
func revive(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := reviveToken(dec)
	if err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return v
}

func reviveToken(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch x := tok.(type) {
	case json.Delim:
		if x == '[' {
			list := value.Array{}
			for dec.More() {
				item, err := reviveToken(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, item)
			}
			_, err := dec.Token()
			return list, err
		}
		var fields value.D
		for dec.More() {
			name, _ := dec.Token()
			item, err := reviveToken(dec)
			if err != nil {
				return nil, err
			}
			fields = append(fields, value.E{Key: name.(string), Value: item})
		}
		_, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if len(fields) == 1 && strings.HasPrefix(fields[0].Key, "$") {
			return reviveTagged(fields[0].Key, fields[0].Value)
		}
		return nil, fmt.Errorf("untagged object %v", fields)
	case json.Number:
		return x.Int64()
	case string, bool, nil:
		return x, nil
	}
	return nil, fmt.Errorf("unexpected token %v", tok)
}

func reviveTagged(tag string, x any) (any, error) {
	list, _ := x.(value.Array)
	switch tag {
	case "$int", "$uint":
		n, ok := new(big.Int).SetString(x.(string), 10)
		if !ok {
			return nil, fmt.Errorf("bad integer %v", x)
		}
		if tag == "$int" {
			return n.Int64(), nil
		}
		return n.Uint64(), nil
	case "$double":
		switch x.(string) {
		case "-0":
			return math.Copysign(0, -1), nil
		case "NaN":
			return math.NaN(), nil
		case "Infinity":
			return math.Inf(1), nil
		case "-Infinity":
			return math.Inf(-1), nil
		}
		return strconv.ParseFloat(x.(string), 64)
	case "$date":
		return parseISO(x.(string))
	case "$calendar":
		var y, m, d int
		_, err := fmt.Sscanf(x.(string), "%d-%d-%d", &y, &m, &d)
		return value.CalendarDate{Year: y, Month: uint8(m), Day: uint8(d)}, err
	case "$decimal":
		return value.ParseDecimal128(x.(string))
	case "$timestamp":
		return value.Timestamp{Seconds: uint32(list[0].(int64)), Increment: uint32(list[1].(int64))}, nil
	case "$code":
		return value.Code{Source: x.(string)}, nil
	case "$oid":
		raw, err := hex.DecodeString(x.(string))
		var id value.ObjectID
		copy(id[:], raw)
		return id, err
	case "$regex":
		return value.RegExp{Pattern: list[0].(string), Flags: list[1].(string)}, nil
	case "$minkey":
		return value.MinKey{}, nil
	case "$maxkey":
		return value.MaxKey{}, nil
	case "$undefined":
		return value.Undefined, nil
	case "$bin":
		raw, err := hex.DecodeString(list[1].(string))
		return value.Binary{Subtype: byte(list[0].(int64)), Data: raw}, err
	case "$doc":
		return docFromPairs(x), nil
	}
	return nil, fmt.Errorf("unknown tag %s", tag)
}

func docFromPairs(x any) any {
	out := value.D{}
	if list, ok := x.(value.Array); ok {
		for _, pair := range list {
			p := pair.(value.Array)
			out = append(out, value.E{Key: p[0].(string), Value: p[1]})
		}
	}
	return out
}

// parseISO reads the extended ISO years the JavaScript Date prints.
func parseISO(s string) (time.Time, error) {
	sign := 1
	if s[0] == '+' || s[0] == '-' {
		if s[0] == '-' {
			sign = -1
		}
		year, err := strconv.Atoi(s[1:7])
		if err != nil {
			return time.Time{}, err
		}
		t, err := time.Parse("2006-01-02T15:04:05.000Z", "2000"+s[7:])
		return t.AddDate(sign*year-2000, 0, 0), err
	}
	return time.Parse("2006-01-02T15:04:05.000Z", s)
}
