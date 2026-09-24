// board_data.js の検査（node --test internal/server/static/board_data_test.mjs）。
// 見直しが重ならないこと・変化の無い周期で描き直さないこと・本文の取り出しを確かめる。
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { makePoller, fetchBoard, fetchBody, fetchSearch, bodyFromMarkdown, makeBodyCache } from "./board_data.js";

// 手で進める時計（setTimeout の代わり）。fire() で予約済みの 1 件を実行する。
function fakeTimers() {
  const q = [];
  let seq = 0;
  return {
    set(fn, ms) { const id = ++seq; q.push({ id, fn, ms }); return id; },
    clear(id) { const i = q.findIndex(x => x.id === id); if (i >= 0) q.splice(i, 1); },
    pending: () => q.length,
    fire() { const t = q.shift(); assert.ok(t, "予約された見直しが無い"); t.fn(); }
  };
}
// 応答を手で返せる取得（遅い回線の代わり）。
function slowRun() {
  const calls = [];
  let active = 0, maxActive = 0;
  const run = force => {
    active++; maxActive = Math.max(maxActive, active);
    let resolve, reject;
    const p = new Promise((res, rej) => { resolve = res; reject = rej; });
    calls.push({ force, resolve: () => { active--; resolve(); }, reject: e => { active--; reject(e); } });
    return p;
  };
  return { run, calls, max: () => maxActive };
}
const flush = () => new Promise(r => setImmediate(r));

test("見直しは前の取得が終わるまで次を始めない（遅い回線でも重ならない）", async () => {
  const tm = fakeTimers();
  const s = slowRun();
  const p = makePoller(s.run, 4000, tm);
  p.poll(true);
  await flush();
  assert.equal(s.calls.length, 1);
  assert.equal(tm.pending(), 0, "取得中に次の見直しを予約している（setInterval と同じ積み上がりになる）");
  // 取得中に周期の見直しが何度来ても、2 本目は始まらない
  p.poll(false); p.poll(false); p.poll(false);
  await flush();
  assert.equal(s.calls.length, 1, "取得中に 2 本目が始まった");
  assert.equal(s.max(), 1);
  // 対照: 取得が終わると、たまった分は 1 回にまとまって走り、その後は予約でつながる（見直しそのものは止まっていない）
  s.calls[0].resolve();
  await flush();
  assert.equal(s.calls.length, 2, "取得中に来た見直しが、完了後に 1 回走っていない");
  s.calls[1].resolve();
  await flush();
  assert.equal(tm.pending(), 1, "完了の後に次の見直しが予約されていない");
  tm.fire();
  await flush();
  assert.equal(s.calls.length, 3, "予約した見直しが走っていない");
  assert.equal(s.max(), 1, "同時に 2 本以上の取得が走った");
  s.calls[2].resolve();
  await flush();
  assert.equal(tm.pending(), 1);
});

test("setInterval（前の完了を待たない形）なら重なる（上の検査が現象を捉えられることの対照）", async () => {
  const s = slowRun();
  // 以前の board.js と同じ形: 周期ごとに無条件で取得を始める
  for (let i = 0; i < 3; i++) s.run(false);
  assert.equal(s.max(), 3);
  s.calls.forEach(c => c.resolve());
});

test("送信の直後の取り直し（force）は、走っている見直しに吸われずに完了後にもう 1 回走る", async () => {
  const tm = fakeTimers();
  const s = slowRun();
  const p = makePoller(s.run, 4000, tm);
  p.poll(false);
  await flush();
  p.poll(true);   // 取得中に送信が終わった
  await flush();
  assert.equal(s.calls.length, 1);
  s.calls[0].resolve();
  await flush();
  assert.equal(s.calls.length, 2);
  assert.equal(s.calls[1].force, true, "取得中に頼まれた force が失われた");
  s.calls[1].resolve();
  await flush();
});

test("取得が失敗しても次の見直しは予約される（失敗を知らせるのは run の仕事）", async () => {
  const tm = fakeTimers();
  const s = slowRun();
  const p = makePoller(s.run, 4000, tm);
  p.poll(true);
  await flush();
  s.calls[0].reject(new Error("HTTP 502"));
  await flush();
  assert.equal(tm.pending(), 1, "失敗の後に見直しが止まった");
  tm.fire();
  await flush();
  assert.equal(s.calls.length, 2);
  s.calls[1].resolve();
  await flush();
  p.stop();
  assert.equal(tm.pending(), 0);
});

// サーバの代わり（ETag と 304・generated のヘッダ。サーバの実物の振る舞いは Go の TestBoardNotModified が見る）。
function fakeServer() {
  const state = { etag: 'W/"a"', generated: "2026-09-24 10:00", data: { issues: [{ id: "REQ-0001", version: 1 }] }, requests: [] };
  state.fetch = async (url, opt) => {
    state.requests.push({ url, headers: opt.headers || {} });
    const h = new Map([["ETag", state.etag], ["X-Looptrack-Generated", state.generated]]);
    const headers = { get: k => h.get(k) || null };
    if ((opt.headers || {})["If-None-Match"] === state.etag) return { status: 304, ok: false, headers };
    return { status: 200, ok: true, headers, json: async () => ({ ...state.data, generated: state.generated }) };
  };
  return state;
}

