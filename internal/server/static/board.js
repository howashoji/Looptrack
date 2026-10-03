// プロジェクトのイシュー画面（ボード / 一覧 / トレース / 詳細）。
// データは /im/api/v1/projects/<slug>/board から取り、4 秒ごとに見直す（現行ビューアと同じ間隔）。
// ボードの JSON は本文を持たない。本文は詳細を開いたときに 1 件だけ取り、本文の検索はサーバに任せる（board_data.js）。
// 値は必ず esc / attr を通して埋める（現行ビューアにあった属性値の引用符・javascript: リンクの穴を塞いだ）。
import { esc, attr, paint, makeRenderer, assigneeLabel, matchAssignee, assignForm, feedbackTag, matchFeedback,
  newIssueForm, statusForm, commentForm, formRequest, apiErrorMessage, loadTexts, fill,
  attachmentList, pendingList, pastedName, sendComment, localizeCommentHeading } from "./render.js";
import { makePoller, fetchBoard, fetchBody, fetchSearch, makeBodyCache } from "./board_data.js";
import { TYPES, PRIORITIES, browserStorage, loadPrefs, savePrefs, applyPrefs, dropStale } from "./board_prefs.js";

const BASE = document.body.dataset.base;
const SLUG = document.body.dataset.slug;

// この画面の文面（サーバが <script type="application/json" id="i18n"> で返す。JS には文面を持たない）。
const TEXT = loadTexts();
const t = (key, kv) => fill(TEXT[key] || "", kv);

const STATUS_COLOR = {
  "Backlog": "#8b8f97", "Todo": "#5c9bd6", "In Progress": "#e0a52b",
  "In Review": "#a26cd6", "Done": "#1f9254", "Canceled": "#8b8f97"
};
const TYPE_LABEL = {};
for (const k of ["requirement", "design", "task", "bug", "test", "epic"]) TYPE_LABEL[k] = TEXT["type_" + k] || k;
// 並び順のキーと規則は CLI の --sort と揃える（日時は新しい順が既定、同順位は ID 昇順）。
// キーは変えずに、見出しだけを文面から取る（文面が無い環境でもキーがそのまま出る）。
const SORTS = {};
for (const k of ["priority", "id", "updated", "created", "status", "type", "title"]) SORTS[k] = TEXT["sort_" + k] || k;
const DATE_SORTS = ["updated", "created"];
const SORT_STORE = "im.sort";

const state = {
  view: "board", q: "",
  types: new Set(), priorities: new Set(),
  readyOnly: false, hideClosed: false, label: "", assignee: "", feedbackOnly: false,
  sort: "priority", desc: false
};
// 並び順だけは再読み込みを跨いで残す（閲覧者ごとの好み。使えない環境では既定に戻るだけ）
try {
  const saved = JSON.parse(localStorage.getItem(SORT_STORE) || "null");
  if (saved && SORTS[saved.sort]) { state.sort = saved.sort; state.desc = !!saved.desc; }
} catch (err) { /* 使えない環境では既定のまま */ }
// 表示形式と絞り込みの条件は、プロジェクトごとのキーで残して次に開いたときに戻す（board_prefs.js。使えない環境では既定のまま）。
// state の項目を変えたら persist() を呼ぶ（呼び忘れは board_prefs_test.mjs が見つける）。
applyPrefs(state, loadPrefs(browserStorage(), SLUG));
function persist() { savePrefs(browserStorage(), SLUG, state); }
// 戻した検索語と表示形式を部品に出す（ボードの JSON が届く前から見た目を合わせる）
document.getElementById("q").value = state.q;
function syncTabs() {
  for (const b of document.querySelectorAll(".tabs button")) b.setAttribute("aria-selected", b.dataset.view === state.view ? "true" : "false");
}
syncTabs();

let DATA = { issues: [], statuses: [], closed_statuses: [], types: [], priorities: [], generated: "", project: "", prefix: "" };
let issues = [];
let byId = {};
let render0 = makeRenderer("REQ");
function reindex() { byId = {}; issues.forEach(i => { byId[i.id] = i; }); }

function color(status) { return STATUS_COLOR[status] || "#8b8f97"; }
function typeLabel(t) { return TYPE_LABEL[t] || t; }

/* ---------------- filter ---------------- */
function isClosed(it) { return DATA.closed_statuses.indexOf(it.status) >= 0; }

/* ---------------- 本文の検索（サーバで照合） ---------------- */
// 全件の本文を画面に持つと数 MB を送ることになるので、検索欄に語があるときだけサーバに照合させ、当たった ID だけを受け取る。
// 結果が届くまでは本文以外の項目だけで絞り、届いたら描き直す（打鍵ごとに要求しないよう少し待ってから送る）。
let bodyHits = { q: "", ids: new Set() };
let searchTimer = null;
let searchSeq = 0;
function searchBodies(delay) {
  clearTimeout(searchTimer);
  const q = state.q.trim().toLowerCase();
  if (!q) { bodyHits = { q: "", ids: new Set() }; return; }
  searchTimer = setTimeout(() => {
    const seq = ++searchSeq;
    fetchSearch(fetch, BASE, SLUG, q)
      .then(ids => {
        if (seq !== searchSeq || state.q.trim().toLowerCase() !== q) return;   // 古い語の結果は捨てる
        bodyHits = { q, ids };
        render();
      })
      .catch(e => { if (seq === searchSeq) reportError("search_failed", e); });
  }, delay);
}

