package value_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kdoqdb/kdoq-go/value"
)

type Key string

type tagged struct {
	ID       Key `kdoq:",key"`
	Plain    string
	Renamed  string `kdoq:"new_name"`
	JSONOnly string `json:"from_json"`
	Both     string `kdoq:"kdoq_wins" json:"json_loses"`
	Fallback string `kdoq:",omitempty" json:"json_name"`
	Skipped  string `kdoq:"-"`
	JSONSkip string `json:"-"`
	hidden   string
	Count    int32
	Big      uint32
}

func TestMarshalStructTagOrder(t *testing.T) {
	in := tagged{
		ID: "k1", Plain: "p", Renamed: "r", JSONOnly: "j", Both: "b", Fallback: "f",
		Skipped: "s", JSONSkip: "js", hidden: "h", Count: 3, Big: 4,
	}
	want := value.D{
		{Key: "Plain", Value: "p"},
		{Key: "new_name", Value: "r"},
		{Key: "from_json", Value: "j"},
		{Key: "kdoq_wins", Value: "b"},
		{Key: "json_name", Value: "f"},
		{Key: "Count", Value: int32(3)},
		{Key: "Big", Value: int64(4)},
	}
	if got := marshalOK(t, in); !reflect.DeepEqual(got, want) {
		t.Errorf("Marshal = %#v\nwant %#v", got, want)
	}
	if got := marshalOK(t, &in); !reflect.DeepEqual(got, want) {
		t.Errorf("Marshal(&struct) = %#v", got)
	}
}

type omitting struct {
	EmptyStr  string            `kdoq:",omitempty"`
	Zero      int               `kdoq:",omitempty"`
	False     bool              `kdoq:",omitempty"`
	NilPtr    *int              `kdoq:",omitempty"`
	EmptyList []int             `kdoq:",omitempty"`
	EmptyMap  map[string]string `kdoq:",omitempty"`
	JSONEmpty string            `json:"j_empty,omitempty"`
	JSONZero  time.Time         `json:"j_zero,omitzero"`
	KdoqZero  value.Timestamp   `kdoq:",omitzero"`
	NotOmit   int
	Pointer0  *int `kdoq:",omitempty"`
}

