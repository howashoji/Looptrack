// board_prefs.js の検査（node --test internal/server/static/board_prefs_test.mjs）。
// 表示形式と絞り込みの条件の保存と読み戻し。往復・壊れた値の扱い・プロジェクトの分離・localStorage が使えない環境を確かめる。
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  VIEWS, TYPES, PRIORITIES, PERSISTED_FIELDS, prefsKey, browserStorage,
  encodePrefs, decodePrefs, savePrefs, loadPrefs, applyPrefs, dropStale
} from "./board_prefs.js";

// board.js の state の既定と同じ形（sort・desc は別のキーなので含めない）。
function freshState() {
  return {
    view: "board", q: "", types: new Set(), priorities: new Set(),
    readyOnly: false, hideClosed: false, label: "", assignee: "", feedbackOnly: false
  };
}
function snapshot(s) { return JSON.parse(encodePrefs(s)); }
// 手元の localStorage の代わり（Map）。
function memStorage(init) {
  const m = new Map(Object.entries(init || {}));
  return { getItem: k => (m.has(k) ? m.get(k) : null), setItem: (k, v) => { m.set(k, String(v)); }, map: m };
}
// 使えない環境の代わり。読み書きのどちらでも例外を投げる。
function throwingStorage() {
  const boom = () => { throw new Error("SecurityError"); };
  return { getItem: boom, setItem: boom };
}
function sampleState() {
  const s = freshState();
  s.view = "list"; s.q = "ログイン 修正";
  s.types = new Set(["bug", "task"]); s.priorities = new Set(["P0", "P2"]);
  s.readyOnly = true; s.hideClosed = true; s.feedbackOnly = true;
  s.label = "web"; s.assignee = "me";
  return s;
}

test("保存して読み戻すと、すべての項目が元と一致する（往復）", () => {
  const st = memStorage();
  const src = sampleState();
  assert.equal(savePrefs(st, "proj-a", src), true);
  const back = applyPrefs(freshState(), loadPrefs(st, "proj-a"));
  assert.deepEqual(back, src);
  assert.ok(back.types instanceof Set && back.priorities instanceof Set, "Set に戻っていない");
  // 対照: 何も保存していなければ既定のまま（往復の一致が「何でも一致する」検査になっていない）
  assert.deepEqual(applyPrefs(freshState(), loadPrefs(memStorage(), "proj-a")), freshState());
});

test("キーはプロジェクトごと。別のプロジェクトの条件は読まない", () => {
  const st = memStorage();
  const a = sampleState();
  const b = freshState(); b.view = "matrix"; b.label = "ops";
  savePrefs(st, "proj-a", a);
  savePrefs(st, "proj-b", b);
  assert.notEqual(prefsKey("proj-a"), prefsKey("proj-b"));
  assert.deepEqual(applyPrefs(freshState(), loadPrefs(st, "proj-a")), a);
  assert.deepEqual(applyPrefs(freshState(), loadPrefs(st, "proj-b")), b);
  // 保存の無い 3 つ目のプロジェクトは、a にも b にも引きずられない
  assert.deepEqual(applyPrefs(freshState(), loadPrefs(st, "proj-c")), freshState());
  // 前方一致で別のプロジェクトを拾わない（proj-a の保存を proj- や proj-a2 が読まない）
  assert.deepEqual(loadPrefs(st, "proj-"), {});
  assert.deepEqual(loadPrefs(st, "proj-a2"), {});
  // slug が無いときは共有のキーを作らない
  assert.equal(savePrefs(st, "", a), false);
  assert.deepEqual(loadPrefs(st, ""), {});
});

test("壊れた JSON・object でない値は既定で開く（対照: 正しい値は読める）", () => {
  for (const raw of ["{", "not json", "", "null", "[]", "42", '"board"', "true", '[{"view":"list"}]']) {
    const st = memStorage({ [prefsKey("p")]: raw });
    assert.deepEqual(applyPrefs(freshState(), loadPrefs(st, "p")), freshState(), "既定に戻っていない: " + JSON.stringify(raw));
  }
  const ok = memStorage({ [prefsKey("p")]: '{"view":"list"}' });
  assert.equal(applyPrefs(freshState(), loadPrefs(ok, "p")).view, "list");
});