function visible() {
  const q = state.q.trim().toLowerCase();
  return issues.filter(it => {
    if (state.hideClosed && isClosed(it)) return false;
    if (!matchFeedback(it, state.feedbackOnly)) return false;
    if (state.readyOnly && !it.ready) return false;
    if (state.types.size && !state.types.has(it.type)) return false;
    if (state.priorities.size && !state.priorities.has(it.priority)) return false;
    if (state.label && (it.labels || []).indexOf(state.label) < 0) return false;
    if (!matchAssignee(it, state.assignee, DATA.me)) return false;
    if (q) {
      // 本文（コメントを含む）の照合はサーバの検索の結果（bodyHits）で行う。題名・ID などはここで即座に照合する
      const hay = [it.id, it.title, (it.labels || []).join(" "), it.assignee || "",
        (it.refs || []).join(" "), (it.traces || []).join(" ")].join(" ").toLowerCase();
      if (hay.indexOf(q) < 0 && !(bodyHits.q === q && bodyHits.ids.has(it.id))) return false;
    }
    return true;
  });
}
function sortValue(it, key) {
  const rank = (v, order) => { const n = order.indexOf(v); return n < 0 ? order.length : n; };
  if (key === "priority") return rank(it.priority, DATA.priorities);
  if (key === "status") return rank(it.status, DATA.statuses);
  if (key === "type") return rank(it.type, DATA.types);
  return String(it[key] || "");
}
function sorted(list) {
  const dir = state.desc ? -1 : 1;
  return list.slice().sort((a, b) => {
    const va = sortValue(a, state.sort), vb = sortValue(b, state.sort);
    const c = typeof va === "number" ? va - vb : va.localeCompare(vb, "ja");
    return c * dir || a.id.localeCompare(b.id);
  });
}
function setSort(key, desc) {
  state.sort = SORTS[key] ? key : "priority";
  state.desc = desc === undefined ? DATE_SORTS.indexOf(state.sort) >= 0 : desc;
  try { localStorage.setItem(SORT_STORE, JSON.stringify({ sort: state.sort, desc: state.desc })); } catch (err) { /* 保存できなくても動く */ }
}

