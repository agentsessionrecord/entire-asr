package pyjson

import "testing"

// Expected values come from Python's json and decimal modules.

func TestDecimalText(t *testing.T) {
	for source, want := range map[string]string{"1e5": "1E+5", "1.50": "1.50", "-0.0": "-0.0", "1e-7": "1E-7",
		"0.000001": "0.000001", "0E+10": "0E+10", "12.340e2": "1234.0", "1E+2": "1E+2", "-1.0e-10": "-1.0E-10",
		"0.10": "0.10", "123456789012345678901234567890.5": "123456789012345678901234567890.5", "1e-6": "0.000001",
		"1.0E-6": "0.0000010"} {
		v, err := Parse(source, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := v.(Decimal).Text; got != want {
			t.Errorf("%s: got %s, want %s", source, got, want)
		}
	}
}

func TestStrictAndLenientParsing(t *testing.T) {
	if _, err := Parse(`{"a":1,"a":2}`, true); err == nil || err.Error() != "duplicate JSON key" {
		t.Fatalf("duplicate key: %v", err)
	}
	v, err := Parse(`{"a":1,"a":2}`, false)
	if err != nil || AsObject(v).Value("a") != int64(2) {
		t.Fatal("lenient duplicate key")
	}
	if _, err := Parse(`[NaN]`, true); err == nil {
		t.Fatal("NaN accepted")
	}
	if _, err := Parse("\"a\x01\"", false); err == nil {
		t.Fatal("control character accepted")
	}
	_, err = Parse(`{"a": tru}`, true)
	if err == nil || err.Error() != "Expecting value: line 1 column 7 (char 6)" {
		t.Fatalf("position: %v", err)
	}
	v, _ = Parse(`"😀\ud800"`, true)
	if err := CheckUTF8(v.(string)); err == nil {
		t.Fatal("lone surrogate accepted for output")
	}
}

func TestDumps(t *testing.T) {
	v := Obj("b", "\u00e9 \x7f", "a", []any{int64(1), true, nil, Decimal{"1.50"}})
	got, _ := Dumps(v, Options{EnsureASCII: true, SortKeys: true, DecimalAsString: true})
	if got != `{"a": [1, true, null, "1.50"], "b": "\u00e9 \u007f"}` {
		t.Fatal(got)
	}
	if got, _ := Dumps(v.Value("b"), Options{Compact: true}); got != "\"\u00e9 \x7f\"" {
		t.Fatal(got)
	}
	if got, _ := Dumps(Obj("a", []any{int64(1)}), Options{Indent: 2}); got != "{\n  \"a\": [\n    1\n  ]\n}" {
		t.Fatal(got)
	}
	if _, err := Dumps(Decimal{"1.5"}, Options{}); err == nil {
		t.Fatal("decimal serialized")
	}
}

func TestDecodeReplace(t *testing.T) {
	for in, want := range map[string]string{"a\xffb": "a�b", "\xe2\x82": "�", "\xed\xa0\x80": "���",
		"\xf0\x9f\x98": "�", "ok é": "ok é"} {
		if got := DecodeReplace([]byte(in)); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestEqualMatchesPython(t *testing.T) {
	if !Equal(int64(1), true) || !Equal(Decimal{"1.0"}, int64(1)) || Equal("1", int64(1)) || !Equal([]any{}, []any{}) {
		t.Fatal("equality")
	}
}

func TestErrorMessagesMatchPython(t *testing.T) {
	cases := map[string]string{
		"":               "Expecting value: line 1 column 1 (char 0)",
		" ":              "Expecting value: line 1 column 2 (char 1)",
		"{":              "Expecting property name enclosed in double quotes: line 1 column 2 (char 1)",
		`{"a"`:           "Expecting ':' delimiter: line 1 column 5 (char 4)",
		`{"a":`:          "Expecting value: line 1 column 6 (char 5)",
		`{"a":1`:         "Expecting ',' delimiter: line 1 column 7 (char 6)",
		`{"a":1,`:        "Expecting property name enclosed in double quotes: line 1 column 8 (char 7)",
		`{"a" 1}`:        "Expecting ':' delimiter: line 1 column 6 (char 5)",
		"{a:1}":          "Expecting property name enclosed in double quotes: line 1 column 2 (char 1)",
		"[1,]":           "Expecting value: line 1 column 4 (char 3)",
		"[1 2]":          "Expecting ',' delimiter: line 1 column 4 (char 3)",
		`"abc`:           "Unterminated string starting at: line 1 column 1 (char 0)",
		`"a\x"`:          `Invalid \escape: line 1 column 3 (char 2)`,
		`"a\u12"`:        `Invalid \uXXXX escape: line 1 column 4 (char 3)`,
		`"a\u12zz"`:      `Invalid \uXXXX escape: line 1 column 4 (char 3)`,
		"\"a\x01\"":      "Invalid control character at: line 1 column 3 (char 2)",
		"1 2":            "Extra data: line 1 column 3 (char 2)",
		"tru":            "Expecting value: line 1 column 1 (char 0)",
		"-":              "Expecting value: line 1 column 1 (char 0)",
		"01":             "Extra data: line 1 column 2 (char 1)",
		"1.":             "Extra data: line 1 column 2 (char 1)",
		"1e":             "Extra data: line 1 column 2 (char 1)",
		`["é", x]`:       "Expecting value: line 1 column 7 (char 6)",
		`{"k":"v"}x`:     "Extra data: line 1 column 10 (char 9)",
		"\n\n  [1,\n x]": "Expecting value: line 4 column 2 (char 9)",
	}
	for doc, want := range cases {
		_, err := Parse(doc, false)
		if err == nil || err.Error() != want {
			t.Errorf("%q: got %v, want %s", doc, err, want)
		}
	}
	decode := map[string]string{
		"\xe2\x28\xa1":      "'utf-8' codec can't decode byte 0xe2 in position 0: invalid continuation byte",
		"ab\xe2\x82":        "'utf-8' codec can't decode bytes in position 2-3: unexpected end of data",
		"\xff":              "'utf-8' codec can't decode byte 0xff in position 0: invalid start byte",
		"a\xc3":             "'utf-8' codec can't decode byte 0xc3 in position 1: unexpected end of data",
		"\xf0\x9f\x98\x28":  "'utf-8' codec can't decode bytes in position 0-2: invalid continuation byte",
		"\xed\xa0\x80":      "'utf-8' codec can't decode byte 0xed in position 0: invalid continuation byte",
		"\xc0\x80":          "'utf-8' codec can't decode byte 0xc0 in position 0: invalid start byte",
		"x\xf4\x90\x80\x80": "'utf-8' codec can't decode byte 0xf4 in position 1: invalid continuation byte",
	}
	for in, want := range decode {
		_, err := DecodeStrict([]byte(in))
		if err == nil || err.Error() != want {
			t.Errorf("%q: got %v, want %s", in, err, want)
		}
	}
}