test("知らない表示形式・型・優先度は捨て、正しい値は残る", () => {
  const raw = JSON.stringify({
    view: "kanban",
    types: ["bug", "nope", 7, null, "task"],
    priorities: ["P1", "P9", "p0", "P3"],
    readyOnly: true, hideClosed: "yes", feedbackOnly: 1,
    label: "web", assignee: ["x"], q: "検索"
  });
  const got = decodePrefs(raw);
  assert.ok(!("view" in got), "知らない表示形式が残っている");
  assert.deepEqual([...got.types].sort(), ["bug", "task"]);
  assert.deepEqual([...got.priorities].sort(), ["P1", "P3"]);
  assert.equal(got.readyOnly, true);                 // 正しい真偽値は残る
  assert.ok(!("hideClosed" in got) && !("feedbackOnly" in got), "真偽値でない値が残っている");
  assert.equal(got.label, "web");                    // 正しい文字列は残る
  assert.ok(!("assignee" in got), "文字列でない担当が残っている");
  assert.equal(got.q, "検索");
  // 捨てた項目は既定のまま、残った項目だけが state に入る
  const s = applyPrefs(freshState(), got);
  assert.equal(s.view, "board");
  assert.equal(s.hideClosed, false);
  assert.equal(s.assignee, "");
  // 対照: 表示形式は 3 つとも、型と優先度は全部、そのまま通る
  for (const v of VIEWS) assert.equal(decodePrefs(JSON.stringify({ view: v })).view, v);
  assert.deepEqual([...decodePrefs(JSON.stringify({ types: TYPES })).types], TYPES);
  assert.deepEqual([...decodePrefs(JSON.stringify({ priorities: PRIORITIES })).priorities], PRIORITIES);
  // 長すぎる文字列は捨てる（対照: 上限ちょうどは通る）
  assert.ok(!("q" in decodePrefs(JSON.stringify({ q: "a".repeat(501) }))));
  assert.equal(decodePrefs(JSON.stringify({ q: "a".repeat(500) })).q.length, 500);
  assert.ok(!("label" in decodePrefs(JSON.stringify({ label: "a".repeat(201) }))));
  assert.equal(decodePrefs(JSON.stringify({ label: "a".repeat(200) })).label.length, 200);
});

test("localStorage が例外を投げる環境でも、保存は黙って諦め、読み戻しは既定のまま開く", () => {
  const bad = throwingStorage();
  assert.doesNotThrow(() => savePrefs(bad, "p", sampleState()));
  assert.equal(savePrefs(bad, "p", sampleState()), false);
  assert.deepEqual(loadPrefs(bad, "p"), {});
  assert.deepEqual(applyPrefs(freshState(), loadPrefs(bad, "p")), freshState());
  // storage そのものが無い（null）場合も同じ
  assert.equal(savePrefs(null, "p", sampleState()), false);
  assert.deepEqual(loadPrefs(null, "p"), {});
  // localStorage を参照しただけで投げる環境は null になる
  const denied = { get localStorage() { throw new Error("SecurityError"); } };
  assert.equal(browserStorage(denied), null);
  assert.deepEqual(applyPrefs(freshState(), loadPrefs(browserStorage(denied), "p")), freshState());
  // 対照: 使える環境では同じ呼び出しで保存され、戻る
  const good = memStorage();
  assert.equal(browserStorage({ localStorage: good }), good);
  assert.equal(savePrefs(good, "p", sampleState()), true);
  assert.deepEqual(applyPrefs(freshState(), loadPrefs(good, "p")), sampleState());
});