/* ---------------- views ---------------- */
function tags(it) {
  const out = [];
  if (it.ready) out.push('<span class="tag ready">' + esc(t("ready")) + "</span>");
  const fb = feedbackTag(it, TEXT);   // 未応答のフィードバック
  if (fb) out.push(fb);
  if (it.assignee) out.push('<span class="tag assignee' + (it.assignee_inactive ? " inactive" : "") + '">@' + esc(assigneeLabel(it, TEXT)) + "</span>");
  if ((it.blocked_by || []).length) out.push('<span class="tag blocked">' + esc(t("blocked", { ids: it.blocked_by.join(", ") })) + "</span>");
  (it.labels || []).forEach(l => out.push('<span class="tag">' + esc(l) + "</span>"));
  return out.join("");
}
function card(it) {
  return '<button class="card" data-issue="' + attr(it.id) + '">'
    + '<div class="top"><span>' + esc(it.id) + "</span>"
    + '<span class="pri ' + attr(it.priority) + '">' + esc(it.priority) + "</span>"
    + "<span>" + esc(typeLabel(it.type)) + "</span></div>"
    + '<div class="ttl">' + esc(it.title) + "</div>"
    + '<div class="bot">' + tags(it) + "</div></button>";
}
function viewBoard(list) {
  return '<div class="board">' + DATA.statuses.map(st => {
    const g = sorted(list.filter(i => i.status === st));
    return '<section class="col"><h2><span class="dot" data-color="' + attr(color(st)) + '"></span>'
      + esc(st) + '<span class="n">' + g.length + "</span></h2>"
      + '<div class="cards">' + (g.length ? g.map(card).join("") : '<div class="empty">' + esc(t("empty")) + "</div>")
      + "</div></section>";
  }).join("") + "</div>";
}
function viewList(list) {
  const rows = sorted(list).map(it =>
    '<tr data-issue="' + attr(it.id) + '"><td class="id">' + esc(it.id) + "</td>"
    + '<td><span class="pri ' + attr(it.priority) + '">' + esc(it.priority) + "</span></td>"
    + "<td>" + esc(typeLabel(it.type)) + "</td>"
    + '<td><span class="dot" data-color="' + attr(color(it.status)) + '"></span> ' + esc(it.status) + "</td>"
    + "<td>" + (esc(assigneeLabel(it, TEXT)) || "—") + "</td>"
    + "<td>" + esc(it.title) + "</td>"
    + "<td>" + (esc((it.blocked_by || []).join(", ")) || "—") + "</td>"
    + "<td>" + esc(it.updated || "") + "</td></tr>").join("");
  if (!rows) return '<div class="empty">' + esc(t("no_match")) + "</div>";
  // 見出しクリックで並べ替え（同じ列をもう一度押すと反転）
  const th = (key, txt) => {
    if (!key) return "<th>" + esc(txt) + "</th>";
    const on = state.sort === key;
    return '<th data-sort="' + attr(key) + '"'
      + (on ? ' class="sorted" aria-sort="' + (state.desc ? "descending" : "ascending") + '"' : "") + ">"
      + esc(txt) + (on ? (state.desc ? " ▼" : " ▲") : "") + "</th>";
  };
  return '<table class="list"><thead><tr>' + th("id", t("th_id")) + th("priority", t("th_priority")) + th("type", t("th_type"))
    + th("status", t("th_status")) + th("", t("th_assignee")) + th("title", t("th_title")) + th("", t("th_blocked")) + th("updated", t("th_updated"))
    + "</tr></thead><tbody>" + rows + "</tbody></table>";
}
function ilink(id) {
  return '<a class="ilink" href="#' + attr(id) + '" data-issue="' + attr(id) + '">' + esc(id) + "</a>";
}
function viewMatrix() {
  const reqs = issues.filter(i => i.type === "requirement").sort((a, b) => a.id.localeCompare(b.id));
  const byTrace = {};
  issues.forEach(it => (it.traces || []).forEach(t => {
    const k = t.toUpperCase();
    (byTrace[k] = byTrace[k] || []).push(it);
  }));
  const cell = (rid, typ) => {
    const g = (byTrace[rid.toUpperCase()] || []).filter(i => i.type === typ).sort((a, b) => a.id.localeCompare(b.id));
    if (!g.length) return '<span class="gap">—</span>';
    return g.map(i => ilink(i.id) + '<span class="gap"> (' + esc(i.status) + ")</span>").join("<br>");
  };
  if (!reqs.length) return '<div class="empty">' + esc(t("matrix_none")) + "</div>";
  const rows = reqs.map(r =>
    "<tr><td>" + ilink(r.id) + "</td>"
    + "<td>" + esc(r.status) + "</td><td>" + esc(r.title) + "</td>"
    + "<td>" + cell(r.id, "design") + "</td><td>" + cell(r.id, "task") + "</td><td>" + cell(r.id, "test") + "</td></tr>").join("");
  const gaps = reqs.filter(r => !(byTrace[r.id.toUpperCase()] || []).some(i => i.type === "test"));
  const orphan = Object.keys(byTrace).filter(t => !reqs.some(r => r.id.toUpperCase() === t)).sort();
  let html = '<div class="wrap"><table class="mx"><thead><tr><th>' + esc(t("mx_req")) + "</th><th>" + esc(t("mx_status")) + "</th><th>" + esc(t("mx_title")) + "</th>"
    + "<th>" + esc(t("mx_design")) + "</th><th>" + esc(t("mx_impl")) + "</th><th>" + esc(t("mx_test")) + "</th></tr></thead><tbody>" + rows + "</tbody></table></div>";
  html += '<div class="note"><h3>' + esc(t("gaps", { n: gaps.length })) + "</h3>"
    + (gaps.length
      ? "<ul>" + gaps.map(r => "<li>" + ilink(r.id) + " " + esc(r.title) + "</li>").join("") + "</ul>"
      : '<div class="empty">' + esc(t("empty")) + "</div>")
    + "</div>";
  if (orphan.length) {
    html += '<div class="note"><h3>' + esc(t("orphan")) + "</h3><ul>"
      + orphan.map(t => "<li><code>" + esc(t) + "</code> ← " + byTrace[t].map(i => esc(i.id)).join(", ") + "</li>").join("")
      + "</ul></div>";
  }
  return html;
}

