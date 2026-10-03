// render.js の検査（node --test internal/server/static/render_test.mjs）。
// 「" や javascript: を含む値で XSS が起きない」ことを確かめる。
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { esc, attr, safeURL, makeRenderer } from "./render.js";

const r = makeRenderer("REQ");

// 画面の文面。本番はサーバが <script type="application/json" id="i18n"> に入れて返す
// （internal/server/templates/board.html）。ここでは日本語の文面を同じ形で渡し、
// 出来上がる HTML が以前と同じであることを確かめる。
const JA = {
  assignee_inactive: "（権限なし）",
  unassigned: "未設定",
  assign_self: "自分（{login}）",
  assign_member: "{name}（{login}）",
  row_assignee: "担当",
  assign_reason_placeholder: "引き継ぐ理由（他の人の担当を替えるとき必須）",
  assign_reason: "引き継ぐ理由",
  assign_submit: "担当を変更",
  feedback_title: "未応答のフィードバック",
  feedback_count: "反応 {n}",
  err_forbidden: "この操作の権限がありません（閲覧のみ）",
  err_failed: "失敗しました（HTTP {status}）",
  override: "上書きの理由（ルールを理由付きで通す。理由は記録に残る）",
  form_title: "題名",
  form_type: "型",
  form_priority: "優先度",
  form_content: "本文（「## 内容」節に入る）",
  form_criteria: "受け入れ条件（1 行に 1 つ。テスト可能な形で書く）",
  form_submit: "起票する",
  status_label: "変更後の状態",
  status_comment_placeholder: "コメント（任意。クローズでは検証結果）",
  status_comment: "状態の変更に添えるコメント",
  status_submit: "状態を変更",
  comment_label: "コメントを追記（追記のみ。後から書き換えられない）",
  comment_submit: "コメントを追記",
  body_empty: "（未記入）",
  body_acceptance: "## 受け入れ条件",
  comments_heading: "コメント",
  att_none: "添付はまだ無い。",
  att_purged: "消去済み",
  att_pick: "ファイルを選ぶ",
  att_hint: "ここへドラッグ&ドロップしても、本文の欄に画像を貼り付けても添付できます",
  att_remove: "{name} を外す",
  att_sent: "送信済み（次の送信でコメントに付く）",
  att_sending: "{name} を送信中（{n}/{total}）",
  att_upload_failed: "{name} を添付できなかった。{reason}",
  att_empty: "本文か添付のどちらかを入れてください。何も送っていない。"
};

test("esc は引用符も落とす（属性値を閉じられない）", () => {
  assert.equal(esc('"><img src=x onerror=alert(1)>'), "&quot;&gt;&lt;img src=x onerror=alert(1)&gt;");
  assert.equal(esc("' onmouseover='alert(1)"), "&#39; onmouseover=&#39;alert(1)");
  assert.equal(esc("a & b"), "a &amp; b");
  assert.equal(esc(null), "");
  assert.equal(attr, esc);
});

test("safeURL は http/https/mailto と同一ページ内・相対パスだけを通す", () => {
  for (const ok of ["https://example.com/a?b=1", "http://example.com", "mailto:a@example.com", "#REQ-0001", "/im/p/x/", "docs/a.md"]) {
    assert.equal(safeURL(ok), ok);
  }
  for (const ng of ["javascript:alert(1)", " javascript:alert(1)", "JaVaScRiPt:alert(1)", "data:text/html,<script>", "vbscript:x", "//evil.example/x"]) {
    assert.equal(safeURL(ng), "#", ng);
  }
});

test("Markdown のリンクは危険なスキームを落とす", () => {
  const html = r.md("[押して](javascript:alert(1)) と [外部](https://example.com/x)");
  assert.match(html, /<a href="#" target="_blank" rel="noreferrer noopener">押して<\/a>/);
  assert.match(html, /<a href="https:\/\/example.com\/x"/);
  assert.doesNotMatch(html, /javascript:/);
});

test("本文の HTML はそのまま出さない", () => {
  const html = r.md('<img src=x onerror=alert(1)>\n\n<script>alert(1)</script>');
  assert.doesNotMatch(html, /<img|<script/);
  assert.match(html, /&lt;img src=x onerror=alert\(1\)&gt;/);
});

test("コードブロック・インラインコードもエスケープする", () => {
  assert.match(r.md("```\n<script>alert(1)</script>\n```"), /<pre><code>&lt;script&gt;/);
  assert.match(r.md("`<img src=x>`"), /<code>&lt;img src=x&gt;<\/code>/);
});

