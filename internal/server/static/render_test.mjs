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
  body_acceptance: "## 受け入れ条件"
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
    commentForm({ canEdit: true, it, t: EN }),
    apiErrorMessage(403, null, EN),
    apiErrorMessage(502, null, EN),
    buildIssueBody("", "works", EN)
  ].join("\n");
  assert.doesNotMatch(html, HAS_JA, "英語の辞書で描いた出力に日本語が残っています:\n" + html);
  // 辞書を引けているか（空の辞書でも「日本語が無い」は成立してしまうため）
  assert.match(html, /Unassign|Reassign|assignee|Body|Comment|Status/i);
});