function labelsOf() {
  const labels = [];
  issues.forEach(i => (i.labels || []).forEach(l => { if (labels.indexOf(l) < 0) labels.push(l); }));
  return labels.sort();
}
function peopleOf() {
  const people = [];
  issues.forEach(i => { if (i.assignee && people.indexOf(i.assignee) < 0) people.push(i.assignee); });
  return people.sort();
}
function renderFilters() {
  const labels = labelsOf();
  const chip = (txt, on, attrs) => '<button class="chip" aria-pressed="' + (on ? "true" : "false") + '" ' + attrs + ">" + esc(txt) + "</button>";
  let h = "";
  h += chip(t("filter_ready"), state.readyOnly, 'data-toggle="ready"');
  h += chip(t("filter_closed"), state.hideClosed, 'data-toggle="closed"');
  h += chip(t("filter_feedback"), state.feedbackOnly, 'data-toggle="feedback" title="' + attr(t("filter_feedback_title")) + '"');
  h += '<span class="sep"></span>';
  h += DATA.types.map(t => chip(typeLabel(t), state.types.has(t), 'data-type="' + attr(t) + '"')).join("");
  h += '<span class="sep"></span>';
  h += DATA.priorities.map(p => chip(p, state.priorities.has(p), 'data-pri="' + attr(p) + '"')).join("");
  // 担当の絞り込み: すべて / 自分 / 未設定 / 各担当者
  const people = peopleOf();
  const aopt = (v, txt) => '<option value="' + attr(v) + '"' + (state.assignee === v ? " selected" : "") + ">" + esc(txt) + "</option>";
  h += '<span class="sep"></span><select class="chip" id="asg" title="' + attr(t("assignee_title")) + '">'
    + aopt("", t("assignee_all")) + aopt("me", t("assignee_me")) + aopt("-", t("assignee_none"))
    + people.map(p => aopt(p, t("assignee_one", { login: p }))).join("") + "</select>";
  if (labels.length) {
    h += '<span class="sep"></span><select class="chip" id="lbl"><option value="">' + esc(t("label_all")) + "</option>"
      + labels.map(l => '<option value="' + attr(l) + '"' + (state.label === l ? " selected" : "") + ">" + esc(l) + "</option>").join("")
      + "</select>";
  }
  h += '<span class="sep"></span><select class="chip" id="sort" title="' + attr(t("sort_by")) + '">'
    + Object.keys(SORTS).map(k => '<option value="' + attr(k) + '"' + (state.sort === k ? " selected" : "") + ">"
      + esc(t("sort_option", { name: SORTS[k] })) + "</option>").join("")
    + "</select>"
    + '<button class="chip" id="dir" title="' + attr(t("dir_title")) + '">' + esc(state.desc ? t("dir_desc") : t("dir_asc")) + "</button>"
    + '<span class="sep"></span><button class="chip" id="export" title="' + attr(t("export_title")) + '">' + esc(t("export")) + "</button>";
  document.getElementById("filters").innerHTML = h;
}

function render() {
  const list = visible();
  document.getElementById("main").innerHTML =
    state.view === "board" ? viewBoard(list) :
      state.view === "list" ? viewList(list) : viewMatrix();
  paint(document.getElementById("main"));
  syncTabs();
  const shown = state.view === "matrix" ? issues.length : list.length;
  document.getElementById("proj").textContent =
    t("count", { shown, total: issues.length, at: DATA.generated });
}

