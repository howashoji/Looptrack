package server

import (
	"encoding/json"
	"testing"
)

// canonJSON は DB から読んだ JSON の文字列を、キーの順・空白によらない形（Go の json.Marshal の形）にそろえる。
// MySQL は JSON 列を正規化して返し（キーの並べ替え・「": "」の空白）、SQLite は書いたままの文字列を返すため。
func canonJSON(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("JSON として読めません: %v: %s", err, s)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