test("表・箇条書き・チェックボックス・見出し・引用が現行と同じ形で出る", () => {
  assert.match(r.md("# 見出し"), /^<h1>見出し<\/h1>$/);
  assert.match(r.md("| a | b |\n| -- | -- |\n| 1 | 2 |"), /<table><thead><tr><th>a<\/th><th>b<\/th><\/tr><\/thead><tbody><tr><td>1<\/td><td>2<\/td><\/tr><\/tbody><\/table>/);
  assert.match(r.md("- [ ] やること\n- [x] 済み"), /<li class="task">☐ やること<\/li><li class="task">☑ 済み<\/li>/);
  assert.match(r.md("1. 一\n2. 二"), /<ol><li>一<\/li><li>二<\/li><\/ol>/);
  assert.match(r.md("> 引用"), /<blockquote><p>引用<\/p><\/blockquote>/);
  assert.match(r.md("**強調** と *斜体*"), /<strong>強調<\/strong> と <em>斜体<\/em>/);
});

test("イシュー ID と文書 ID に印を付ける（ID は接頭辞 + 数字だけ）", () => {
  const html = r.md("REQ-0123 と FR-001 を見る");
  assert.match(html, /<a class="ilink" href="#REQ-0123" data-issue="REQ-0123">REQ-0123<\/a>/);
  assert.match(html, /<span class="docid">FR-001<\/span>/);
  assert.equal(r.isIssueID("REQ-0001"), true);
  assert.equal(r.isIssueID("FR-001"), false);
});

test("接頭辞に正規表現のメタ文字があっても壊れない", () => {
  const h = makeRenderer("APP-DEV");
  assert.match(h.md("APP-DEV-0001 の件"), /data-issue="APP-DEV-0001"/);
  assert.doesNotThrow(() => makeRenderer("A.*B").md("x"));
});

// 担当の表記・絞り込み・変更フォーム
import { assigneeLabel, matchAssignee, assignForm } from "./render.js";

test("担当の表記と絞り込み", () => {
  assert.equal(assigneeLabel({}, JA), "");
  assert.equal(assigneeLabel({ assignee: "alice" }, JA), "alice");
  assert.equal(assigneeLabel({ assignee: "bob", assignee_inactive: true }, JA), "bob（権限なし）");
  const mine = { assignee: "alice" }, none = {}, other = { assignee: "bob" };
  assert.deepEqual([mine, none, other].map(i => matchAssignee(i, "", "alice")), [true, true, true]);
  assert.deepEqual([mine, none, other].map(i => matchAssignee(i, "me", "alice")), [true, false, false]);
  assert.deepEqual([mine, none, other].map(i => matchAssignee(i, "-", "alice")), [false, true, false]);
  assert.deepEqual([mine, none, other].map(i => matchAssignee(i, "bob", "alice")), [false, false, true]);
});

test("担当変更フォームは CSRF 付きの通常の POST で、値をエスケープする", () => {
  const html = assignForm({ base: "/im", slug: "im", csrf: 'c"sr<f', canEdit: true, closed: false, me: "alice",
    it: { id: "IM-0001", assignee: "bob" }, members: [{ login: "bob", name: "<b>ボブ" }, { login: "x\"y", name: "" }], t: JA });
  assert.match(html, /<form class="assign" method="post" action="\/im\/p\/im\/issues\/IM-0001\/assign">/);
  assert.match(html, /name="csrf" value="c&quot;sr&lt;f"/);
  assert.match(html, /<option value="bob" selected>&lt;b&gt;ボブ（bob）<\/option>/);
  assert.match(html, /value="x&quot;y"/);
  assert.match(html, /name="override_reason"/);   // 他人の担当を替えるときは理由の欄
  assert.doesNotMatch(html, /<b>/);
  assert.equal(assignForm({ canEdit: false, it: { id: "IM-0001" } }), "");   // viewer には出さない
  assert.equal(assignForm({ canEdit: true, closed: true, it: { id: "IM-0001" } }), "");
  const self = assignForm({ base: "/im", slug: "im", csrf: "t", canEdit: true, me: "alice", it: { id: "IM-0002", assignee: "alice" }, members: [], t: JA });
  assert.doesNotMatch(self, /override_reason/);
  assert.match(self, /<option value="alice" selected>自分（alice）<\/option>/);
});

// 外からの反応（未応答のフィードバック）の印と絞り込み「未応答の反応」
import { feedbackPending, feedbackTag, matchFeedback } from "./render.js";