/* ---------------- detail ---------------- */
function stripTitle(id, body) {
  // 本文冒頭の「# REQ-nnnn タイトル」はドロワー見出しと重複するので落とす
  return String(body || "").replace(new RegExp("^\\s*#\\s+" + id.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "\\b[^\\n]*\\n"), "");
}
// 本文は詳細を開いたときに 1 件だけ取る（ボードの JSON には無い）。同じ版なら取り直さない。
const bodies = makeBodyCache();
let openId = "";
// コメント節の見出しは保存した本文では「## コメント」のまま（構造の目印）なので、画面の言語の見出しに替えて描く
function bodyHTML(id, body) { return render0.md(localizeCommentHeading(stripTitle(id, body), TEXT)); }
function loadBody(it) {
  fetchBody(fetch, BASE, it.id, SLUG)
    .then(got => {
      bodies.put(it.id, it.version, got.body);
      const el = document.getElementById("dbodyText");
      if (openId !== it.id || !el) return;   // 取得中に別の詳細へ移った
      el.innerHTML = bodyHTML(it.id, got.body);
      paint(el);
    })
    .catch(e => {
      console.error("looptrack: " + it.id + ": " + t("body_failed", { reason: e.message }), e);
      const el = document.getElementById("dbodyText");
      if (openId === it.id && el) {
        el.innerHTML = '<p class="load-error">' + esc(t("body_failed", { reason: e.message })) + "</p>";
      }
    });
}
function setHash(h) {
  try { history.replaceState(null, "", h || location.pathname + location.search); }
  catch (err) { if (h) location.hash = h.slice(1); }
}
function openIssue(id) {
  const it = byId[id];
  if (!it) return;
  const row = (k, v) => "<dt>" + esc(k) + "</dt><dd>" + v + "</dd>";
  const ids = arr => (arr && arr.length)
    ? arr.map(x => render0.isIssueID(x) ? ilink(x) : '<span class="docid">' + esc(x) + "</span>").join(" ")
    : "—";
  const d = document.getElementById("drawer");
  const cached = bodies.get(it.id, it.version);
  d.innerHTML =
    '<div class="dhead"><div class="top"><span>' + esc(it.id) + "</span>"
    + '<span class="pri ' + attr(it.priority) + '">' + esc(it.priority) + "</span>"
    + '<span class="dot" data-color="' + attr(color(it.status)) + '"></span><span>' + esc(it.status) + "</span>"
    + "<span>" + esc(typeLabel(it.type)) + "</span>"
    + (it.ready ? '<span class="tag ready">' + esc(t("ready")) + "</span>" : "")
    + '<button class="close" id="closeBtn" aria-label="' + attr(t("close")) + '">×</button></div>'
    + "<h2>" + esc(it.title) + "</h2></div>"
    + '<dl class="dmeta">'
    // 担当。editor 以上には JS を使わない変更フォームを出す（閲覧のみの原則の例外・DESIGN.md §7）
    + row(t("row_assignee"), (esc(assigneeLabel(it, TEXT)) || esc(t("unassigned")))
      + assignForm({ base: BASE, slug: SLUG, csrf: document.body.dataset.csrf, it, members: DATA.members, me: DATA.me,
        canEdit: DATA.can_edit, closed: isClosed(it), t: TEXT }))
    // 状態の変更。editor 以上だけ。ルール（usage / verify の require_on_close・担当者）で拒否されたらその文言を出す
    + (DATA.can_edit ? row(t("row_status_change"), statusForm({ canEdit: DATA.can_edit, it, statuses: DATA.statuses, t: TEXT,
      notice: statusNotice && statusNotice.id === it.id ? statusNotice.text : "" })) : "")
    + row(t("row_blocked"), ids(it.blocked_by))
    + row(t("row_traces"), ids(it.traces))
    + row(t("row_refs"), ids(it.refs))
    + row(t("row_labels"), (it.labels || []).length ? it.labels.map(l => '<span class="tag">' + esc(l) + "</span>").join(" ") : "—")
    + row(t("row_parent"), it.parent ? ids([it.parent]) : "—")
    + row(t("row_dates"), esc(it.created || "—") + " / " + esc(it.updated || "—"))
    + (it.origin ? row(t("row_origin"), '<span class="tag">' + esc(it.origin) + "</span>") : "")
    // 旧ファイルモードから取り込んだイシューだけ元のファイル名を出す。サーバで起票したものは名前を合成しただけで実体が無い
    + (it.imported ? row(t("row_file"), '<span class="path">' + esc(it.path) + "</span>") : "")
    + "</dl>"
    + '<div class="dbody"><div id="dbodyText">' + (cached !== undefined ? bodyHTML(it.id, cached)
      : '<p class="loading">' + esc(t("body_loading")) + "</p>") + "</div>"
    // 添付の一覧（開くたびに取り直す。取得までは前に取った一覧を出しておく）
    + '<section class="atts"><h3>' + esc(t("att_h3")) + '</h3><div id="attList">'
    + (attachCache.has(it.id) ? attachmentList(attachCache.get(it.id), TEXT) : '<p class="loading">' + esc(t("att_loading")) + "</p>")
    + "</div></section>"
    + commentForm({ canEdit: DATA.can_edit, it, t: TEXT, pending: pendingItems(it.id) }) + "</div>";   // コメントの追記と添付
  openId = id;
  showDrawer(d);
  setHash("#" + id);
  if (cached === undefined) loadBody(it);
  loadAttachments(it.id);
}

