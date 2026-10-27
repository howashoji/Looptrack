// 入口（サイトの直下・latest/ の英語のパス）で、日本語と英語のどちらのページへ転送するかを決める。
// 言語のメニューで選んだ言語はブラウザに覚え、ブラウザの言語より優先する。版のページへの直接のアクセスは振り分けない
// （張られたリンクの言語を勝手に変えないため）。build.mjs の転送のページと、テーマ（選んだ言語を覚える側）の両方が使う。
// node の機能を読み込まない。テーマから読まれて画面にも配られるため。

// 言語のメニューで選んだ言語を覚える localStorage の鍵。
export const LANG_STORAGE_KEY = 'looptrack-guide-lang'

// 入口で使う言語を返す。stored は覚えた言語（無ければ null）、languages は navigator.languages。
// 覚えた言語が無ければ、ブラウザの言語を好みの順に見て ja と en のうち先に出たほうを採る。どちらも無ければ英語にする。
// 転送のページへ文字列にして埋め込むので、外の変数を使わずにこの中だけで完結させ、古いブラウザでも動く書き方にする。
export function pickLang(stored, languages) {
  if (stored === 'ja' || stored === 'en') return stored
  var list = languages || []
  for (var i = 0; i < list.length; i++) {
    var tag = String(list[i]).toLowerCase()
    if (tag === 'ja' || tag.indexOf('ja-') === 0) return 'ja'
    if (tag === 'en' || tag.indexOf('en-') === 0) return 'en'
  }
  return 'en'
}

// 言語のメニューで選んだ言語を覚える。保存できない環境（プライベートブラウズ・保存の禁止）では何もしない。
export function rememberLang(lang) {
  var value = lang === 'ja' ? 'ja' : 'en'
  try {
    globalThis.localStorage.setItem(LANG_STORAGE_KEY, value)
  } catch {
    // 覚えられなくても、次の入口でブラウザの言語に従うだけなので止めない。
  }
}

// 転送のページの script。英語の行き先 en と、日本語のページがあればその行き先 ja を受け取り、言語を決めて移る。
// ja が無いページは英語へ移る。問い合わせ（?）と断片（#）はそのまま引き継ぐ。
export function redirectScript(en, ja) {
  const json = (v) => JSON.stringify(v).replace(/</g, '\\u003c')
  if (!ja) return `location.replace(${json(en)} + location.search + location.hash)`
  return `(function () {
  ${pickLang.toString()}
  var stored = null;
  try { stored = localStorage.getItem(${json(LANG_STORAGE_KEY)}); } catch (e) {}
  var langs = navigator.languages && navigator.languages.length ? navigator.languages : [navigator.language];
  var target = pickLang(stored, langs) === 'ja' ? ${json(ja)} : ${json(en)};
  location.replace(target + location.search + location.hash);
})();`
}
