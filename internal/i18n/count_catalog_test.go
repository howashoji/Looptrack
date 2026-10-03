package i18n

import (
	"strings"
	"testing"
)

// 実際の対訳表の件数つきの文面を、件数 1 と 2 で英語と日本語に出して確かめる。
// 英語は 1 のときだけ単数。日本語は件数に関わらず同じ型（これまでの文面と同じ）。
func TestCatalogCountedTexts(t *testing.T) {
	bytes := func(n int) Msg { return MN("service.err.attach.bytes", n, "n", n) }
	for _, c := range []struct {
		name string
		lang Lang
		got  string
		want string
	}{
		{"未送信 1（en）", EN, TN(EN, "cli.summary.usage_send_failed", 1, "at", "t", "count", 2, "reason", "r", "spooled", 1),
			"The hook on this machine is failing to send token usage (last failure t, 2 in a row, r). The 1 unsent one is resent the next time a send succeeds"},
		{"未送信 2（en）", EN, TN(EN, "cli.summary.usage_send_failed", 2, "at", "t", "count", 2, "reason", "r", "spooled", 2),
			"The hook on this machine is failing to send token usage (last failure t, 2 in a row, r). The 2 unsent ones are resent the next time a send succeeds"},
		{"未送信 1（ja）", JA, TN(JA, "cli.summary.usage_send_failed", 1, "at", "t", "count", 2, "reason", "r", "spooled", 1),
			"この PC のフックがトークン情報を送れていません（最後の失敗 t・続けて 2 回・r）。未送信 1 件は、次に送れたときに再送します"},
		// 問題の 1 件が複数の添付をまとめていても、後半（書き出せなかった添付）は複数のまま
		{"書き出しの問題 1（en）", EN, TN(EN, "cmd.err.export_attachments_incomplete", 1, "count", 1),
			"1 problem exporting attachments (the NG line above). The Markdown and the manifest are written, and the attachments that could not be exported are listed in the manifest as missing"},
		{"鍵の長さ 1（en）", EN, TN(EN, "auth.err.secret_key_length", 1, "bytes", 1), "LOOPTRACK_SECRET_KEY must be 32 bytes (it is 1 byte)"},
		{"鍵の長さ 2（en）", EN, TN(EN, "auth.err.secret_key_length", 2, "bytes", 2), "LOOPTRACK_SECRET_KEY must be 32 bytes (it is 2 bytes)"},
		{"鍵の長さ 1（ja）", JA, TN(JA, "auth.err.secret_key_length", 1, "bytes", 1), "LOOPTRACK_SECRET_KEY は 32 バイトにしてください（現在 1 バイト）"},
		{"運用文書 1（en）", EN, TN(EN, "cmd.guide.set", 1, "slug", "p", "bytes", 1, "source", "f"), "Set: the operations document of p (1 byte, from f)"},
		{"運用文書 2（en）", EN, TN(EN, "cmd.guide.set", 2, "slug", "p", "bytes", 2, "source", "f"), "Set: the operations document of p (2 bytes, from f)"},
		{"大きさの違い 1（en）", EN, TN(EN, "transfer.verify.attach_size_differs", 1, "id", 3, "filename", "a", "size", 1, "want", 5, "file", "x"),
			"The file of attachment 3 (a) has a different size from the manifest (1 byte, the manifest says 5; not read): x"},
		{"大きさの違い 2（ja）", JA, TN(JA, "transfer.verify.attach_size_differs", 2, "id", 3, "filename", "a", "size", 2, "want", 5, "file", "x"),
			"添付 3（a）の本体の大きさが目録と違います（2 バイト・目録は 5 バイト。読みません）: x"},
		{"目録との一致 1（en）", EN, TN(EN, "cmd.verify_files.attachments", 1, "slug", "p", "ok", 1, "total", 1), "Attachments: p, 1 / 1 file matches the SHA-256 in the manifest"},
		{"目録との一致 2（en）", EN, TN(EN, "cmd.verify_files.attachments", 2, "slug", "p", "ok", 2, "total", 2), "Attachments: p, 2 / 2 files match the SHA-256 in the manifest"},
		{"往復 1（en）", EN, TN(EN, "cmd.verify_files.result", 1, "ok", 1, "total", 1), "1 / 1 file survives a round trip"},
		{"往復 2（en）", EN, TN(EN, "cmd.verify_files.result", 2, "ok", 2, "total", 2), "2 / 2 files survive a round trip"},
		{"往復 2（ja）", JA, TN(JA, "cmd.verify_files.result", 2, "ok", 2, "total", 2), "2 / 2 ファイルが往復一致"},
		{"容量の超過 1・1（en）", EN, T(EN, "service.err.attach.project_quota", "slug", "p", "limit", 1048576, "used", bytes(1), "size", bytes(1)),
			"Attachments in project p would exceed the limit (1048576 bytes; 1 byte in use, this file 1 byte)"},
		{"容量の超過 2・5（en）", EN, T(EN, "service.err.attach.project_quota", "slug", "p", "limit", 1048576, "used", bytes(2), "size", bytes(5)),
			"Attachments in project p would exceed the limit (1048576 bytes; 2 bytes in use, this file 5 bytes)"},
		{"容量の超過 1・5（ja）", JA, T(JA, "service.err.attach.project_quota", "slug", "p", "limit", 1048576, "used", bytes(1), "size", bytes(5)),
			"プロジェクト p の添付の合計が上限（1048576 バイト）を超えます（いまの合計 1 バイト・この添付 5 バイト）"},
		{"配布物 1・1（en）", EN, T(EN, "server.mcp.setup.text.dist_note", "url", "u",
			"files", MN("server.mcp.setup.text.dist_files", 1, "n", 1), "binaries", MN("server.mcp.setup.text.dist_binaries", 1, "n", 1)),
			"The SHA-256 hashes of 1 kit file and 1 looptrack executable"},
		{"配布物 2・3（en）", EN, T(EN, "server.mcp.setup.text.dist_note", "url", "u",
			"files", MN("server.mcp.setup.text.dist_files", 2, "n", 2), "binaries", MN("server.mcp.setup.text.dist_binaries", 3, "n", 3)),
			"The SHA-256 hashes of 2 kit files and 3 looptrack executables"},
		{"配布物 1・3（ja）", JA, T(JA, "server.mcp.setup.text.dist_note", "url", "u",
			"files", MN("server.mcp.setup.text.dist_files", 1, "n", 1), "binaries", MN("server.mcp.setup.text.dist_binaries", 3, "n", 3)),
			"kit 1 件と looptrack の実行ファイル 3 件の SHA-256 は"},
	} {
		// 配布物の文は長いので、件数を埋めた前半だけを見る
		if strings.HasPrefix(c.name, "配布物") && strings.HasPrefix(c.got, c.want) {
			continue
		}
		if c.got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, c.got, c.want)
		}
	}
}
