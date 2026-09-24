package jsonorder

import "testing"

// 期待値は以前の CLI（1.0.0 より前）が出していた形（2026-09-19 に記録した）。
func TestFormatNumberGolden(t *testing.T) {
	cases := map[string]string{
		"1": "1", "-0": "0", "1.0": "1.0", "1.50": "1.5", "1e3": "1000.0", "1e16": "1e+16", "1e15": "1000000000000000.0",
		"0.0001": "0.0001", "0.00001": "1e-05", "1.5e-7": "1.5e-07", "123456789012345678901234567890": "123456789012345678901234567890",
		"-0.0": "-0.0", "1E400": "Infinity", "0.1": "0.1", "1714600000.25": "1714600000.25", "3.14159": "3.14159",
		"2e-05": "2e-05", "100.0e0": "100.0", "-1.5e+300": "-1.5e+300", "0.0": "0.0",
	}
	for in, want := range cases {
		if got := FormatNumber(Number(in)); got != want {
			t.Errorf("%s: %s（期待 %s）", in, got, want)
		}
	}
}

func TestIndentGolden(t *testing.T) {
	v, err := Decode([]byte(`{"a":"\u003c&\u003e\u2028\u007f\u001f\n\t\"\\ é","b":[],"c":{},"d":[1,{"x":null,"y":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": \"<&>\u2028\u007f\\u001f\\n\\t\\\"\\\\ é\",\n  \"b\": [],\n  \"c\": {},\n  \"d\": [\n    1,\n    {\n" +
		"      \"x\": null,\n      \"y\": true\n    }\n  ]\n}"
	if got := Indent(v, 2); got != want {
		t.Errorf("Indent:\n%s\n期待:\n%s", got, want)
	}
	c, _ := Decode([]byte(`{"a":[1,2],"b":{"c":"d"}}`))
	if got := Compact(c); got != `{"a": [1, 2], "b": {"c": "d"}}` {
		t.Errorf("Compact: %s", got)
	}
}

func TestObjectKeepsOrder(t *testing.T) {
	o, err := DecodeObject([]byte(`{"z":1,"a":2,"m":{"y":1,"b":2},"z":3}`))
	if err != nil {
		t.Fatal(err)
	}
	// 重なったキーは最初の位置・後の値
	if got := Compact(o); got != `{"z": 3, "a": 2, "m": {"y": 1, "b": 2}}` {
		t.Errorf("順: %s", got)
	}
	o.Set("new", "x").Set("a", 9)
	o.Delete("z")
	if got := Compact(o); got != `{"a": 9, "m": {"y": 1, "b": 2}, "new": "x"}` {
		t.Errorf("Set / Delete: %s", got)
	}
	c := o.Clone()
	c.Object("m").Set("y", 5)
	if Compact(o.Object("m")) != `{"y": 1, "b": 2}` {
		t.Error("Clone が中身を共有している")
	}
}

func TestDecodeRejects(t *testing.T) {
	for _, s := range []string{`{"a":`, `{"a":1} x`, ``, `[1,]`} {
		if _, err := Decode([]byte(s)); err == nil {
			t.Errorf("%q を読めてしまう", s)
		}
	}
	if _, err := DecodeObject([]byte(`[1]`)); err == nil {
		t.Error("配列をオブジェクトとして読めてしまう")
	}
}

func TestTruthyStrInt(t *testing.T) {
	for v, want := range map[any]bool{nil: false, "": false, "x": true, Number("0"): false, Number("0.0"): false, Number("2"): true, false: false} {
		if Truthy(v) != want {
			t.Errorf("Truthy(%#v) = %v", v, !want)
		}
	}
	if Str(nil) != "None" || Str(true) != "True" || Str(Number("1.50")) != "1.5" || Str("あ") != "あ" {
		t.Error("Str")
	}
	if n, ok := Int(Number("3600.9")); !ok || n != 3600 {
		t.Errorf("Int(3600.9) = %d %v", n, ok)
	}
	if n, ok := Int("3600"); !ok || n != 3600 {
		t.Errorf("Int(\"3600\") = %d %v", n, ok)
	}
	if _, ok := Float("1"); ok {
		t.Error("文字列を数とみなした")
	}
}