/* ---------------- 添付 ---------------- */
// 一覧は GET …/attachments の応答をそのまま描く。画像として埋め込むかはサーバの inline に従う（render.js の showInline）。
// 送る前の添付はイシューごとに持つ（4 秒ごとの見直しでドロワーを描き直しても消えないように）。sent は送り終えてコメント待ちのもの。
const attachCache = new Map();
const pendingAttach = new Map();
function pendingOf(id) {
  if (!pendingAttach.has(id)) pendingAttach.set(id, { files: [], uploaded: [] });
  return pendingAttach.get(id);
}
function pendingItems(id) {
  const p = pendingAttach.get(id);
  if (!p) return [];
  return p.uploaded.map(u => ({ name: u.name, size: u.size, sent: true })).concat(p.files.map(f => ({ name: f.name, size: f.size })));
}
function hasPending(id) {
  const p = pendingAttach.get(id);
  return !!p && (p.files.length > 0 || p.uploaded.length > 0);
}
function refreshPending(form) {
  const ul = form && form.querySelector("[data-attach-pending]");
  if (ul) ul.innerHTML = pendingList(pendingItems(form.dataset.id), TEXT);
}
function addFiles(form, files, pasted) {
  if (!form || !files || !files.length) return;
  const p = pendingOf(form.dataset.id);
  for (const f of files) {
    if (!pasted) { p.files.push(f); continue; }
    const name = pastedName(f.name, f.type);
    p.files.push(name === f.name ? f : new File([f], name, { type: f.type }));
  }
  refreshPending(form);
}
function loadAttachments(id) {
  fetch(BASE + "/api/v1/issues/" + encodeURIComponent(id) + "/attachments?project=" + encodeURIComponent(SLUG),
    { credentials: "same-origin", cache: "no-store" })
    .then(r => r.json().catch(() => null).then(data => ({ r, data })))
    .then(({ r, data }) => {
      if (r.status === 401) { location.href = BASE + "/login"; return; }
      if (!r.ok) throw new Error(apiErrorMessage(r.status, data, TEXT));
      const list = (data && data.attachments) || [];
      attachCache.set(id, list);
      const el = document.getElementById("attList");
      if (openId === id && el) el.innerHTML = attachmentList(list, TEXT);
    })
    .catch(e => {
      console.error("looptrack: " + id + ": " + t("att_list_failed", { reason: e.message }), e);
      const el = document.getElementById("attList");
      if (openId === id && el) el.innerHTML = '<p class="load-error">' + esc(t("att_list_failed", { reason: e.message })) + "</p>";
    });
}
// submitComment はコメントのフォームの送信。送る順と空の送信の歯止めは render.js の sendComment にあり、ここは画面の表示だけを持つ。
async function submitComment(form) {
  const id = form.dataset.id;
  const err = form.querySelector(".form-error");
  const btn = form.querySelector('button[type="submit"]');
  const status = form.querySelector(".att-status");
  err.hidden = true;
  btn.disabled = true;
  const fail = msg => { err.textContent = msg; err.hidden = false; };
  try {
    const res = await sendComment({ fetch, base: BASE, slug: SLUG, id, csrf: document.body.dataset.csrf || "",
      text: form.elements.text.value, pending: pendingOf(id), t: TEXT,
      onProgress: msg => { status.textContent = msg; status.hidden = false; },
      onUploaded: () => refreshPending(form) });
    if (res.login) { location.href = BASE + "/login"; return; }
    if (!res.ok) { fail(res.error); return; }
    pendingAttach.delete(id);
    form.reset();
    setHash("#" + id);
    await poller.poll(true);   // 取り直して詳細を開き直す（添付の一覧も取り直す）
  } catch (e) {
    fail(t("send_failed", { reason: e.message }));
  } finally {
    btn.disabled = false;
    status.hidden = true;
  }
}
// openNew は起票フォームをドロワーに出す（editor 以上だけ）。
function openNew() {
  if (!DATA.can_edit) return;
  openId = "";
  const d = document.getElementById("drawer");
  d.innerHTML = '<div class="dhead"><div class="top"><span>' + esc(t("new_id", { prefix: DATA.prefix || "" })) + "</span>"
    + '<button class="close" id="closeBtn" aria-label="' + attr(t("close")) + '">×</button></div><h2>' + esc(t("new_h2")) + "</h2></div>"
    + '<div class="dbody">' + newIssueForm({ canEdit: DATA.can_edit, types: DATA.types, priorities: DATA.priorities, typeLabel, t: TEXT }) + "</div>";
  showDrawer(d);
  setHash("");
  const title = d.querySelector('[name="title"]');
  if (title) title.focus();
}
function showDrawer(d) {
  paint(d);
  d.classList.add("on");
  d.setAttribute("aria-hidden", "false");
  document.getElementById("scrim").classList.add("on");
  document.getElementById("closeBtn").onclick = closeIssue;
}
function closeIssue() {
  statusNotice = null;
  const d = document.getElementById("drawer");
  d.classList.remove("on");
  d.setAttribute("aria-hidden", "true");
  document.getElementById("scrim").classList.remove("on");
  openId = "";
  setHash("");
}

/* ---------------- 起票・状態の変更・コメントの送信 ---------------- */
// 既存の REST API を、画面のセッション（Cookie）と X-CSRF-Token で呼ぶ（サーバ側に新しい書き込みの経路は作らない）。
// 失敗（権限の 403・ルールの 422 など）はサーバの文言をフォームの下に出す。上書きできる違反なら理由の欄を出す。
// 成功しても応答が注意（acceptance_notice。受け入れ条件が雛形のままの着手。evidence_notice。エビデンスの無い Done）を載せたら、そのイシューの状態の変更フォームの
// 下に出し続ける（ドロワーは見直しのたびに描き直すので、フォームの中ではなくここに持つ）。閉じるか次の変更で消す。
let statusNotice = null;
function submitForm(form) {
  if (form.dataset.action === "comment") { submitComment(form); return; }
  const values = {};
  for (const el of form.elements) if (el.name) values[el.name] = el.value;
  const req = formRequest(form.dataset.action, form.dataset.id, SLUG, values, TEXT);
  if (!req) return;
  const err = form.querySelector(".form-error");
  const btn = form.querySelector('button[type="submit"]');
  err.hidden = true;
  btn.disabled = true;
  fetch(BASE + req.path, {
    method: "POST", credentials: "same-origin", cache: "no-store",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": document.body.dataset.csrf || "" },
    body: JSON.stringify(req.body)
  })
    .then(r => r.json().catch(() => null).then(data => ({ r, data })))
    .then(({ r, data }) => {
      if (r.status === 401) { location.href = BASE + "/login"; return; }
      if (!r.ok) {
        err.textContent = apiErrorMessage(r.status, data, TEXT);
        err.hidden = false;
        const ov = form.querySelector(".override");
        if (ov && data && data.error && data.error.overridable) ov.hidden = false;
        return;
      }
      form.reset();
      if (form.dataset.action === "status") {
        const text = data ? ["acceptance_notice", "evidence_notice"].map(k => typeof data[k] === "string" ? data[k] : "").filter(Boolean).join(" ") : "";
        statusNotice = text ? { id: form.dataset.id, text } : null;
      }
      const id = form.dataset.action === "create" ? data && data.issue && data.issue.id : form.dataset.id;
      if (id) setHash("#" + id);
      return poller.poll(true);   // 取り直して、起票・変更したイシューの詳細を開き直す
    })
    .catch(e => { err.textContent = t("send_failed", { reason: e.message }); err.hidden = false; })
    .finally(() => { btn.disabled = false; });
}
// drawerBusy は詳細ドロワーのフォームに書きかけがあるか（4 秒ごとの見直しで開き直して入力を消さないため）。
function drawerBusy() {
  const d = document.getElementById("drawer");
  if (!d.classList.contains("on")) return false;
  if (d.querySelector("#newIssueForm")) return true;
  if (openId && hasPending(openId)) return true;   // 送る前の添付がある
  for (const el of d.querySelectorAll("form.issue-form input, form.issue-form textarea")) if (el.value.trim()) return true;
  return d.contains(document.activeElement) && /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName);
}

