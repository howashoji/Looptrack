// lang.mjs の検査（node --test site/lib/lang.test.mjs）。
import assert from 'node:assert/strict'
import test from 'node:test'
import vm from 'node:vm'
import { LANG_STORAGE_KEY, pickLang, redirectScript, rememberLang } from './lang.mjs'

test('pickLang: 覚えた言語がブラウザの言語より先', () => {
  assert.equal(pickLang('en', ['ja-JP']), 'en')
  assert.equal(pickLang('ja', ['en-US']), 'ja')
})

test('pickLang: ブラウザの言語を好みの順に見る', () => {
  assert.equal(pickLang(null, ['ja-JP', 'en-US']), 'ja')
  assert.equal(pickLang(null, ['JA']), 'ja')
  assert.equal(pickLang(null, ['en-US', 'ja']), 'en')
  assert.equal(pickLang(null, ['fr-FR', 'ja']), 'ja')
  assert.equal(pickLang(null, ['fr-FR', 'en', 'ja']), 'en')
})

test('pickLang: 決め手が無ければ英語', () => {
  assert.equal(pickLang(null, ['fr-FR', 'de']), 'en')
  assert.equal(pickLang(null, []), 'en')
  assert.equal(pickLang(null, undefined), 'en')
  // 覚えた値が壊れていたら無視してブラウザの言語に従う。
  assert.equal(pickLang('jp', ['ja-JP']), 'ja')
  // 「ja」で始まるだけの別の言語の札（jam など）は日本語にしない。
  assert.equal(pickLang(null, ['jam']), 'en')
})

// 転送のページの script を、ブラウザの代わりの入れ物で動かして行き先を返す。
function runRedirect(script, { stored = null, languages, language = 'en-US', storageThrows = false, search = '', hash = '' }) {
  let went = null
  const ctx = {
    location: { search, hash, replace: (u) => { went = u } },
    navigator: { languages, language },
    localStorage: {
      getItem: (k) => {
        if (storageThrows) throw new Error('blocked')
        return k === LANG_STORAGE_KEY ? stored : null
      },
    },
  }
  vm.runInNewContext(script, ctx)
  return went
}

const EN = '/Looptrack/v1.0.2/daily-use.html'
const JA = '/Looptrack/v1.0.2/ja/daily-use.html'

test('redirectScript: 日本語のブラウザは ja/ へ、英語のブラウザは英語へ', () => {
  const s = redirectScript(EN, JA)
  assert.equal(runRedirect(s, { languages: ['ja-JP', 'en'] }), JA)
  assert.equal(runRedirect(s, { languages: ['en-US', 'ja'] }), EN)
  assert.equal(runRedirect(s, { languages: ['fr'] }), EN)
})

test('redirectScript: 覚えた言語を優先し、読めなければブラウザの言語に従う', () => {
  const s = redirectScript(EN, JA)
  assert.equal(runRedirect(s, { stored: 'en', languages: ['ja-JP'] }), EN)
  assert.equal(runRedirect(s, { stored: 'ja', languages: ['en-US'] }), JA)
  assert.equal(runRedirect(s, { storageThrows: true, languages: ['ja-JP'] }), JA)
})

test('redirectScript: navigator.languages が無ければ navigator.language を見る', () => {
  const s = redirectScript(EN, JA)
  assert.equal(runRedirect(s, { languages: undefined, language: 'ja' }), JA)
  assert.equal(runRedirect(s, { languages: [], language: 'ja-JP' }), JA)
})

test('redirectScript: 問い合わせと断片を引き継ぐ', () => {
  const s = redirectScript(EN, JA)
  assert.equal(runRedirect(s, { languages: ['ja'], search: '?q=1', hash: '#x' }), `${JA}?q=1#x`)
})

test('redirectScript: 日本語のページが無ければ、日本語のブラウザでも英語へ', () => {
  const s = redirectScript(EN, null)
  assert.equal(runRedirect(s, { languages: ['ja-JP'] }), EN)
})

test('redirectScript: 行き先の </script> を閉じさせない', () => {
  const s = redirectScript('/a</script>', '/b</script>')
  assert.ok(!s.includes('</script>'))
})

test('rememberLang: 選んだ言語を覚え、保存できなくても止まらない', () => {
  const saved = new Map()
  const prev = globalThis.localStorage
  try {
    globalThis.localStorage = { setItem: (k, v) => saved.set(k, v) }
    rememberLang('ja')
    assert.equal(saved.get(LANG_STORAGE_KEY), 'ja')
    rememberLang('en-US')
    assert.equal(saved.get(LANG_STORAGE_KEY), 'en')
    globalThis.localStorage = { setItem: () => { throw new Error('blocked') } }
    assert.doesNotThrow(() => rememberLang('ja'))
  } finally {
    globalThis.localStorage = prev
  }
})
