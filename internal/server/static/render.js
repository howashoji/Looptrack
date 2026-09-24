// 表示用の変換（エスケープ・簡易 Markdown）。board.js から使い、Node でそのまま検査できるように分けてある。
// 以前のビューア（1.0.0 より前）から移すときに直した点:
//   - esc が " と ' を落としていなかった（属性値に入れると閉じられる）
//   - Markdown のリンク先を検査していなかった（javascript: が書けた）
//   - ID・ステータス・型を属性値に生のまま入れていた
export function esc(s) {
  return String(s === null || s === undefined ? "" : s)
    .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
}
export const attr = esc;

// paint は data-color / data-w を style に当てる。CSP の style-src 'self' では innerHTML で入れた
// style 属性が捨てられる（本番で色・幅が出なかった）ため、描画後に CSSOM で設定する。
export function paint(root) {
  for (const el of root.querySelectorAll("[data-color]")) el.style.background = el.dataset.color;
  for (const el of root.querySelectorAll("[data-w]")) el.style.width = el.dataset.w + "%";
}

// safeURL は表示してよいリンク先だけを返す（それ以外は無効なリンクにする）。
export function safeURL(raw) {
  const s = String(raw || "").trim();
  if (/^\/\//.test(s)) return "#";                          // //example.com（スキーム相対）は外部扱いで通さない
  if (/^(https?:|mailto:)/i.test(s)) return s;
  if (/^#/.test(s)) return s;                               // 同じページ内
  if (/^\//.test(s)) return s;                              // 絶対パス
  if (/^[^:?#]*(?:[?#]|$)/.test(s)) return s;               // 相対パス（スキームを含まない）
  return "#";
}

const CODE_MARK = "\uE000";   // 私用領域の 1 文字（本文に現れない目印）

/* ---------------- 画面の文面 ---------------- */
// 文面は JS に持たない。サーバが、その画面で使うぶんだけを
// <script type="application/json" id="i18n"> に入れて返し、ここで読む（対訳表を API では配らない）。
// Node の検査からは、同じ形のオブジェクトを引数（t）で渡す。
export function loadTexts(doc) {
  try {
    const el = (doc || document).getElementById("i18n");
    return el ? JSON.parse(el.textContent) : {};
  } catch (err) {
    return {};   // 文面が読めなくても画面は動く（空文字で出る）
  }
}

// fill は文面の {名前} を値に置き換える（Go の i18n.T と同じ書き方）。
export function fill(s, kv) {
  let out = String(s === null || s === undefined ? "" : s);
  for (const k in kv || {}) out = out.split("{" + k + "}").join(String(kv[k]));
  return out;
}

// tx は文面の取り出し（無ければ空文字）。t は loadTexts が返す形のオブジェクト。
function tx(t, key, kv) { return fill((t && t[key]) || "", kv); }

export function makeRenderer(prefix) {
  const PREFIX = String(prefix || "REQ");
  const PREFIX_ESC = PREFIX.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const ID_RE = new RegExp("\\b(" + PREFIX_ESC + "-\\d{3,4})\\b", "g");
  const ID_HEAD_RE = new RegExp("^" + PREFIX_ESC + "-", "i");

  // decorateIds は**エスケープ済みの断片**に対して働く（ID は接頭辞 + 数字なので追加の加工は要らない）
  function decorateIds(s) {
    return s.split(/(<[^>]+>)/).map(part => {
      if (part.charAt(0) === "<") return part;
      part = part.replace(ID_RE, '<a class="ilink" href="#$1" data-issue="$1">$1</a>');
      part = part.replace(/\b((?:FR|NFR|UC|ISS|DEC)-(?:[A-Z]{2,4}-)?\d{2,3})\b/g, '<span class="docid">$1</span>');
      return part;
    }).join("");
  }

  function inline(src) {
    const codes = [];
    let s = esc(src);
    s = s.replace(/`([^`]+)`/g, (m, c) => { codes.push(c); return CODE_MARK + (codes.length - 1) + CODE_MARK; });
    s = s.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
    s = s.replace(/(^|[^*])\*([^*\s][^*]*)\*/g, "$1<em>$2</em>");
    // リンク先は safeURL を通す（esc 済みなので実体参照を戻してから検査する）
    s = s.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, (m, text, url) => {
      const raw = String(url).replace(/&quot;/g, '"').replace(/&#39;/g, "'")
        .replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");
      return '<a href="' + attr(safeURL(raw)) + '" target="_blank" rel="noreferrer noopener">' + text + "</a>";
    });
    s = decorateIds(s);
    // codes は esc 済みなので、ここでは戻すだけ
    s = s.replace(new RegExp(CODE_MARK + "(\\d+)" + CODE_MARK, "g"), (m, i) => "<code>" + codes[+i] + "</code>");
    return s;
  }

  function md(src) {
    const lines = String(src || "").replace(/\r\n/g, "\n").split("\n");
    const out = [];
    let i = 0, para = [];
    const flush = () => { if (para.length) { out.push("<p>" + inline(para.join(" ")) + "</p>"); para = []; } };
    const isItem = l => /^\s*([-*+]|\d+\.)\s+/.test(l);
    while (i < lines.length) {
      const ln = lines[i];
      if (/^```/.test(ln)) {
        flush(); i++;
        const buf = [];
        while (i < lines.length && !/^```/.test(lines[i])) buf.push(lines[i++]);
        i++;
        out.push("<pre><code>" + esc(buf.join("\n")) + "</code></pre>");
        continue;
      }
      if (!ln.trim()) { flush(); i++; continue; }
      const h = ln.match(/^(#{1,6})\s+(.*)$/);
      if (h) { flush(); const lv = h[1].length; out.push("<h" + lv + ">" + inline(h[2]) + "</h" + lv + ">"); i++; continue; }
      if (/^\s*(-{3,}|\*{3,})\s*$/.test(ln)) { flush(); out.push("<hr>"); i++; continue; }
      if (/^>\s?/.test(ln)) {
        flush();
        const buf = [];
        while (i < lines.length && /^>\s?/.test(lines[i])) buf.push(lines[i++].replace(/^>\s?/, ""));
        out.push("<blockquote>" + md(buf.join("\n")) + "</blockquote>");
        continue;
      }
      if (/^\s*\|.*\|\s*$/.test(ln)) {
        flush();
        const buf = [];
        while (i < lines.length && /^\s*\|.*\|\s*$/.test(lines[i])) buf.push(lines[i++]);
        const rows = buf.map(r => r.trim().replace(/^\||\|$/g, "").split("|").map(c => c.trim()));
        let head = null, body = rows;
        if (rows.length > 1 && rows[1].every(c => /^:?-{2,}:?$/.test(c))) { head = rows[0]; body = rows.slice(2); }
        let t = "<table>";
        if (head) t += "<thead><tr>" + head.map(c => "<th>" + inline(c) + "</th>").join("") + "</tr></thead>";
        t += "<tbody>" + body.map(r => "<tr>" + r.map(c => "<td>" + inline(c) + "</td>").join("") + "</tr>").join("") + "</tbody></table>";
        out.push(t);
        continue;
      }
      if (isItem(ln)) {
        flush();
        const ordered = /^\s*\d+\.\s+/.test(ln);
        const items = [];
        while (i < lines.length && isItem(lines[i])) {
          let txt = lines[i].replace(/^\s*([-*+]|\d+\.)\s+/, "");
          i++;
          while (i < lines.length && lines[i].trim() && !isItem(lines[i])
            && !/^#{1,6}\s/.test(lines[i]) && !/^\s*\|/.test(lines[i]) && !/^```/.test(lines[i])) {
            txt += " " + lines[i].trim(); i++;
          }
          const box = txt.match(/^\[([ xX])\]\s*(.*)$/);
          if (box) {
            items.push('<li class="task">' + (box[1].toLowerCase() === "x" ? "☑" : "☐") + " " + inline(box[2]) + "</li>");
          } else {
            items.push("<li>" + inline(txt) + "</li>");
          }
        }
        out.push((ordered ? "<ol>" : "<ul>") + items.join("") + (ordered ? "</ol>" : "</ul>"));
        continue;
      }
      para.push(ln.trim()); i++;
    }
    flush();
    return out.join("\n");
  }

  return { md, inline, isIssueID: x => ID_HEAD_RE.test(x) };
}

/* ---------------- 担当者 ---------------- */
// 一覧・カードの担当の表記。未設定は空、権限を外された担当には t.assignee_inactive を付ける。
export function assigneeLabel(it, t) {
  if (!it || !it.assignee) return "";
  return it.assignee + (it.assignee_inactive ? tx(t, "assignee_inactive") : "");
}

// 担当の絞り込み。want は ""（すべて）・"me"・"-"（未設定）・login。
export function matchAssignee(it, want, me) {
  if (!want) return true;
  const a = (it && it.assignee) || "";
  if (want === "me") return a !== "" && a === me;
  if (want === "-") return a === "";
  return a === want;
}

// 詳細ドロワーの担当変更フォーム（JS を使わない通常の POST・CSRF。DESIGN.md §5-2 の閲覧のみの例外）。
// 変更できない（viewer・クローズ済み）ときは空文字。値は必ず esc / attr を通す。
export function assignForm(opt) {
  const { base, slug, csrf, it, members, me, canEdit, closed, t } = opt;
  if (!canEdit || closed || !it) return "";
  const cur = it.assignee || "";
  const seen = new Set();
  const opts = ['<option value="-"' + (cur === "" ? " selected" : "") + ">" + esc(tx(t, "unassigned")) + "</option>"];
  const add = (login, label) => {
    if (!login || seen.has(login)) return;
    seen.add(login);
    opts.push('<option value="' + attr(login) + '"' + (cur === login ? " selected" : "") + ">" + esc(label) + "</option>");
  };
  if (me) add(me, tx(t, "assign_self", { login: me }));
  (members || []).forEach(m => add(m.login, m.name ? tx(t, "assign_member", { name: m.name, login: m.login }) : m.login));
  if (cur) add(cur, assigneeLabel(it, t));   // 権限を外された担当も選択肢に残す（今の値を表示するため）
  const other = cur !== "" && cur !== me && !it.assignee_inactive;
  return '<form class="assign" method="post" action="' + attr(base + "/p/" + encodeURIComponent(slug) + "/issues/" + encodeURIComponent(it.id) + "/assign") + '">'
    + '<input type="hidden" name="csrf" value="' + attr(csrf || "") + '">'
    + '<select name="assignee" aria-label="' + attr(tx(t, "row_assignee")) + '">' + opts.join("") + "</select>"
    + (other
      ? ' <input name="override_reason" maxlength="500" placeholder="' + attr(tx(t, "assign_reason_placeholder"))
        + '" aria-label="' + attr(tx(t, "assign_reason")) + '">'
      : "")
    + ' <button type="submit">' + esc(tx(t, "assign_submit")) + "</button></form>";
}

/* ---------------- 外からの反応（DESIGN.md §5-8-6） ---------------- */
// 未応答のフィードバック（先頭「フィードバック:」のコメントで、その後に先頭語の無いコメントも状態変更も無いもの）の件数は
// サーバが board の各項目に feedback_pending として付ける（判定は SQL。画面では数え直さない）。
export function feedbackPending(it) {
  const n = Number(it && it.feedback_pending);
  return n > 0 ? Math.floor(n) : 0;
}

// カードの印（t.feedback_count）。未応答が無ければ空文字。
export function feedbackTag(it, t) {
  const n = feedbackPending(it);
  return n ? '<span class="tag feedback" title="' + attr(tx(t, "feedback_title")) + '">'
    + esc(tx(t, "feedback_count", { n })) + "</span>" : "";
}

// 絞り込み「未応答の反応」。on のときは未応答のあるものだけ（クローズ済みも含む。list --has-feedback と同じ集合）。
export function matchFeedback(it, on) {
  return !on || feedbackPending(it) > 0;
}

/* ---------------- 画面からの起票・状態の変更・コメント ---------------- */
// フォームは既存の REST API（POST …/issues・…/status・…/comments）を Cookie のセッション + X-CSRF-Token で呼ぶ。
// 出すかどうかはサーバが board に付ける can_edit（editor 以上）で決める。viewer には出さない（API も 403 を返す）。

// buildIssueBody は起票フォームの「本文」と「受け入れ条件」から、API の body（「## 内容」節に入る文）を作る。
// サーバは CLI の new と同じ雛形（## 背景 / ## 内容 / ## 受け入れ条件 / ## コメント）に body を差し込み、
// body が受け入れ条件の見出しを持てば雛形の受け入れ条件節を外す（domain.NewDocument）。
// 受け入れ条件は 1 行 1 項目で、行頭に「- [ ] 」を補う（既にチェックボックスならそのまま、箇条書きならチェックボックスにする）。
// 見出しと「未記入」の文面は、サーバの雛形（domain.template.*）と同じ語を t（作成者＝見ている人の言語）から取る。
// 本文として DB に残るので、雛形の見出しと言語を揃える（英語の雛形の中に日本語の見出しが混ざらないように）。
// t に無いとき（文面が読めなかったとき）は英語の見出しにする。サーバは日英どちらの見出しも受ける（domain.AcceptanceHeading）。
export function buildIssueBody(content, criteria, t) {
  const text = String(content || "").replace(/\r\n/g, "\n").trim();
  const items = String(criteria || "").replace(/\r\n/g, "\n").split("\n").map(l => l.trim()).filter(Boolean)
    .map(l => /^[-*+]\s+\[[ xX]\]\s/.test(l) ? l : "- [ ] " + l.replace(/^[-*+]\s+/, ""));
  if (!items.length) return text;
  const empty = tx(t, "body_empty") || "(not written yet)";
  const heading = tx(t, "body_acceptance") || "## Acceptance criteria";
  return (text || empty) + "\n\n" + heading + "\n\n" + items.join("\n");
}

// apiErrorMessage は API の失敗応答（{"error":{"message":…}}）から画面に出す文を作る。
// usage / verify の require_on_close・担当者などのルールで拒否されたときの文言は、サーバのものをそのまま出す（握りつぶさない）。
export function apiErrorMessage(status, data, t) {
  const e = data && data.error;
  const msg = e && typeof e.message === "string" ? e.message : "";
  if (msg) return msg;
  if (status === 403) return tx(t, "err_forbidden");
  return tx(t, "err_failed", { status });
}

// overrideField は「上書きの理由」の欄。サーバが上書きを許すルール違反（overridable）で拒否したときだけ表示する。
function overrideField(t) {
  return '<label class="override" hidden>' + esc(tx(t, "override"))
    + '<input name="override_reason" maxlength="500" autocomplete="off"></label>';
}

const formError = '<p class="form-error" role="alert" hidden></p>';

// newIssueForm は起票フォーム（詳細ドロワーに出す）。canEdit でなければ空文字。既定は CLI の new と同じ task / P2。
export function newIssueForm(opt) {
  const { canEdit, types, priorities, typeLabel, t } = opt || {};
  if (!canEdit) return "";
  const label = typeLabel || (t => t);
  const sel = (name, list, def, fmt) => '<select name="' + attr(name) + '">'
    + (list || []).map(v => '<option value="' + attr(v) + '"' + (v === def ? " selected" : "") + ">" + esc(fmt(v)) + "</option>").join("")
    + "</select>";
  return '<form class="issue-form" id="newIssueForm" data-action="create">'
    + "<label>" + esc(tx(t, "form_title")) + '<input name="title" required maxlength="200" autocomplete="off"></label>'
    + '<div class="row"><label>' + esc(tx(t, "form_type")) + sel("type", types, "task", label) + "</label>"
    + "<label>" + esc(tx(t, "form_priority")) + sel("priority", priorities, "P2", v => v) + "</label></div>"
    + "<label>" + esc(tx(t, "form_content")) + '<textarea name="content" rows="6"></textarea></label>'
    + "<label>" + esc(tx(t, "form_criteria")) + '<textarea name="criteria" rows="4"></textarea></label>'
    + overrideField(t) + formError
    + '<div class="actions"><button type="submit">' + esc(tx(t, "form_submit")) + "</button></div></form>";
}

// statusForm は詳細ドロワーの状態の変更フォーム。canEdit でなければ空文字。
export function statusForm(opt) {
  const { canEdit, it, statuses, t } = opt || {};
  if (!canEdit || !it) return "";
  return '<form class="issue-form inline" data-action="status" data-id="' + attr(it.id) + '">'
    + '<select name="status" aria-label="' + attr(tx(t, "status_label")) + '">'
    + (statuses || []).map(s => '<option value="' + attr(s) + '"' + (s === it.status ? " selected" : "") + ">" + esc(s) + "</option>").join("")
    + "</select>"
    + ' <input name="comment" maxlength="2000" placeholder="' + attr(tx(t, "status_comment_placeholder"))
    + '" aria-label="' + attr(tx(t, "status_comment")) + '" autocomplete="off">'
    + ' <button type="submit">' + esc(tx(t, "status_submit")) + "</button>"
    + overrideField(t) + formError + "</form>";
}

// commentForm は詳細ドロワーのコメントの追記フォーム。canEdit でなければ空文字。
export function commentForm(opt) {
  const { canEdit, it, t } = opt || {};
  if (!canEdit || !it) return "";
  return '<form class="issue-form" data-action="comment" data-id="' + attr(it.id) + '">'
    + "<label>" + esc(tx(t, "comment_label")) + '<textarea name="text" rows="3" required></textarea></label>'
    + formError
    + '<div class="actions"><button type="submit">' + esc(tx(t, "comment_submit")) + "</button></div></form>";
}

// formRequest はフォームの値（name → 値）から API の要求（base からのパスと JSON）を作る。t は画面の文面（起票の本文の見出しに使う）。
export function formRequest(action, id, slug, v, t) {
  const s = k => String((v && v[k]) || "").trim();
  const withOverride = body => { if (s("override_reason")) body.override_reason = s("override_reason"); return body; };
  if (action === "create") {
    return { path: "/api/v1/projects/" + encodeURIComponent(slug) + "/issues",
      body: withOverride({ title: s("title"), type: s("type"), priority: s("priority"), body: buildIssueBody(s("content"), s("criteria"), t) }) };
  }
  if (action === "status") {
    const body = { status: s("status") };
    if (s("comment")) body.comment = s("comment");
    return { path: "/api/v1/issues/" + encodeURIComponent(id) + "/status", body: withOverride(body) };
  }
  if (action === "comment") return { path: "/api/v1/issues/" + encodeURIComponent(id) + "/comments", body: { text: s("text") } };
  return null;
}
