// ボード画面のデータの取り方（DOM に触れない部分。node --test board_data_test.mjs で検査する）。
//
// - ボードの JSON は本文を持たない（全件の本文を 4 秒ごとに送ると応答が数 MB になり、遅い回線では
//   前の取得が終わる前に次の取得が始まって積み上がった）。本文は詳細を開いたときに 1 件だけ取る。
// - 見直しは前の取得が終わってから次を予約する（setInterval は前の完了を待たない）。
// - 見直しには If-None-Match を付け、変化が無ければサーバは本文の無い 304 を返す（転送も再描画もしない）。

// makePoller は run（Promise を返す 1 回の取得）を、重ならないように interval ごとに繰り返す。
// poll(force) は今すぐ 1 回取る。取得中に呼ばれたら、今の取得が終わった直後にもう 1 回だけ取る
// （送信の直後の取り直しを、走っている見直しの古い結果で済ませないため）。
// run の失敗は run 自身が知らせる（ここでは次の周期を止めないように握るだけ）。
export function makePoller(run, interval, timers) {
  const set = (timers && timers.set) || ((fn, ms) => setTimeout(fn, ms));
  const clear = (timers && timers.clear) || (id => clearTimeout(id));
  let timer = null;
  let running = null;
  let again = null;   // 取得中に poll が呼ばれた印（{ force }）
  let stopped = false;
  function schedule() {
    if (stopped) return;
    timer = set(() => { timer = null; poll(false); }, interval);
  }
  function poll(force) {
    if (stopped) return Promise.resolve();
    if (running) {
      again = { force: !!force || !!(again && again.force) };
      return running;
    }
    if (timer !== null) { clear(timer); timer = null; }
    running = Promise.resolve()
      .then(() => run(!!force))
      .catch(() => { /* 知らせるのは run の仕事。ここでは次の周期を守る */ })
      .then(() => {
        running = null;
        if (again) {
          const f = again.force;
          again = null;
          return poll(f);
        }
        schedule();
      });
    return running;
  }
  return {
    poll,
    busy: () => running !== null,
    stop: () => { stopped = true; if (timer !== null) { clear(timer); timer = null; } }
  };
}

// fetchBoard はボードの JSON を 1 回取る。etag を渡すと If-None-Match を付け、304 なら changed: false を返す。
// 304 のときも generated（「… 時点」の表示）はヘッダ X-Looptrack-Generated から取れる。
// 失敗（401 を含む）は status を持つ Error で投げる。
export async function fetchBoard(fetchFn, url, etag) {
  const headers = {};
  if (etag) headers["If-None-Match"] = etag;
  // cache: "no-store" でブラウザの HTTP キャッシュを通さない（条件付きの要求はここで自分で組む）
  const r = await fetchFn(url, { cache: "no-store", credentials: "same-origin", headers });
  if (r.status === 304) {
    return { changed: false, etag, generated: (r.headers && r.headers.get("X-Looptrack-Generated")) || "" };
  }
  if (!r.ok) throw httpError(r.status);
  const data = await r.json();
  return { changed: true, data, etag: (r.headers && r.headers.get("ETag")) || "" };
}

// fetchBody はイシュー 1 件の本文（frontmatter を除き、コメントを含む）を取る。
export async function fetchBody(fetchFn, base, id, slug) {
  const url = base + "/api/v1/issues/" + encodeURIComponent(id) + "?project=" + encodeURIComponent(slug);
  const r = await fetchFn(url, { cache: "no-store", credentials: "same-origin" });
  if (!r.ok) throw httpError(r.status);
  const d = await r.json();
  return { body: bodyFromMarkdown(d.markdown || ""), version: d.version };
}

// fetchSearch は本文の検索（サーバで照合し、当たった ID の列だけが返る）。
export async function fetchSearch(fetchFn, base, slug, q) {
  const url = base + "/api/v1/projects/" + encodeURIComponent(slug) + "/board/search?q=" + encodeURIComponent(q);
  const r = await fetchFn(url, { cache: "no-store", credentials: "same-origin" });
  if (!r.ok) throw httpError(r.status);
  const d = await r.json();
  return new Set(d.ids || []);
}

// bodyFromMarkdown は全文（frontmatter つき）から本文を取り出す（サーバの mdformat.RenderBody と同じ区切り）。
export function bodyFromMarkdown(md) {
  const sep = "\n---\n\n";
  const i = md.indexOf(sep);
  return i >= 0 ? md.slice(i + sep.length) : md;
}

// makeBodyCache は本文を「ID と版」で覚える（版はコメント・状態の変更でも進むので、古い本文を出し続けない）。
export function makeBodyCache() {
  const m = new Map();
  return {
    get(id, version) { const v = m.get(id); return v && v.version === version ? v.body : undefined; },
    put(id, version, body) { m.set(id, { version, body }); }
  };
}

function httpError(status) {
  const e = new Error("HTTP " + status);
  e.status = status;
  return e;
}