func TestMarshalOmitEmptyAndOmitZero(t *testing.T) {
	zero := 0
	got := marshalOK(t, omitting{Pointer0: &zero})
	want := value.D{
		{Key: "NotOmit", Value: int64(0)},
		{Key: "Pointer0", Value: int64(0)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("zero struct: %#v\nwant %#v", got, want)
	}
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	got = marshalOK(t, omitting{
		EmptyStr: "a", Zero: 1, False: true, EmptyList: []int{1}, EmptyMap: map[string]string{"k": "v"},
		JSONEmpty: "e", JSONZero: when, KdoqZero: value.Timestamp{Seconds: 1},
	})
	names := []string{}
	for _, e := range got.(value.D) {
		names = append(names, e.Key)
	}
	wantNames := []string{"EmptyStr", "Zero", "False", "EmptyList", "EmptyMap", "j_empty", "j_zero", "KdoqZero", "NotOmit"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Errorf("fields = %v, want %v", names, wantNames)
	}
}

type base struct {
	Created string
	Tag     string `kdoq:"base_tag"`
}

type withEmbedded struct {
	base
	*Extra
	Own string
}

type Extra struct{ Note string }

func TestMarshalFlattensEmbeddedStructs(t *testing.T) {
	got := marshalOK(t, withEmbedded{base: base{Created: "c", Tag: "t"}, Extra: &Extra{Note: "n"}, Own: "o"})
	want := value.D{
		{Key: "Created", Value: "c"}, {Key: "base_tag", Value: "t"},
		{Key: "Note", Value: "n"}, {Key: "Own", Value: "o"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Marshal = %#v\nwant %#v", got, want)
	}
	got = marshalOK(t, withEmbedded{Own: "o"})
	if want := (value.D{{Key: "Created", Value: ""}, {Key: "base_tag", Value: ""}, {Key: "Own", Value: "o"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("nil embedded pointer: %#v", got)
	}
}

func TestMarshalRejectsDuplicateFieldNames(t *testing.T) {
	type dup struct {
		A string `kdoq:"x"`
		B string `json:"x"`
	}
	if _, err := value.Marshal(dup{}); err == nil {
		t.Error("duplicate field names accepted")
	}
}

func TestKeyOfReportsTheTaggedFieldSeparately(t *testing.T) {
	key, ok, err := value.KeyOf(tagged{ID: "k1"})
	if err != nil || !ok || key != "k1" {
		t.Errorf("KeyOf = %q %v %v", key, ok, err)
	}
	key, ok, err = value.KeyOf(&tagged{ID: "\xff\x00"})
	if err != nil || !ok || key != "\xff\x00" {
		t.Errorf("binary key = %q %v %v", key, ok, err)
	}
	if _, ok, _ := value.KeyOf(tagged{}); ok {
		t.Error("an empty key field reported a key")
	}
	if _, ok, _ := value.KeyOf(struct{ A int }{1}); ok {
		t.Error("a struct without a key field reported a key")
	}
	if _, ok, _ := value.KeyOf(value.Doc{"ID": "x"}); ok {
		t.Error("a document reported a key")
	}
	if _, _, err := value.KeyOf(struct {
		K int `kdoq:",key"`
	}{1}); err == nil {
		t.Error("a non-string key field was accepted")
	}
	if _, err := value.Marshal(struct {
		A string `kdoq:",key"`
		B string `kdoq:",key"`
	}{}); err == nil {
		t.Error("two key fields were accepted")
	}
}

// Hooks.

type celsius float64

func (c celsius) MarshalKdoq() (any, error) {
	return value.Doc{"degrees": float64(c), "unit": "C"}, nil
}

type pointerHook struct{ n int }

func (p *pointerHook) MarshalKdoq() (any, error) { return "ptr:" + string(rune('0'+p.n)), nil }

type failingHook struct{}

func (failingHook) MarshalKdoq() (any, error) { return nil, errors.New("boom") }

type nested struct {
	Temp  celsius
	List  []celsius
	Map   map[string]celsius
	Ptr   *celsius
	Hook  pointerHook
	Inner struct{ T celsius }
}

func TestMarshalHonorsMarshalersForNestedValues(t *testing.T) {
	c := celsius(1.5)
	got := marshalOK(t, nested{
		Temp: 20, List: []celsius{1}, Map: map[string]celsius{"k": 2}, Ptr: &c, Hook: pointerHook{n: 3},
		Inner: struct{ T celsius }{4},
	})
	deg := func(v float64) value.Doc { return value.Doc{"degrees": v, "unit": "C"} }
	want := value.D{
		{Key: "Temp", Value: deg(20)},
		{Key: "List", Value: value.Array{deg(1)}},
		{Key: "Map", Value: value.Doc{"k": deg(2)}},
		{Key: "Ptr", Value: deg(1.5)},
		{Key: "Hook", Value: "ptr:3"},
		{Key: "Inner", Value: value.D{{Key: "T", Value: deg(4)}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Marshal = %#v\nwant %#v", got, want)
	}
	if got := marshalOK(t, (*celsius)(nil)); got != nil {
		t.Errorf("nil pointer to a marshaler: %#v", got)
	}
	if got := marshalOK(t, celsius(2)); !reflect.DeepEqual(got, deg(2)) {
		t.Errorf("top-level marshaler: %#v", got)
	}
}

func TestMarshalReportsHookErrorsWithPath(t *testing.T) {
	_, err := value.Marshal(struct{ Field failingHook }{})
	var verr *value.Error
	if !asError(err, &verr) || verr.Path != "Field" {
		t.Fatalf("error %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("hook error text lost: %v", err)
	}
}
