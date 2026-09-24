package clitest

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// 正規化の規則そのもののテスト。

func testNormalizer() *Normalizer {
	start := time.Date(2026, 9, 18, 3, 0, 0, 0, time.UTC) // JST 12:00
	return &Normalizer{
		Paths:    []string{"/private/var/folders/ab/T/TestX/001", "/var/folders/ab/T/TestX/001"},
		Repo:     "/Users/dev/src/looptrack",
		APIBase:  "http://127.0.0.1:54321/im",
		Hostname: "build-mac.local",
		Start:    start,
		End:      start.Add(3 * time.Second),
	}
}

func TestNormalizeText(t *testing.T) {
	n := testNormalizer()
	cases := []struct{ name, in, want string }{
		{"CRLF", "a\r\nb\r\n", "a\nb\n"},
		{"一時パス（symlink を解いた形）", "保存: /private/var/folders/ab/T/TestX/001/ws/out.xlsx", "保存: $TMP/ws/out.xlsx"},
		{"一時パス（解く前の形）", "場所: /var/folders/ab/T/TestX/001/ws", "場所: $TMP/ws"},
		{"一時ディレクトリの外は変えない", "/var/folders/ab/T/TestY/ws", "/var/folders/ab/T/TestY/ws"},
		{"リポジトリのパス", "/Users/dev/src/looptrack/deploy/deploy.sh", "$REPO/deploy/deploy.sh"},
		{"リポジトリへの相対パス", ".claude/skills/x -> ../../../../Users/dev/src/looptrack/skills/x", ".claude/skills/x -> $REPO_REL/skills/x"},
		{"偽 API の URL", "サーバ http://127.0.0.1:54321/im/api/v1", "サーバ $API/api/v1"},
		{"URL エンコードした偽 API", "resource=http%3A%2F%2F127.0.0.1%3A54321%2Fim%2Fapi%2Fv1", "resource=$API%2Fapi%2Fv1"},
		{"ほかの 127.0.0.1 のポート", "http://127.0.0.1:61234/callback", "http://127.0.0.1:$PORT/callback"},
		{"ほかのポート（エンコード）", "http%3A%2F%2F127.0.0.1%3A61234%2Fcallback", "http%3A%2F%2F127.0.0.1%3A$PORT%2Fcallback"},
		{"state", "&state=Zq0_x-9abcdefghijklmnop&scope=im", "&state=$STATE&scope=im"},
		{"短い state は乱数と見なさない", "state=open", "state=open"},
		{"PKCE", "code_challenge=abcdefghijklmnopqrstuvwxyz012345&code_verifier=ABCDEFGHIJKLMNOPQRSTUVWXYZ-_0123",
			"code_challenge=$PKCE_CHALLENGE&code_verifier=$PKCE_VERIFIER"},
		{"経過時間", "[1/2] ok echo ok（0.0 秒）\n結果: 2/2 成功（12.35 秒）", "[1/2] ok echo ok（$SEC 秒）\n結果: 2/2 成功（$SEC 秒）"},
		{"duration_ms", `"duration_ms": 1234,`, `"duration_ms": 0,`},
		{"接続できない理由（以前の CLI の文言）", "エラー: サーバに接続できません（$API）: Remote end closed connection without response",
			"エラー: サーバに接続できません（$API）: $REASON"},
		{"接続できない理由（JSON の中）", `"error": "サーバに接続できません（http://127.0.0.1:54321/im）: [Errno 61] Connection refused",`,
			`"error": "サーバに接続できません（$API）: $REASON",`},
		{"実行中の時刻（JST・分まで）", "生成日時: 2026-09-18 12:00", "生成日時: $NOW"},
		{"実行中の時刻（秒・小数）", "2026-09-18 12:01:30.123456", "$NOW"},
		{"実行中の時刻（UTC）", "2026-09-18T03:00:02.5Z", "$NOW"},
		{"実行中の時刻（時差つき）", "2026-09-18T12:00:01+09:00 と 2026-09-18T12:00:01+0900", "$NOW と $NOW"},
		{"前後 2 分の余裕", "2026-09-18 11:58 / 2026-09-18 12:02", "$NOW / $NOW"},
		{"範囲の外（フィクスチャの時刻）は変えない", "2026-09-18 11:55・2024-05-02 11:30・2026-09-18T03:10:00Z",
			"2026-09-18 11:55・2024-05-02 11:30・2026-09-18T03:10:00Z"},
		{"ホスト名は本文では変えない（JSON の host・client_name だけ）", "build-mac.local", "build-mac.local"},
	}
	for _, c := range cases {
		if got := n.Text(c.in); got != c.want {
			t.Errorf("%s:\n  in   %q\n  got  %q\n  want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestNormalizeTextWithoutRunWindow(t *testing.T) {
	n := &Normalizer{}
	if got := n.Text("2026-09-18 12:00"); got != "2026-09-18 12:00" {
		t.Errorf("Start が無いときは時刻を変えない: %q", got)
	}
}

func TestNormalizeJSON(t *testing.T) {
	n := testNormalizer()
	exp := n.Start.Unix() + 3600 + 1 // int(time.time()) + expires_in（1 秒ずれても 1 分単位に丸まる）
	in := `{"z":1,"a":{"host":"build-mac.local","client_name":"looptrack（build-mac.local）","workspace":"ws"},` +
		`"results":[{"duration_ms":987,"output_tail":"<ok> & done"}],"expires_at":` + strconv.FormatInt(exp, 10) +
		`,"files":{"kit.tar.gz":"deadbeef","looptrack":"cafe"},"state":"s","code_verifier":"v","code_challenge":"c",` +
		`"since":0.0,"count":10,"path":"/var/folders/ab/T/TestX/001/ws/x"}`
	got, ok := n.JSON([]byte(in))
	if !ok {
		t.Fatal("JSON として読めない")
	}
	want := `{
  "a": {
    "client_name": "looptrack（$HOST）",
    "host": "$HOST",
    "workspace": "ws"
  },
  "code_challenge": "$PKCE_CHALLENGE",
  "code_verifier": "$PKCE_VERIFIER",
  "count": 10,
  "expires_at": "$NOW+3600s",
  "files": {
    "kit.tar.gz": "$SHA256",
    "looptrack": "$SHA256"
  },
  "path": "$TMP/ws/x",
  "results": [
    {
      "duration_ms": 0,
      "output_tail": "<ok> & done"
    }
  ],
  "since": 0.0,
  "state": "$STATE",
  "z": 1
}
`
	if got != want {
		t.Errorf("JSON の正規化:\n%s\nwant:\n%s", got, want)
	}
	for _, bad := range []string{"", "{", `{"a":1} {"b":2}`, "not json"} {
		if _, ok := n.JSON([]byte(bad)); ok {
			t.Errorf("JSON でないものを JSON と見なした: %q", bad)
		}
	}
}

// キーの順が違うだけの JSON（挿入順・Go の構造体の順）は同じになる。
func TestNormalizeJSONKeyOrder(t *testing.T) {
	n := testNormalizer()
	a, _ := n.JSON([]byte(`{"title":"x","type":"bug","labels":["b","a"]}`))
	b, _ := n.JSON([]byte(`{"labels":["b","a"],"type":"bug","title":"x"}`))
	if a != b {
		t.Errorf("キーの順で違う:\n%s\n%s", a, b)
	}
	if !strings.Contains(a, "\"b\",\n    \"a\"") {
		t.Errorf("配列の順は変えない: %s", a)
	}
}

func TestNormalizeQuery(t *testing.T) {
	n := testNormalizer()
	cases := []struct{ in, want string }{
		{"", ""},
		{"project=demo&format=md", "format=md&project=demo"},
		{"sort=priority&status=In+Progress&label=%E7%94%BB%E9%9D%A2", "label=%E7%94%BB%E9%9D%A2&sort=priority&status=In+Progress"},
		{"b=2&a=1&b=1", "a=1&b=2&b=1"}, // 同じキーは現れた順
		{"state=abc&code_verifier=xyz&redirect_uri=http%3A%2F%2F127.0.0.1%3A61234%2Fcallback&resource=http%3A%2F%2F127.0.0.1%3A54321%2Fim%2Fapi%2Fv1",
			"code_verifier=$PKCE_VERIFIER&redirect_uri=http%3A%2F%2F127.0.0.1%3A$PORT%2Fcallback&resource=$API%2Fapi%2Fv1&state=$STATE"},
	}
	for _, c := range cases {
		if got := n.Query(c.in); got != c.want {
			t.Errorf("Query(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