/* ---------------- 課題管理表（xlsx）の書き出し ---------------- */
// 今出ている一覧をそのままサーバへ渡す（行と並びが画面と一致する）。
function filterText() {
  const p = [];
  if (state.q.trim()) p.push(t("filter_search", { q: state.q.trim() }));
  if (state.readyOnly) p.push(t("filter_ready"));
  if (state.feedbackOnly) p.push(t("filter_feedback"));
  p.push(state.hideClosed ? t("filter_closed") : t("filter_closed_shown"));
  if (state.types.size) p.push(t("filter_types", { types: [...state.types].map(typeLabel).join("・") }));
  if (state.priorities.size) p.push(t("filter_priorities", { priorities: [...state.priorities].join("・") }));
  if (state.label) p.push(t("filter_label", { label: state.label }));
  if (state.assignee) {
    p.push(t("filter_assignee", { assignee: state.assignee === "me" ? t("filter_me") : state.assignee === "-" ? t("filter_unassigned") : state.assignee }));
  }
  p.push(t("filter_sort", { name: SORTS[state.sort], dir: state.desc ? t("filter_desc") : t("filter_asc") }));
  return p.join(" / ");
}
function exportXlsx() {
  const list = sorted(visible());
  const btn = document.getElementById("export");
  if (!list.length) {
    if (btn) { btn.textContent = t("export_none"); setTimeout(renderFilters, 1500); }
    return;
  }
  const form = document.createElement("form");
  form.method = "post";
  form.action = BASE + "/api/v1/projects/" + encodeURIComponent(SLUG) + "/issues.xlsx";
  const add = (name, value) => {
    const i = document.createElement("input");
    i.type = "hidden"; i.name = name; i.value = value;
    form.appendChild(i);
  };
  add("csrf", document.body.dataset.csrf || "");
  add("ids", list.map(i => i.id).join(","));
  add("filter", filterText());
  document.body.appendChild(form);
  form.submit();
  form.remove();
}