test("変化の無い周期は描き直さない（generated の分が進んでも changed にならない）", async () => {
  const srv = fakeServer();
  const first = await fetchBoard(srv.fetch, "/b", "");
  assert.equal(first.changed, true);
  assert.equal(first.etag, 'W/"a"');
  assert.equal(srv.requests[0].headers["If-None-Match"], undefined);
  srv.generated = "2026-09-24 10:01";   // 時刻だけが進む
  const second = await fetchBoard(srv.fetch, "/b", first.etag);
  assert.equal(srv.requests[1].headers["If-None-Match"], 'W/"a"', "前回の ETag を送っていない");
  assert.equal(second.changed, false, "時刻だけの変化で描き直しになる");
  assert.equal(second.generated, "2026-09-24 10:01", "304 でも「… 時点」を進められるように generated を返す");
  // 対照: 内容が変わると changed になる
  srv.etag = 'W/"b"';
  srv.data = { issues: [{ id: "REQ-0001", version: 2 }] };
  const third = await fetchBoard(srv.fetch, "/b", second.etag);
  assert.equal(third.changed, true);
  assert.equal(third.etag, 'W/"b"');
  assert.equal(third.data.issues[0].version, 2);
});

test("取得の失敗は status つきの Error で投げる（黙って捨てない）", async () => {
  const fail = status => async () => ({ status, ok: false, headers: { get: () => null } });
  await assert.rejects(fetchBoard(fail(502), "/b", ""), e => e.status === 502 && /502/.test(e.message));
  await assert.rejects(fetchBoard(fail(401), "/b", ""), e => e.status === 401);
  await assert.rejects(fetchBody(fail(404), "", "REQ-1", "req"), e => e.status === 404);
  await assert.rejects(fetchSearch(fail(500), "", "req", "x"), e => e.status === 500);
});

test("本文は 1 件取得の全文から frontmatter を除いて取り出す", async () => {
  const md = "---\nid: REQ-0001\ntitle: a\n---\n\n# REQ-0001 a\n\n本文\n\n## コメント\n\n### 2026-09-24 10:00\n\nx\n";
  assert.equal(bodyFromMarkdown(md), "# REQ-0001 a\n\n本文\n\n## コメント\n\n### 2026-09-24 10:00\n\nx\n");
  assert.equal(bodyFromMarkdown("frontmatter なし"), "frontmatter なし");
  let seen = "";
  const got = await fetchBody(async url => { seen = url; return { ok: true, status: 200, json: async () => ({ markdown: md, version: 3 }) }; },
    "/im", "REQ-0001", "req");
  assert.equal(seen, "/im/api/v1/issues/REQ-0001?project=req");
  assert.equal(got.version, 3);
  assert.ok(got.body.startsWith("# REQ-0001 a"));
});

test("本文の検索はサーバの結果を ID の集合で返す（検索語は URL に符号化する）", async () => {
  let seen = "";
  const ids = await fetchSearch(async url => { seen = url; return { ok: true, status: 200, json: async () => ({ ids: ["REQ-0002"] }) }; },
    "/im", "req", "目印 & x");
  assert.equal(seen, "/im/api/v1/projects/req/board/search?q=" + encodeURIComponent("目印 & x"));
  assert.ok(ids.has("REQ-0002"));
  assert.equal(ids.size, 1);
});

test("本文のキャッシュは版が変わると使わない", () => {
  const c = makeBodyCache();
  c.put("REQ-0001", 2, "古い");
  assert.equal(c.get("REQ-0001", 2), "古い");
  assert.equal(c.get("REQ-0001", 3), undefined);
  assert.equal(c.get("REQ-0002", 2), undefined);
});

test("board.js は setInterval で見直さず、本文をボードの JSON から読まない", () => {
  const src = readFileSync(new URL("./board.js", import.meta.url), "utf8");
  assert.ok(!/setInterval\s*\(/.test(src), "board.js に setInterval が残っている");
  assert.ok(/makePoller\(/.test(src), "board.js が makePoller を使っていない");
  assert.ok(/fetchBoard\(/.test(src) && /fetchBody\(/.test(src) && /fetchSearch\(/.test(src));
  assert.ok(!/catch\s*\(\s*\(\s*\)\s*=>\s*\{\s*\/\*[^*]*\*\/\s*\}\s*\)/.test(src), "取得の失敗を黙って捨てる catch が残っている");
  assert.ok(/console\.error\(/.test(src), "取得の失敗を console に出していない");
});
