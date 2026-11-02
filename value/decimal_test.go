package value_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kdoqdb/kdoq-go/value"
)

// The corpus files are copied from crates/kdoq-document/tests/fixtures/decimal
// (the MongoDB BSON decimal128 corpus), the vectors the Rust implementation
// is checked against.
type corpusCase struct {
	Description       string `json:"description"`
	CanonicalBSON     string `json:"canonical_bson"`
	CanonicalExtJSON  string `json:"canonical_extjson"`
	DegenerateExtJSON string `json:"degenerate_extjson"`
	Lossy             bool   `json:"lossy"`
}

type corpus struct {
	Valid       []corpusCase `json:"valid"`
	ParseErrors []struct {
		Description string `json:"description"`
		String      string `json:"string"`
	} `json:"parseErrors"`
}

func numberDecimal(t *testing.T, extJSON string) string {
	t.Helper()
	var doc struct {
		D struct {
			N string `json:"$numberDecimal"`
		} `json:"d"`
	}
	if err := json.Unmarshal([]byte(extJSON), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.D.N
}

func TestDecimal128Corpus(t *testing.T) {
	files, err := filepath.Glob("testdata/decimal/bson-decimal128-*.json")
	if err != nil || len(files) != 4 {
		t.Fatalf("corpus files: %v %v", files, err)
	}
	valid := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var c corpus
		if err := json.Unmarshal(data, &c); err != nil {
			t.Fatal(err)
		}
		for _, tc := range c.Valid {
			valid++
			raw, err := hex.DecodeString(tc.CanonicalBSON)
			if err != nil {
				t.Fatal(err)
			}
			var d value.Decimal128
			copy(d[:], raw[7:23])
			text := numberDecimal(t, tc.CanonicalExtJSON)
			if got := d.String(); got != text {
				t.Errorf("%s: String() = %q, want %q", tc.Description, got, text)
			}
			if !tc.Lossy {
				parsed, err := value.ParseDecimal128(text)
				if err != nil {
					t.Errorf("%s: parse %q: %v", tc.Description, text, err)
				} else if parsed != d {
					t.Errorf("%s: parse %q = %x, want %x", tc.Description, text, parsed.Bytes(), d.Bytes())
				}
			}
			if tc.DegenerateExtJSON != "" {
				degenerate := numberDecimal(t, tc.DegenerateExtJSON)
				parsed, err := value.ParseDecimal128(degenerate)
				if err != nil {
					t.Errorf("%s: parse degenerate %q: %v", tc.Description, degenerate, err)
				} else if parsed.String() != text {
					t.Errorf("%s: degenerate %q prints %q, want %q", tc.Description, degenerate, parsed.String(), text)
				}
			}
		}
		for _, bad := range c.ParseErrors {
			if _, err := value.ParseDecimal128(bad.String); err == nil {
				t.Errorf("%s: %q parsed, want an error", bad.Description, bad.String)
			}
		}
	}
	if valid != 538 {
		t.Fatalf("exercised %d valid cases, want 538", valid)
	}
}

func TestDecimal128KnownBytes(t *testing.T) {
	cases := map[string]string{
		"1":    "01" + "00000000000000000000000000" + "4030",
		"-0.1": "01" + "00000000000000000000000000" + "3eb0",
	}
	for text, want := range cases {
		d := mustDecimal(t, text)
		if got := hex.EncodeToString(d.Bytes()); got != want {
			t.Errorf("%s: bytes %s, want %s", text, got, want)
		}
	}
}

func TestDecimal128RejectsInexactText(t *testing.T) {
	for _, text := range []string{"1.00000000000000000000000000000000001", "abc", "", ".", "1e", "--1"} {
		if _, err := value.ParseDecimal128(text); err == nil {
			t.Errorf("%q parsed, want an error", text)
		}
	}
	if got := mustDecimal(t, "1e3").String(); got != "1E+3" {
		t.Errorf("1e3 prints %q, want 1E+3", got)
	}
}

func mustDecimal(t *testing.T, text string) value.Decimal128 {
	t.Helper()
	d, err := value.ParseDecimal128(text)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
