package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// 合成の秘密（どれも明らかに偽の値。メッセージには値を出さず、名前と番号だけを出す）。
var maskCountValues = []struct{ name, in string }{
	{"token", "export TOKEN=FAKE-not-a-real-value-01 && run"},
	{"Bearer", "Authorization: Bearer FAKE.not.a.real.value.02"},
	{"password", "password: FAKE-not-a-real-value-03"},
	{"api_key", "set api_key=FAKE-not-a-real-value-04 now"},
	{"PRIVATE KEY", "head\n-----BEGIN RSA PRIVATE KEY-----\nFAKENOTAREALKEYBODY05\n-----END RSA PRIVATE KEY-----\ntail"},
}

// (a) MaskSecretsCount の文字列は MaskSecrets と同じ。(b) マスクした後は FindSecret に当たらない（対照: 前は当たる）。
func TestMaskSecretsCountMatchesMaskSecrets(t *testing.T) {
	for _, v := range maskCountValues {
		got, counts := MaskSecretsCount(v.in)
		if want := MaskSecrets(v.in); got != want {
			t.Errorf("%s: MaskSecretsCount の文字列が MaskSecrets と違う", v.name)
		}
		total := 0
		for label, n := range counts {
			total += n
			if strings.Contains(label, "FAKE") {
				t.Errorf("%s: 件数のキーに値が入った", v.name)
			}
		}
		if total < 1 {
			t.Errorf("%s: 当たった数が 0（前提が崩れています: 合成の値がマスクの規則に当たっていません）", v.name)
		}
		// 規則の語は FindSecret の Label と同じ
		if f, ok := FindSecret(v.in); ok && counts[f.Label] < 1 {
			t.Errorf("%s: FindSecret の Label %q が件数に無い（件数のキー: %v）", v.name, f.Label, keysOf(counts))
		}
	}
	// 複数の当たりは語ごとに数える。マスク済みの形は数えない
	_, counts := MaskSecretsCount("token=aaa1 token=bbb2 password: ccc3 Bearer ddd4 token=***")
	want := map[string]int{"token=…": 2, "password: …": 1, "Bearer …": 1}
	if fmt.Sprint(counts) != fmt.Sprint(want) {
		t.Errorf("語ごとの件数 = %v, want %v", counts, want)
	}
	if got, c := MaskSecretsCount("何も無い行"); got != "何も無い行" || len(c) != 0 {
		t.Errorf("当たらない文字列が変わった: %q %v", got, c)
	}
	// 何度かけても同じ（2 回目は数えない）
	once, _ := MaskSecretsCount(maskCountValues[0].in)
	if twice, c := MaskSecretsCount(once); twice != once || len(c) != 0 {
		t.Errorf("2 回目で変わった/数えた: %v", c)
	}
}

func TestMaskedValuesDoNotMatchFindSecret(t *testing.T) {
	for i, v := range maskCountValues {
		if _, ok := FindSecret(v.in); !ok {
			t.Fatalf("前提が崩れています: マスクする前の合成の値 %d (%s) に FindSecret が当たりません（誤検知の確認が通ったことにはなりません）", i, v.name)
		}
		masked, _ := MaskSecretsCount(v.in)
		if f, ok := FindSecret(masked); ok {
			t.Errorf("マスクした後も FindSecret が当たった: %d (%s) 行 %d 規則 %q", i, v.name, f.Line, f.Label)
		}
	}
}

// maskJSONStrings は JSON の文字列の値（キーは除く）ごとに MaskSecretsCount をかける。
func maskJSONStrings(v any) (any, int) {
	switch x := v.(type) {
	case string:
		s, c := MaskSecretsCount(x)
		n := 0
		for _, k := range c {
			n += k
		}
		return s, n
	case []any:
		n := 0
		for i := range x {
			var k int
			x[i], k = maskJSONStrings(x[i])
			n += k
		}
		return x, n
	case *jsonorder.Object:
		n := 0
		for i := range x.Members {
			var k int
			x.Members[i].Value, k = maskJSONStrings(x.Members[i].Value)
			n += k
		}
		return x, n
	}
	return v, 0
}

// (c) 値ごとにマスクして書き直した行は JSON のまま。生の行に MaskSecrets をかけた対照は壊れる。
func TestMaskJSONLPerValueKeepsValid(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"run with token=FAKE-not-real-a1"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Authorization: Bearer FAKE.not.real.a2"}]}}`,
		`{"type":"tool","input":{"command":"curl -H 'x' password: FAKE-not-real-a3"},"n":1}`,
		`{"type":"tool","output":"api_key=FAKE-not-real-a4","ok":true}`,
		`{"type":"tool","output":"-----BEGIN PRIVATE KEY-----\nFAKENOTREALA5\n-----END PRIVATE KEY-----\n","n":2}`,
		`{"type":"user","message":{"content":"nothing secret here, 日本語もあります"}}`,
		`{"a":["secret: FAKE-not-real-a7","plain"],"b":null,"c":1.50}`,
		`{"type":"assistant","text":"imp_FAKEnotreal8 を使う","k":false}`,
		`{"type":"user","text":"TOKEN: FAKE-not-real-a9"}`,
		`{"type":"system","text":"sk-FAKEnotrealFAKEnotreal10 と AKIAFAKENOTREAL00010"}`,
	}
	if len(lines) != 10 {
		t.Fatalf("前提が崩れています: 合成の行が %d 行（10 行のはず）", len(lines))
	}
	valid, controlInvalid, masked := 0, 0, 0
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("前提が崩れています: 合成の行 %d がそもそも JSON ではありません", i)
		}
		v, err := jsonorder.Decode([]byte(line))
		if err != nil {
			t.Fatalf("行 %d を読めない: %v", i, err)
		}
		v, n := maskJSONStrings(v)
		masked += n
		out := jsonorder.Marshal(v)
		if json.Valid(out) {
			valid++
		} else {
			t.Errorf("値ごとにマスクして書き直した行 %d が JSON でない", i)
		}
		if strings.Contains(string(out), "FAKE") {
			t.Errorf("行 %d に合成の値が残った", i)
		}
		// 検査も値ごと（引用符・区切りを含む JSON の行そのものに FindSecret をかけると、マスク済みでも \S+ が区切りを食って当たる）
		if k := findSecretInStrings(v); k > 0 {
			t.Errorf("行 %d: マスクした文字列の値 %d 個に FindSecret が当たる", i, k)
		}
		if !json.Valid([]byte(MaskSecrets(line))) {
			controlInvalid++
		}
	}
	if valid != 10 {
		t.Errorf("json.Valid は %d/10", valid)
	}
	if masked < 9 {
		t.Errorf("前提が崩れています: 値ごとのマスクが当たった数が %d（9 以上のはず）", masked)
	}
	if controlInvalid < 1 {
		t.Errorf("前提が崩れています: 生の行に MaskSecrets をかけても JSON が壊れませんでした（対照が効いていません）")
	}
	t.Logf("値ごと: json.Valid %d/10・マスク %d 件 / 対照（生の行）: 壊れた行 %d/10", valid, masked, controlInvalid)
}

// findSecretInStrings は JSON の文字列の値のうち FindSecret に当たるものの数。
func findSecretInStrings(v any) int {
	switch x := v.(type) {
	case string:
		if _, ok := FindSecret(x); ok {
			return 1
		}
	case []any:
		n := 0
		for _, e := range x {
			n += findSecretInStrings(e)
		}
		return n
	case *jsonorder.Object:
		n := 0
		for _, m := range x.Members {
			n += findSecretInStrings(m.Value)
		}
		return n
	}
	return 0
}

func keysOf(m map[string]int) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