test("カードの「反応 N」は未応答があるときだけ", () => {
  assert.equal(feedbackTag({}, JA), "");
  assert.equal(feedbackTag({ feedback_pending: 0 }, JA), "");
  assert.equal(feedbackTag({ feedback_pending: 2 }, JA), '<span class="tag feedback" title="未応答のフィードバック">反応 2</span>');
  assert.equal(feedbackTag({ feedback_pending: '"><script>' }, JA), "");   // 数でない値は出さない（埋め込まない）
  assert.equal(feedbackPending({ feedback_pending: "3" }), 3);
});

test("絞り込み「未応答の反応」は list --has-feedback と同じ集合（クローズ済みも含む）", () => {
  // サーバの board の各項目（feedback_pending は GET …/issues?has_feedback=1 と同じ SQL の件数）
  const board = [
    { id: "IM-0001", status: "Todo", feedback_pending: 1 },
    { id: "IM-0002", status: "Todo", feedback_pending: 0 },
    { id: "IM-0003", status: "Done", feedback_pending: 2 },
    { id: "IM-0004", status: "Canceled" },
    { id: "IM-0005", status: "In Review", feedback_pending: 1 },
  ];
  // has_feedback=1 の一覧が返す集合（既定でクローズ済みも含む）
  const hasFeedback = ["IM-0001", "IM-0003", "IM-0005"];
  assert.deepEqual(board.filter(it => matchFeedback(it, true)).map(it => it.id), hasFeedback);
  assert.equal(board.filter(it => matchFeedback(it, false)).length, board.length);   // 切っていればすべて
});

// 画面からの起票・状態の変更・コメント
import { buildIssueBody, apiErrorMessage, newIssueForm, statusForm, commentForm, formRequest } from "./render.js";

test("起票の本文は CLI の new と同じ節構成になる（受け入れ条件は「## 受け入れ条件」の節に）", () => {
  assert.equal(buildIssueBody("", "", JA), "");
  assert.equal(buildIssueBody("  内容だけ \n", "", JA), "内容だけ");
  assert.equal(buildIssueBody("内容", "一つ目\n\n- 二つ目\r\n- [x] 済み\n* [ ] 星", JA),
    "内容\n\n## 受け入れ条件\n\n- [ ] 一つ目\n- [ ] 二つ目\n- [x] 済み\n* [ ] 星");
  // 本文が空でも受け入れ条件があれば「## 内容」は（未記入）のまま
  assert.equal(buildIssueBody("", "条件", JA), "（未記入）\n\n## 受け入れ条件\n\n- [ ] 条件");
});

// 見出しと「未記入」は、サーバの雛形（domain.template.*）と同じ語を作成者の言語で書く。
// 画面の文面（board.html が i18n の JSON に入れる値）と同じものを対訳表から読み、日英の両方を確かめる。
const catalog = lang => JSON.parse(readFileSync(new URL("../../i18n/" + lang + ".json", import.meta.url), "utf8"));
const bodyTexts = lang => {
  const c = catalog(lang);
  return { body_empty: c["domain.template.empty"], body_acceptance: c["domain.template.acceptance_heading"] };
};

test("起票の本文の見出しは作成者の言語になる（英語の利用者の本文に日本語の見出しが混ざらない）", () => {
  const en = bodyTexts("en"), ja = bodyTexts("ja");
  assert.equal(en.body_acceptance, "## Acceptance criteria");
  assert.equal(ja.body_acceptance, "## 受け入れ条件");
  assert.deepEqual(ja, { body_empty: JA.body_empty, body_acceptance: JA.body_acceptance });   // 上の JA と対訳表が揃っている
  const enBody = buildIssueBody("", "works\n- [ ] tested", en);
  assert.equal(enBody, "(not written yet)\n\n## Acceptance criteria\n\n- [ ] works\n- [ ] tested");
  assert.doesNotMatch(enBody, /[\u3040-\u30ff\u4e00-\u9fff]/);   // 日本語の文字が 1 つも無い
  assert.equal(buildIssueBody("", "works", ja), "（未記入）\n\n## 受け入れ条件\n\n- [ ] works");
  // 雛形の受け入れ条件節（domain.template.acceptance）の先頭の行と同じ見出し
  for (const lang of ["ja", "en"]) {
    assert.equal(catalog(lang)["domain.template.acceptance"].split("\n")[0], bodyTexts(lang).body_acceptance);
  }
  // formRequest も画面の文面を渡せば同じ見出しで要求を作る
  assert.equal(formRequest("create", undefined, "im", { title: "t", type: "task", priority: "P2", content: "c", criteria: "works" }, en).body.body,
    "c\n\n## Acceptance criteria\n\n- [ ] works");
  // 文面が読めなかったとき（t が空）は英語の見出しに倒す（サーバはどちらの言語の見出しも受ける）
  assert.equal(buildIssueBody("", "works"), "(not written yet)\n\n## Acceptance criteria\n\n- [ ] works");
});