/* ---------------- events ---------------- */
document.addEventListener("click", e => {
  const link = e.target.closest("[data-issue]");
  if (link) { e.preventDefault(); openIssue(link.dataset.issue); return; }
  const tab = e.target.closest(".tabs button");
  if (tab) { state.view = tab.dataset.view; persist(); render(); return; }
  const th = e.target.closest("th[data-sort]");
  if (th) {
    const k = th.dataset.sort;
    setSort(k, state.sort === k ? !state.desc : undefined);
    renderFilters(); render(); return;
  }
  if (e.target.closest("#dir")) { setSort(state.sort, !state.desc); renderFilters(); render(); return; }
  if (e.target.closest("#export")) { exportXlsx(); return; }
  const rm = e.target.closest("[data-attach-remove]");
  if (rm) {
    const form = rm.closest("form.issue-form");
    const p = pendingOf(form.dataset.id);
    p.files.splice(Number(rm.dataset.attachRemove) - p.uploaded.length, 1);
    refreshPending(form);
    return;
  }
  const chip = e.target.closest(".chip[aria-pressed]");
  if (chip) {
    const d = chip.dataset;
    if (d.toggle === "ready") state.readyOnly = !state.readyOnly;
    else if (d.toggle === "closed") state.hideClosed = !state.hideClosed;
    else if (d.toggle === "feedback") state.feedbackOnly = !state.feedbackOnly;
    else if (d.type) state.types.has(d.type) ? state.types.delete(d.type) : state.types.add(d.type);
    else if (d.pri) state.priorities.has(d.pri) ? state.priorities.delete(d.pri) : state.priorities.add(d.pri);
    persist(); renderFilters(); render();
  }
});
document.addEventListener("submit", e => {
  const form = e.target.closest("form.issue-form");
  if (form) { e.preventDefault(); submitForm(form); }
});
const newBtn = document.getElementById("newIssue");   // 起票ボタン（editor 以上にだけサーバが出す）
if (newBtn) newBtn.onclick = openNew;
document.addEventListener("change", e => {
  if (e.target.matches && e.target.matches("[data-attach-input]")) {
    addFiles(e.target.closest("form.issue-form"), e.target.files, false);
    e.target.value = "";   // 同じファイルをもう一度選べるように
    return;
  }
  if (e.target.id === "lbl") { state.label = e.target.value; persist(); render(); }
  if (e.target.id === "asg") { state.assignee = e.target.value; persist(); render(); }
  if (e.target.id === "sort") { setSort(e.target.value); renderFilters(); render(); }
});
// 添付: コメントのフォームへのドラッグ&ドロップと、クリップボードの画像の貼り付け（フォームがあるのは editor 以上だけ）
function commentFormOf(target) {
  const form = target && target.closest ? target.closest('form.issue-form[data-action="comment"]') : null;
  return form && document.getElementById("drawer").contains(form) ? form : null;
}
document.addEventListener("dragover", e => {
  const form = commentFormOf(e.target);
  if (!form || !e.dataTransfer || ![...e.dataTransfer.types].includes("Files")) return;
  e.preventDefault();
  form.querySelector("[data-attach-drop]").classList.add("over");
});
document.addEventListener("dragleave", e => {
  const zone = e.target.closest && e.target.closest("[data-attach-drop]");
  if (zone) zone.classList.remove("over");
});
document.addEventListener("drop", e => {
  const form = commentFormOf(e.target);
  if (!form || !e.dataTransfer || !e.dataTransfer.files.length) return;
  e.preventDefault();
  form.querySelector("[data-attach-drop]").classList.remove("over");
  addFiles(form, e.dataTransfer.files, false);
});
document.addEventListener("paste", e => {
  const form = commentFormOf(e.target);
  const files = e.clipboardData ? [...e.clipboardData.files] : [];
  // 文字の貼り付けはそのまま。表計算の範囲のように文字と画像を一緒に持つものも、文字として貼る
  if (!form || !files.length || [...e.clipboardData.types].includes("text/plain")) return;
  e.preventDefault();
  addFiles(form, files, true);
});
document.getElementById("scrim").onclick = closeIssue;
document.getElementById("q").addEventListener("input", e => { state.q = e.target.value; persist(); searchBodies(250); render(); });
document.addEventListener("keydown", e => {
  if (e.key === "Escape") closeIssue();
  // 入力欄（担当変更フォームの理由など）では / をそのまま入力させる
  const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName);
  if (e.key === "/" && !typing) { e.preventDefault(); document.getElementById("q").focus(); }
});

/* ---------------- データの取得（4 秒ごとに見直す） ---------------- */
// 前の取得が終わってから次を予約し（makePoller）、If-None-Match で変化の無い周期は 304 で済ませる（転送も描き直しもしない）。
// 失敗は console と画面の帯に出し、次の周期で取り直す。成功したら帯を消す。
let restoredChecked = false;
function apply(d) {
  DATA = d;
  issues = d.issues || [];
  render0 = makeRenderer(d.prefix);
  document.getElementById("projname").textContent = d.project || d.slug;
  document.title = (d.project || d.slug) + " — " + t("title_suffix");
  reindex();
  // 戻した条件に、今のデータに無いラベル・担当者があれば最初の 1 回だけ外す（外れた分は保存し直す）
  if (!restoredChecked) {
    restoredChecked = true;
    if (dropStale(state, { types: d.types || TYPES, priorities: d.priorities || PRIORITIES, labels: labelsOf(), people: peopleOf() })) persist();
  }
  renderFilters();
  render();
}
function showCount() {
  const shown = state.view === "matrix" ? issues.length : visible().length;
  document.getElementById("proj").textContent = t("count", { shown, total: issues.length, at: DATA.generated });
}
function reportError(key, e) {
  console.error("looptrack: " + t(key, { reason: e && e.message || String(e) }), e);
  const el = document.getElementById("loadError");
  if (!el) return;
  el.textContent = t(key, { reason: e && e.message || String(e) });
  el.hidden = false;
}
function clearError() {
  const el = document.getElementById("loadError");
  if (el) el.hidden = true;
}
const BOARD_URL = BASE + "/api/v1/projects/" + encodeURIComponent(SLUG) + "/board";
let etag = "";
function load(first) {
  return fetchBoard(fetch, BOARD_URL, first ? "" : etag)
    .then(res => {
      clearError();
      if (!res.changed) {
        // 内容は同じ。「… 時点」だけを進める（一覧は描き直さない）
        if (res.generated && res.generated !== DATA.generated) { DATA.generated = res.generated; showCount(); }
        return;
      }
      etag = res.etag;
      apply(res.data);
      if (state.q.trim()) searchBodies(0);   // 本文が変わったかもしれないので検索も取り直す
      const open = location.hash.slice(1);
      if (open && byId[open] && (first || !drawerBusy())) openIssue(open);
    })
    .catch(e => {
      if (e && e.status === 401) { location.href = BASE + "/login"; return; }
      reportError("load_failed", e);
      throw e;
    });
}
const poller = makePoller(load, 4000);
poller.poll(true);
