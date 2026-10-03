// ボード画面の表示形式と絞り込みの条件を、ブラウザの localStorage に残して次に開いたときに戻す処理。
// DOM に触れない純粋な関数だけを置く（node --test board_prefs_test.mjs で検査する）。
//
// - 保存先はプロジェクトごとのキー。ラベルや担当者はプロジェクトで違うので、別のプロジェクトの条件は読まない。
// - 読み戻した値は 1 項目ずつ検める。知らない値は捨てて既定のままにし、ほかの項目は生かす。
// - localStorage が使えない環境（例外を投げる・private window）では、保存も読み戻しも黙って諦めて既定で開く。
// - 並び順（sort・desc）は board.js が別のキーで持っているので、ここでは扱わない。

export const VIEWS = ["board", "list", "matrix"];
// 型と優先度はサーバの決まった集合（internal/domain/issue.go）。読み込みの時点ではまだボードの JSON が無いので、同じ並びをここに持つ。
export const TYPES = ["requirement", "design", "task", "bug", "test", "epic"];
export const PRIORITIES = ["P0", "P1", "P2", "P3"];
// 保存する項目の一覧。board.js の state にここへ無い項目が増えたら、検査（board_prefs_test.mjs）が落ちる。
export const PERSISTED_FIELDS = ["view", "q", "types", "priorities", "readyOnly", "hideClosed", "label", "assignee", "feedbackOnly"];
const MAX_Q = 500;
const MAX_NAME = 200;

export function prefsKey(slug) { return "im.filters." + slug; }

// browserStorage は localStorage を返す。参照しただけで例外を投げる環境（cookie の遮断など）では null を返す。
export function browserStorage(scope) {
  try { return (scope || globalThis).localStorage || null; } catch (err) { return null; }
}

// encodePrefs は state のうち保存する項目だけを JSON にする（Set は配列にする）。
export function encodePrefs(state) {
  return JSON.stringify({
    view: state.view, q: state.q,
    types: [...state.types], priorities: [...state.priorities],
    readyOnly: state.readyOnly, hideClosed: state.hideClosed, feedbackOnly: state.feedbackOnly,
    label: state.label, assignee: state.assignee
  });
}

// decodePrefs は保存された文字列から、検めて通った項目だけを返す。壊れた JSON や object でない値は {} になる。
export function decodePrefs(raw) {
  let o;
  try { o = JSON.parse(raw); } catch (err) { return {}; }
  if (!o || typeof o !== "object" || Array.isArray(o)) return {};
  const out = {};
  if (VIEWS.includes(o.view)) out.view = o.view;
  if (typeof o.q === "string" && o.q.length <= MAX_Q) out.q = o.q;
  if (Array.isArray(o.types)) out.types = new Set(o.types.filter(x => TYPES.includes(x)));
  if (Array.isArray(o.priorities)) out.priorities = new Set(o.priorities.filter(x => PRIORITIES.includes(x)));
  for (const k of ["readyOnly", "hideClosed", "feedbackOnly"]) if (typeof o[k] === "boolean") out[k] = o[k];
  for (const k of ["label", "assignee"]) if (typeof o[k] === "string" && o[k].length <= MAX_NAME) out[k] = o[k];
  return out;
}

// savePrefs は state を保存する。保存できたかを返し、失敗しても例外は投げない（保存できなくても画面は動く）。
export function savePrefs(storage, slug, state) {
  if (!storage || !slug) return false;
  try { storage.setItem(prefsKey(slug), encodePrefs(state)); return true; } catch (err) { return false; }
}

// loadPrefs は保存された条件のうち有効な項目を返す。何も無い・読めない・使えない環境では {} になる。
export function loadPrefs(storage, slug) {
  if (!storage || !slug) return {};
  try {
    const raw = storage.getItem(prefsKey(slug));
    return raw == null ? {} : decodePrefs(raw);
  } catch (err) { return {}; }
}

// applyPrefs は loadPrefs の結果を state に写す。結果に無い項目は state のまま（既定）。
export function applyPrefs(state, prefs) {
  for (const k of PERSISTED_FIELDS) if (k in prefs) state[k] = prefs[k];
  return state;
}

// dropStale は、戻した条件のうち今のデータに無いものを外す。外したら true を返す。
// ラベルや担当者は読み込みの時点では確かめられず、残ったままだと選択欄は「すべて」を示すのに一覧だけが絞られる。
// avail は { types, priorities, labels, people }（どれもボードの JSON から数えた配列）。
export function dropStale(state, avail) {
  let changed = false;
  for (const [k, list] of [["types", avail.types], ["priorities", avail.priorities]]) {
    for (const v of [...state[k]]) if (!list.includes(v)) { state[k].delete(v); changed = true; }
  }
  if (state.label && !avail.labels.includes(state.label)) { state.label = ""; changed = true; }
  const a = state.assignee;
  if (a && a !== "me" && a !== "-" && !avail.people.includes(a)) { state.assignee = ""; changed = true; }
  return changed;
}