test("フォームは can_edit のときだけ出る（viewer には出さない）", () => {
  const it = { id: "IM-0001", status: "Todo" };
  for (const f of [newIssueForm({ canEdit: false, types: ["task"], priorities: ["P2"], t: JA }),
    statusForm({ canEdit: false, it, statuses: ["Todo"], t: JA }), commentForm({ canEdit: false, it, t: JA })]) {
    assert.equal(f, "");
  }
  assert.equal(newIssueForm(), "");
  const nf = newIssueForm({ canEdit: true, types: ["requirement", "task"], priorities: ["P0", "P2"], typeLabel: x => ({ task: "作業" })[x] || x, t: JA });
  assert.match(nf, /<form class="issue-form" id="newIssueForm" data-action="create">/);
  for (const name of ["title", "type", "priority", "content", "criteria", "override_reason"]) assert.match(nf, new RegExp('name="' + name + '"'));
  assert.match(nf, /<option value="task" selected>作業<\/option>/);   // 既定は CLI の new と同じ task / P2
  assert.match(nf, /<option value="P2" selected>P2<\/option>/);
  const sf = statusForm({ canEdit: true, it, statuses: ["Todo", "Done"], t: JA });
  assert.match(sf, /data-action="status" data-id="IM-0001"/);
  assert.match(sf, /<option value="Todo" selected>Todo<\/option><option value="Done">Done<\/option>/);
  assert.match(commentForm({ canEdit: true, it, t: JA }), /data-action="comment" data-id="IM-0001"/);
});