test("今のデータに無いラベル・担当・型・優先度は外す（対照: あるものは残る）", () => {
  const avail = { types: TYPES, priorities: PRIORITIES, labels: ["web", "ops"], people: ["alice"] };
  // 外す側
  const s = freshState();
  s.label = "gone"; s.assignee = "bob"; s.types = new Set(["bug"]); s.priorities = new Set(["P1"]);
  assert.equal(dropStale(s, { ...avail, types: ["task"], priorities: ["P2"] }), true);
  assert.equal(s.label, ""); assert.equal(s.assignee, "");
  assert.equal(s.types.size, 0); assert.equal(s.priorities.size, 0);
  // 残す側（me・未設定・存在する担当・存在するラベル・存在する型と優先度）
  for (const a of ["", "me", "-", "alice"]) {
    const k = freshState();
    k.label = "web"; k.assignee = a; k.types = new Set(["bug"]); k.priorities = new Set(["P1"]);
    assert.equal(dropStale(k, avail), false, "外してはいけない担当: " + JSON.stringify(a));
    assert.equal(k.label, "web"); assert.equal(k.assignee, a);
    assert.equal(k.types.size, 1); assert.equal(k.priorities.size, 1);
  }
});

// board.js の呼び出し側の検査（部品の表示は DOM の無い Node では確かめられないので、書き方の取りこぼしを見つける）。
const boardSrc = readFileSync(new URL("./board.js", import.meta.url), "utf8");

test("board.js の state の項目は、並び順を除いてすべて保存の対象にしてある", () => {
  const m = boardSrc.match(/const state = \{([\s\S]*?)\n\};/);
  assert.ok(m, "前提が崩れている: board.js に const state = { ... }; が見つからない");
  const fields = [...m[1].matchAll(/\b([A-Za-z]+):/g)].map(x => x[1]);
  assert.ok(fields.includes("view") && fields.includes("sort"), "前提が崩れている: state の項目を読めていない " + fields);
  const sortOnly = ["sort", "desc"];   // 並び順は別のキー（im.sort）で持つ
  for (const f of fields) {
    if (sortOnly.includes(f)) continue;
    assert.ok(PERSISTED_FIELDS.includes(f), "state." + f + " が保存の対象に入っていない（board_prefs.js の PERSISTED_FIELDS）");
  }
  for (const f of PERSISTED_FIELDS) assert.ok(fields.includes(f), "PERSISTED_FIELDS の " + f + " が board.js の state に無い");
});

test("board.js は保存する項目を変えた箇所のすぐ後で persist() を呼ぶ", () => {
  const lines = boardSrc.split("\n");
  const re = new RegExp("state\\.(" + PERSISTED_FIELDS.join("|") + ")\\s*=[^=]|state\\.(types|priorities)\\.(add|delete|clear)\\(");
  const sites = [];
  lines.forEach((ln, i) => { if (re.test(ln) && !/^\s*\/\//.test(ln)) sites.push(i); });
  // 前提: 実物（タブ 1・チップ 5・ラベル 1・担当 1・検索 1 の 9 行）を拾えている。拾えなければ検査が空振りしている
  assert.ok(sites.length >= 9, "前提が崩れている: state の変更箇所を " + sites.length + " 行しか拾えていない");
  // else で続く行（チップの if / else if の連なり）は 1 組にまとめる。1 行で済む変更は同じ行に persist() を要る
  // （数行先まで探すと、隣の handler の persist() を自分のものと取り違えて、呼び忘れが通る）。連なりは、最後の行かその次の行に要る。
  const groups = [];
  for (const i of sites) {
    const last = groups[groups.length - 1];
    if (last && i === last.end + 1 && /^\s*else\b/.test(lines[i])) last.end = i;
    else groups.push({ start: i, end: i });
  }
  assert.ok(groups.some(g => g.end > g.start), "前提が崩れている: チップの連なりを 1 組にまとめられていない");
  for (const g of groups) {
    const upto = g.end > g.start ? g.end + 1 : g.end;
    const near = lines.slice(g.end, upto + 1).join("\n");
    assert.ok(/\bpersist\(\)/.test(near), "board.js:" + (g.start + 1) + " の変更の後に persist() が無い: " + lines[g.start].trim());
  }
  // 戻した状態を部品に出す呼び出しがある（読み込み時に 1 回）
  assert.ok(/applyPrefs\(state, loadPrefs\(browserStorage\(\), SLUG\)\)/.test(boardSrc), "読み込み時に保存した条件を戻していない");
  assert.ok(/getElementById\("q"\)\.value = state\.q/.test(boardSrc), "検索欄に戻した検索語を出していない");
});