test("状態の変更フォームは、応答が載せた注意をフォームの下に出す（無ければ出さない）", () => {
  const it = { id: "IM-0001", status: "In Progress" };
  const without = statusForm({ canEdit: true, it, statuses: ["Todo", "In Progress"], t: JA });
  assert.doesNotMatch(without, /form-notice/);   // 対照: 注意が無ければ枠も出さない
  const note = "注意: IM-0001 の「## 受け入れ条件」が起票の雛形のままです <b>";
  const html = statusForm({ canEdit: true, it, statuses: ["Todo", "In Progress"], t: JA, notice: note });
  assert.match(html, /<p class="form-notice" role="status">注意: IM-0001 の「## 受け入れ条件」が起票の雛形のままです &lt;b&gt;<\/p><\/form>$/);
});

test("フォームの値はエスケープする", () => {
  const html = statusForm({ canEdit: true, it: { id: 'X"><script>', status: "<b>" }, statuses: ["<b>"], t: JA })
    + newIssueForm({ canEdit: true, types: ['"><img>'], priorities: [], t: JA });
  assert.doesNotMatch(html, /<script>|<img>|<b>/);
});

test("フォームの値から既存の API の要求を作る", () => {
  assert.deepEqual(formRequest("create", undefined, "my proj", { title: " 題 ", type: "bug", priority: "P1", content: "内容", criteria: "条件", override_reason: "" }, JA),
    { path: "/api/v1/projects/my%20proj/issues", body: { title: "題", type: "bug", priority: "P1", body: "内容\n\n## 受け入れ条件\n\n- [ ] 条件" } });
  assert.deepEqual(formRequest("status", "IM-0001", "im", { status: "Done", comment: " 検証した ", override_reason: "急ぎ" }),
    { path: "/api/v1/issues/IM-0001/status", body: { status: "Done", comment: "検証した", override_reason: "急ぎ" } });
  assert.deepEqual(formRequest("status", "IM-0001", "im", { status: "In Progress", comment: "" }),
    { path: "/api/v1/issues/IM-0001/status", body: { status: "In Progress" } });
  assert.deepEqual(formRequest("comment", "IM-0001", "im", { text: "メモ\n" }), { path: "/api/v1/issues/IM-0001/comments", body: { text: "メモ" } });
  assert.equal(formRequest("delete", "IM-0001", "im", {}), null);
});

test("失敗はサーバの文言をそのまま出す（ルールの拒否を握りつぶさない）", () => {
  assert.equal(apiErrorMessage(422, { error: { code: "rule_violation", message: "Done にするには ## 検証 の節が必要です" } }, JA),
    "Done にするには ## 検証 の節が必要です");
  assert.equal(apiErrorMessage(403, null, JA), "この操作の権限がありません（閲覧のみ）");
  assert.equal(apiErrorMessage(502, null, JA), "失敗しました（HTTP 502）");
});

// 英語の文面は対訳表（internal/i18n/en.json）から同じキーで組む。
// 対訳表から引くのは、テストの中に英訳を写すと本物とずれても気づけないため。
// 起票の本文の見出しと「未記入」は、サーバの雛形と同じ ID を board.html が渡す（server.web.board.* ではない）。
const TEXT_ID = { body_empty: "domain.template.empty", body_acceptance: "domain.template.acceptance_heading" };
const EN = Object.fromEntries(Object.keys(JA).map(k => {
  const id = TEXT_ID[k] || "server.web.board." + k;
  const v = JSON.parse(readFileSync(new URL("../../i18n/en.json", import.meta.url), "utf8"))[id];
  if (typeof v !== "string") throw new Error(id + " が en.json にありません");
  return [k, v];
}));

const HAS_JA = /[\u3040-\u30ff\u3400-\u4dbf\u4e00-\u9fff]/;

test("en の文面で描くと、画面に日本語が残らない", () => {
  const it = { id: "REQ-0001", assignee: "bob", assignee_inactive: true, feedback_pending: 2, status: "Todo" };
  const html = [
    assigneeLabel(it, EN),
    assignForm({ base: "/im", slug: "im", csrf: "t", canEdit: true, me: "alice", it, members: [{ login: "bob", name: "Bob" }], t: EN }),
    feedbackTag(it, EN),
    newIssueForm({ canEdit: true, types: ["task"], priorities: ["P2"], t: EN }),
    statusForm({ canEdit: true, it, statuses: ["Todo", "Done"], t: EN }),
    commentForm({ canEdit: true, it, t: EN, pending: [{ name: "a.png", size: 10 }, { name: "b.log", size: 2048, sent: true }] }),
    attachmentList([], EN),
    attachmentList([{ id: 1, filename: "x.png", purged: true }, { id: 2, filename: "y.txt", media_type: "text/plain", size: 3, url: "/im/api/v1/attachments/2" }], EN),
    apiErrorMessage(403, null, EN),
    apiErrorMessage(502, null, EN),
    buildIssueBody("", "works", EN)
  ].join("\n");
  assert.doesNotMatch(html, HAS_JA, "英語の辞書で描いた出力に日本語が残っています:\n" + html);
  // 辞書を引けているか（空の辞書でも「日本語が無い」は成立してしまうため）
  assert.match(html, /Unassign|Reassign|assignee|Body|Comment|Status/i);
});

/* ---------------- 添付（コメントのフォームとドロワーの一覧） ---------------- */
import { attachmentList, showInline, pendingList, uploadRequest, pastedName, formatSize, sendComment } from "./render.js";

const att = (over) => Object.assign({ id: 1, filename: "f", media_type: "application/octet-stream", size: 10, purged: false,
  url: "/im/api/v1/attachments/1", inline: false }, over);

test("ドロワーの添付: サーバが inline にした png は img、SVG はどの経路でも img にしない", () => {
  // 対照: 中身も png（サーバの inline が true）なら、その場で表示する
  const png = attachmentList([att({ id: 7, filename: "shot.png", media_type: "image/png", inline: true, url: "/im/api/v1/attachments/7" })], JA);
  assert.match(png, /<img class="att-img" src="\/im\/api\/v1\/attachments\/7" alt="shot.png"/);
  // SVG: サーバが attachment で返す（inline: false）ので img にしない。ダウンロードのリンクになる
  const svg = attachmentList([att({ id: 8, filename: "evil.svg", media_type: "image/svg+xml", url: "/im/api/v1/attachments/8" })], JA);
  assert.doesNotMatch(svg, /<img/);
  assert.match(svg, /<a href="\/im\/api\/v1\/attachments\/8" download="evil.svg">evil.svg<\/a>/);
  // サーバが誤って inline と言っても、SVG だけは img にしない（大文字・引数つきの申告も）
  for (const mt of ["image/svg+xml", "IMAGE/SVG+XML", " image/svg+xml; charset=utf-8"]) {
    assert.equal(showInline(att({ media_type: mt, inline: true })), false, mt);
    assert.doesNotMatch(attachmentList([att({ media_type: mt, inline: true })], JA), /<img/, mt);
  }
  // 判定はサーバの inline に従い、形式で作り直さない: png を名乗っても inline でなければ img にしない
  assert.equal(showInline(att({ media_type: "image/png", inline: false })), false);
  assert.equal(showInline(att({ media_type: "image/png", inline: "true" })), false);   // 真偽値の true だけ
  assert.equal(showInline(att({ media_type: "image/png", inline: true })), true);
});

test("ドロワーの添付: 消去済みは名前と「消去済み」だけ、空なら空と出す", () => {
  const html = attachmentList([att({ id: 3, filename: "secret.png", media_type: "image/png", inline: true, purged: true })], JA);
  assert.match(html, /<span class="att-name">secret.png<\/span> <span class="tag">消去済み<\/span>/);
  assert.doesNotMatch(html, /<img|<a |href=/);
  assert.match(attachmentList([], JA), /添付はまだ無い。/);
  assert.match(attachmentList(null, JA), /添付はまだ無い。/);
});

test("ドロワーの添付の値はエスケープし、危険なリンク先は通さない", () => {
  const html = attachmentList([att({ filename: '"><script>x</script>', media_type: "<b>", url: "javascript:alert(1)" }),
    att({ id: 2, filename: '"><img onerror=x>', inline: true, media_type: "image/png", url: "/im/x" })], JA);
  assert.doesNotMatch(html, /<script>|<b>|javascript:|<img onerror/);
  assert.match(html, /href="#"/);
});

test("viewer にはフォームの添付の操作が出ない（editor には出る）", () => {
  const it = { id: "IM-0001", status: "Todo" };
  assert.equal(commentForm({ canEdit: false, it, t: JA, pending: [{ name: "a.png", size: 1 }] }), "");
  // 対照: editor には選択の部品・ドロップの場所・送る前の一覧が出る
  const html = commentForm({ canEdit: true, it, t: JA, pending: [{ name: "a.png", size: 1 }] });
  assert.match(html, /<input type="file" multiple data-attach-input>/);
  assert.match(html, /data-attach-drop/);
  assert.match(html, /<ul class="att-pending" data-attach-pending><li class="att-item"><span class="att-name">a.png<\/span>/);
  assert.doesNotMatch(html, /<textarea name="text" rows="3" required>/);   // 本文が空でも添付だけを送れる
});

test("送る前の一覧: 送信済みは外せず、名前はエスケープする", () => {
  const html = pendingList([{ name: '"><b>', size: 1536 }, { name: "done.log", size: 1, sent: true }], JA);
  assert.doesNotMatch(html, /<b>/);
  assert.match(html, /1.5 KiB/);
  assert.match(html, /data-attach-remove="0"/);
  assert.doesNotMatch(html, /data-attach-remove="1"/);
  assert.match(html, /送信済み（次の送信でコメントに付く）/);
  assert.equal(pendingList(undefined, JA), "");
});

test("添付の要求: 本文はファイル、名前は符号化して X-Looptrack-Filename、画面の CSRF を付ける", () => {
  const file = { name: "画面 1.png", type: "image/png" };
  const req = uploadRequest("/im", "IM-0001", "my proj", file, "tok");
  assert.equal(req.url, "/im/api/v1/issues/IM-0001/attachments?project=my%20proj");
  assert.equal(req.init.method, "POST");
  assert.equal(req.init.credentials, "same-origin");
  assert.equal(req.init.body, file);
  assert.deepEqual(req.init.headers, { "Content-Type": "image/png", "X-Looptrack-Filename": "%E7%94%BB%E9%9D%A2%201.png", "X-CSRF-Token": "tok" });
  // 形式が分からないファイルは octet-stream で送る（形式の判定はサーバがする）
  assert.equal(uploadRequest("/im", "IM-0001", "p", { name: "x" }, "").init.headers["Content-Type"], "application/octet-stream");
  // コメントには返った ID を付ける。付けないときは本文だけ（これまでと同じ形）
  assert.deepEqual(formRequest("comment", "IM-0001", "im", { text: "記録", attachments: [3, 4] }),
    { path: "/api/v1/issues/IM-0001/comments", body: { text: "記録", attachments: [3, 4] } });
  assert.deepEqual(formRequest("comment", "IM-0001", "im", { text: "記録", attachments: [] }),
    { path: "/api/v1/issues/IM-0001/comments", body: { text: "記録" } });
});

test("貼り付けた画像の名前: 汎用の名前だけを日時の名前に替える", () => {
  const at = new Date(2026, 9, 2, 13, 5, 9);
  assert.equal(pastedName("image.png", "image/png", at), "paste-20261002-130509.png");
  assert.equal(pastedName("", "image/jpeg", at), "paste-20261002-130509.jpg");
  assert.equal(pastedName("design.png", "image/png", at), "design.png");
  assert.equal(formatSize(20 << 20), "20 MiB");
  assert.equal(formatSize(512), "512 B");
});

// fakeFetch は送った要求を数え、添付には ID を、コメントには 201 を返す。
function fakeFetch() {
  const calls = [];
  let next = 40;
  const f = async (url, init) => {
    calls.push({ url, init });
    const body = /\/attachments\?/.test(url) ? { attachment: { id: next++ } } : { ok: true };
    return { ok: true, status: 201, json: async () => body };
  };
  return { f, calls };
}
const sendOpt = (f, text, files) => ({ fetch: f, base: "/im", slug: "im", id: "IM-0001", csrf: "tok", text, t: JA,
  pending: { files: files || [], uploaded: [] } });

test("コメントの送信: 本文も添付も無ければ要求 0 件でエラーを出す（本文だけ・添付だけなら送る）", async () => {
  for (const text of ["", "  \n\t "]) {
    const { f, calls } = fakeFetch();
    const res = await sendComment(sendOpt(f, text));
    assert.equal(calls.length, 0, JSON.stringify(text) + " で要求を送った");
    assert.deepEqual(res, { ok: false, error: "本文か添付のどちらかを入れてください。何も送っていない。" });
  }
  // 対照 1: 本文だけ → コメントの要求 1 件（添付の ID は付けない）
  {
    const { f, calls } = fakeFetch();
    assert.deepEqual(await sendComment(sendOpt(f, " 記録 ")), { ok: true });
    assert.equal(calls.length, 1);
    assert.equal(calls[0].url, "/im/api/v1/issues/IM-0001/comments");
    assert.deepEqual(JSON.parse(calls[0].init.body), { text: "記録" });
    assert.equal(calls[0].init.headers["X-CSRF-Token"], "tok");
  }
  // 対照 2: 添付だけ → 添付の要求 1 件で、空のコメントは送らない
  {
    const { f, calls } = fakeFetch();
    const opt = sendOpt(f, "", [{ name: "a.png", type: "image/png", size: 3 }]);
    assert.deepEqual(await sendComment(opt), { ok: true });
    assert.equal(calls.length, 1);
    assert.match(calls[0].url, /^\/im\/api\/v1\/issues\/IM-0001\/attachments\?project=im$/);
    assert.deepEqual(opt.pending, { files: [], uploaded: [{ id: 40, name: "a.png", size: 3 }] });
  }
  // 本文と添付 → 添付を送ってから、返った ID をコメントに付ける
  {
    const { f, calls } = fakeFetch();
    await sendComment(sendOpt(f, "記録", [{ name: "a.png", type: "image/png", size: 3 }, { name: "b.log", type: "text/plain", size: 1 }]));
    assert.equal(calls.length, 3);
    assert.deepEqual(JSON.parse(calls[2].init.body), { text: "記録", attachments: [40, 41] });
  }
});

test("コメントの送信: 添付が拒まれたらそこで止め、コメントは送らない", async () => {
  const calls = [];
  const f = async (url, init) => {
    calls.push(url);
    return { ok: false, status: 413, json: async () => ({ error: { message: "1 ファイルの上限を超えています" } }) };
  };
  const opt = sendOpt(f, "記録", [{ name: "big.bin", size: 9 }, { name: "next.bin", size: 1 }]);
  const res = await sendComment(opt);
  assert.deepEqual(res, { ok: false, error: "big.bin を添付できなかった。1 ファイルの上限を超えています" });
  assert.equal(calls.length, 1);
  assert.equal(opt.pending.files.length, 2);   // 送れなかったものは送る前の一覧に残る
});

import { localizeCommentHeading, COMMENT_SECTION } from "./render.js";

// 保存した本文のコメント節の見出しは構造の目印なので「## コメント」のまま。描くときだけ画面の言語の見出しに替える。
const STORED = "## Details\n\nWrite the README.\n\n" + COMMENT_SECTION + "\n\n### 2026-10-01 10:00\n\nDone.";

test("英語の画面では、詳細のコメント節の見出しが英語で出る", () => {
  assert.equal(EN.comments_heading, "Comments");   // 対訳表（en.json）の文面
  const src = localizeCommentHeading(STORED, EN);
  assert.equal(src, STORED.replace(COMMENT_SECTION, "## Comments"));
  const html = r.md(src);
  assert.match(html, /<h2>Comments<\/h2>/);
  assert.doesNotMatch(html, HAS_JA);   // 英語の本文に日本語の見出しが混ざらない
});

test("日本語の画面では、コメント節の見出しは「コメント」のまま出る（英語の対照）", () => {
  assert.equal(localizeCommentHeading(STORED, JA), STORED);
  assert.match(r.md(localizeCommentHeading(STORED, JA)), /<h2>コメント<\/h2>/);
});

test("置き換えるのはコードブロックの外の最初の見出しだけで、文面が無ければ本文をそのまま返す", () => {
  const body = "```\n" + COMMENT_SECTION + "\n```\n\n" + COMMENT_SECTION + "\n\nquoted:\n\n" + COMMENT_SECTION;
  assert.equal(localizeCommentHeading(body, EN), "```\n" + COMMENT_SECTION + "\n```\n\n## Comments\n\nquoted:\n\n" + COMMENT_SECTION);
  assert.equal(localizeCommentHeading(STORED, {}), STORED);
  assert.equal(localizeCommentHeading("## Details\n\nno comments", EN), "## Details\n\nno comments");
});

import { countText } from "./render.js";

// プロジェクト選択の画面の見出しの下の行（hub.html が server.web.hub.sub と sub_one を渡す）。
const hubTexts = lang => ({ sub: catalog(lang)["server.web.hub.sub"], sub_one: catalog(lang)["server.web.hub.sub_one"] });

test("英語のプロジェクト選択の画面は、1 件なら project・2 件なら projects と出る", () => {
  const en = hubTexts("en");
  assert.equal(countText(en, "sub", 1, { open: 6, at: "2026-10-02 21:13" }), "1 project / 6 open / as of 2026-10-02 21:13");
  assert.equal(countText(en, "sub", 2, { open: 1, at: "x" }), "2 projects / 1 open / as of x");
  assert.equal(countText(en, "sub", 0, { open: 0, at: "x" }), "0 projects / 0 open / as of x");
});

test("日本語のプロジェクト選択の画面は、件数に関わらず同じ文面（英語の対照）", () => {
  const ja = hubTexts("ja");
  assert.equal(countText(ja, "sub", 1, { open: 6, at: "t" }), "1 プロジェクト　／　6 件が未クローズ　／　t 時点");
  assert.equal(countText(ja, "sub", 2, { open: 6, at: "t" }), "2 プロジェクト　／　6 件が未クローズ　／　t 時点");
});

test("単数の文面が無ければ、1 件でも基の文面を使う", () => {
  assert.equal(countText({ sub: "{n} items" }, "sub", 1), "1 items");
  assert.equal(countText({}, "sub", 1), "");
});

// Go の i18n.TN（<ID>_one を件数 1 のときだけ選ぶ）と同じ規則を、同じ対訳表で確かめる。
// 対訳表の <ID>_one を持つ基の文面は、n=1 で単数・n=0 と n=2 で基の文面になり、日本語は件数に関わらず同じ。
test("countText は Go の i18n.TN と同じく、_one を持つ文面を n=1 のときだけ単数にする", () => {
  const en = catalog("en"), ja = catalog("ja");
  const bases = Object.keys(en).filter(k => k.endsWith("_one") && en[k.slice(0, -4)] !== undefined).map(k => k.slice(0, -4));
  assert.ok(bases.length >= 20, "単数の文面を持つ文面が " + bases.length + " 件しかない（対訳表の読み違い）");
  // countText は {n} を件数で埋める。ほかの {名前} は埋めずに文面そのものの選ばれ方を見る
  const raw = (c, key) => ({ [key]: c[key], [key + "_one"]: c[key + "_one"] });
  const withN = (text, n) => text.split("{n}").join(String(n));
  for (const key of bases) {
    const e = raw(en, key), j = raw(ja, key);
    assert.equal(countText(e, key, 1, {}), withN(e[key + "_one"], 1), key + " の n=1（英語）");
    assert.equal(countText(e, key, 0, {}), withN(e[key], 0), key + " の n=0（英語）");
    assert.equal(countText(e, key, 2, {}), withN(e[key], 2), key + " の n=2（英語）");
    assert.equal(countText(j, key, 1, {}), withN(j[key], 1), key + " の n=1（日本語は件数に関わらず同じ文面）");
    assert.equal(countText(j, key, 2, {}), withN(j[key], 2), key + " の n=2（日本語は件数に関わらず同じ文面）");
  }
});
