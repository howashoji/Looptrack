#!/usr/bin/env node
// 利用者ガイドのサイトを版ごとに作る。
//
//   node site/build.mjs <公開リポジトリのチェックアウト> <出力先>
//
// 版は公開リポジトリのタグのうち、リリースの形（v<数>.<数>.<数> と -rc.<数>）のものを全部拾う。
// 中身はその版のタグから取る。文書だけを直した版は site/versions.json の overrides に「版 → ref」を書いて差し替える
// （ref はタグ・origin/<名前>・ローカルのブランチの順に解決する）。リリースのたびに一覧を書き換えずに済むようにするため。
// 各版の docs/guide を git archive で一時ディレクトリに取り出し、site/ の設定（.vitepress）で VitePress に作らせる。
// 設定を各版の中身から読まないのは、設定を持たない古いタグも同じ形で作るため。
//
// 出力の形（SITE_BASE は既定で /Looptrack/）:
//   <SITE_BASE><版>/      英語（docs/guide 直下）
//   <SITE_BASE><版>/ja/   日本語（docs/guide/ja）
//   <SITE_BASE>latest/    最新の正式版（rc でない版のうち semver で最大）の同じパスへ転送するページ。
//                         正式版が 1 つも無いときだけ、rc を含めて最大の版へ転送する。
//                         英語のパスは、言語のメニューで選んだ言語かブラウザの言語が日本語なら ja/ の同じページへ転送する
//                         （site/lib/lang.mjs）。JavaScript が無いときは英語のまま
//   <SITE_BASE>           latest/ へ転送するページ
//   404.html              無いページの案内。版をまたいで移った先にページが無いときに、その版の先頭へ案内する。
//                         版の付かないパスは latest/ の同じパスへ転送する
//   THIRD-PARTY-NOTICES.txt  配る JS と CSS に入った npm の包みのライセンスの全文（各版のフッタからリンクする）
//   social-preview.png・icon.png  全版で共有する画像（site/brand/ から写す。先頭のページ・og:image・ヘッダが指す）
import { spawn } from 'node:child_process'
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { collectNotices, expandWithDependencies, packageRootOf, packagesBundledInside, packagesFromBundleList, renderNotices } from './lib/notices.mjs'
import { VERSION_TAG_RE, loadOverrides, parseVersion, siteDir, sortNewestFirst } from './lib/versions.mjs'
import { BRAND_FILES, DESCRIPTION, SITE_ORIGIN, brandDir, openGraphTags } from './lib/brand.mjs'
import { redirectScript } from './lib/lang.mjs'

function fail(msg) {
  console.error(`build.mjs: ${msg}`)
  process.exit(1)
}

const [repoArg, outArg] = process.argv.slice(2)
if (!repoArg || !outArg) fail('使い方: node site/build.mjs <公開リポジトリのチェックアウト> <出力先>')
const repo = path.resolve(repoArg)
const out = path.resolve(outArg)
const siteBase = process.env.SITE_BASE || '/Looptrack/'
if (!siteBase.startsWith('/') || !siteBase.endsWith('/')) fail(`SITE_BASE は / で始まり / で終わる形にしてください: ${siteBase}`)

// 前の生成物が混ざると、消えたページが残って見えるので、空の出力先にだけ書く。
if (fs.existsSync(out) && fs.readdirSync(out).length > 0) fail(`出力先が空ではありません: ${out}`)

function git(...args) {
  return execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim()
}

function tryRev(name) {
  try {
    return git('rev-parse', '--verify', '--quiet', `${name}^{commit}`)
  } catch {
    return null
  }
}

// タグを先に見る。同じ名前のブランチがあってもリリースの中身を取るため。
function resolveRef(ref, tagOnly = false) {
  const candidates = [['tag', `refs/tags/${ref}`]]
  if (!tagOnly) candidates.push(['remote', `refs/remotes/origin/${ref}`], ['branch', `refs/heads/${ref}`])
  for (const [kind, name] of candidates) {
    const sha = tryRev(name)
    if (sha) return { kind, name, sha }
  }
  return null
}

const tagNames = git('tag', '--list').split('\n').filter((t) => VERSION_TAG_RE.test(t))
if (tagNames.length === 0) fail(`${repo}: リリースの形のタグがありません（取得の段でタグを取ったかを確かめてください）`)
const overrides = loadOverrides()
// 上書きに書いた版のタグが無いのは、版の名前の書き損じか、まだリリースしていない版。どちらも黙って作らずに止める。
for (const v of Object.keys(overrides)) {
  if (!tagNames.includes(v)) fail(`site/versions.json: 上書きに書いた版 ${v} のタグがありません`)
}
const versions = sortNewestFirst(tagNames).map((v) => ({ version: v, ref: overrides[v] ?? v, overridden: v in overrides }))
// 案内の行き先は、まだ試しの段階の rc より正式版を優先する。版を選ぶ欄には rc も含めて全部並べる。
const latest = (versions.find((e) => parseVersion(e.version).rc === null) ?? versions[0]).version

// ref は生成を始める前に全部解決する。途中の版で止まると、それより新しい版だけが出力に残るため。
// 上書きの無い版はタグそのものだけを見る（同じ名前のブランチに取り違えないように）。
for (const e of versions) {
  e.resolved = e.overridden ? resolveRef(e.ref) : resolveRef(e.ref, true)
  if (!e.resolved) {
    fail(e.overridden
      ? `site/versions.json: ${e.version} の上書きの ref ${e.ref} がタグ・origin/${e.ref}・ローカルのブランチのどれにもありません`
      : `${e.version}: タグ ${e.ref} を解決できません`)
  }
}

function run(cmd, args, opts) {
  return new Promise((resolve, reject) => {
    const p = spawn(cmd, args, { stdio: 'inherit', ...opts })
    p.on('error', reject)
    p.on('close', (code) => (code === 0 ? resolve() : reject(new Error(`${cmd} ${args.join(' ')} が ${code} で終わりました`))))
  })
}

// git archive の出力をそのまま tar に流す。どちらかが失敗したら止める。
function extractGuide(sha, dest) {
  return new Promise((resolve, reject) => {
    const archive = spawn('git', ['-C', repo, 'archive', '--format=tar', sha, 'docs/guide'], { stdio: ['ignore', 'pipe', 'inherit'] })
    const tar = spawn('tar', ['-x', '-f', '-', '-C', dest], { stdio: ['pipe', 'inherit', 'inherit'] })
    archive.stdout.pipe(tar.stdin)
    let left = 2
    let failed = null
    const done = (name) => (code) => {
      if (code !== 0 && !failed) failed = new Error(`${name} が ${code} で終わりました`)
      if (--left === 0) (failed ? reject(failed) : resolve())
    }
    archive.on('error', reject)
    tar.on('error', reject)
    archive.on('close', done('git archive'))
    tar.on('close', done('tar'))
  })
}

function countFiles(dir) {
  let n = 0
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    n += e.isDirectory() ? countFiles(path.join(dir, e.name)) : 1
  }
  return n
}

function listHtml(dir, rel = '') {
  const found = []
  for (const e of fs.readdirSync(path.join(dir, rel), { withFileTypes: true })) {
    const r = rel ? `${rel}/${e.name}` : e.name
    if (e.isDirectory()) {
      if (r !== 'assets') found.push(...listHtml(dir, r))
    } else if (e.name.endsWith('.html') && r !== '404.html') {
      found.push(r)
    }
  }
  return found
}

const escapeHtml = (s) => s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c])
// script の中に置く JSON。</script> で閉じられないよう < を逃がす。
const scriptJson = (v) => JSON.stringify(v).replace(/</g, '\\u003c')

// 転送と案内のページにも、版のページと同じ Open Graph を入れる。サイトの直下や latest/ のリンクが張られたとき、
// SNS の取得は転送をたどらずにこのページの head だけを読むことがあるため。
const headTags = (tags) => tags.map(([tag, attrs]) => `<${tag}${Object.entries(attrs).map(([k, v]) => ` ${k}="${escapeHtml(v)}"`).join('')}>`).join('\n')
const openGraphHead = ({ lang, url }) =>
  headTags(openGraphTags({ siteBase, lang, title: 'Looptrack', description: DESCRIPTION[lang], url: url ? `${SITE_ORIGIN}${url}` : null }))

// jaTarget は、日本語のページがあるときのその行き先。言語を決めるのは script だけで、JavaScript が無ければ target へ移る。
function redirectPage(target, lang = 'en', jaTarget = null) {
  const t = escapeHtml(target)
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="robots" content="noindex">
<title>Looptrack</title>
<link rel="canonical" href="${t}">
<meta http-equiv="refresh" content="0; url=${t}">
${openGraphHead({ lang, url: target })}
<script>${redirectScript(target, jaTarget)}</script>
</head>
<body><p><a href="${t}">${t}</a></p></body>
</html>
`
}

function notFoundPage() {
  const latestHref = escapeHtml(`${siteBase}latest/`)
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Not found | Looptrack</title>
${openGraphHead({ lang: 'en', url: null })}
<style>
body { margin: 0; padding: 64px 16px; font-family: system-ui, -apple-system, "Segoe UI", sans-serif; color: #213547; background: #fff; text-align: center; }
@media (prefers-color-scheme: dark) { body { color: #dfdfd6; background: #1b1b1f; } a { color: #a8b1ff; } }
h1 { font-size: 24px; }
p { line-height: 1.7; }
</style>
</head>
<body>
<h1 id="title">Page not found</h1>
<p id="message">This page does not exist.</p>
<p><a id="home" href="${latestHref}">Go to the latest guide</a></p>
<script>
(function () {
  var base = ${scriptJson(siteBase)};
  var versions = ${scriptJson(versions.map((e) => e.version))};
  var latest = ${scriptJson(latest)};
  var p = location.pathname;
  if (p.indexOf(base) !== 0) return;
  var parts = p.slice(base.length).split('/');
  var seg = parts[0];
  var known = versions.indexOf(seg) >= 0;
  // latest/ の下で転送のページが無いパスも、最新の版の同じパスへ送る。
  if (seg === 'latest') {
    location.replace(base + latest + '/' + parts.slice(1).join('/') + location.search + location.hash);
    return;
  }
  // 版の付かないパス（ほかのサイトや文書から版を書かずに張られたリンク）は、latest/ の同じパスへ送る。
  // 行き着いた最新の版にも無ければ、その版の 404 として下の案内になるので、転送は繰り返されない。
  if (!known) {
    location.replace(base + 'latest/' + parts.join('/') + location.search + location.hash);
    return;
  }
  var ja = parts[1] === 'ja';
  var link = document.getElementById('home');
  link.href = base + seg + '/' + (ja ? 'ja/' : '');
  if (ja) {
    document.documentElement.lang = 'ja';
    document.title = 'ページが見つかりません | Looptrack';
    document.getElementById('title').textContent = 'ページが見つかりません';
    document.getElementById('message').textContent = seg + ' のガイドには、このページがありません。';
    link.textContent = seg + ' のガイドの先頭へ';
  } else {
    document.getElementById('message').textContent = 'The ' + seg + ' guide does not have this page.';
    link.textContent = 'Go to the top of the ' + seg + ' guide';
  }
})();
</script>
</body>
</html>
`
}

const vitepressBin = path.join(siteDir, 'node_modules', 'vitepress', 'bin', 'vitepress.js')
if (!fs.existsSync(vitepressBin)) fail(`VitePress がありません。先に site/ で npm ci を実行してください: ${vitepressBin}`)
// 画像が欠けたまま全版を作ると、どのページも切れた画像を指すので、生成を始める前に止める。
for (const f of BRAND_FILES) {
  if (!fs.existsSync(path.join(brandDir, f))) fail(`共有の画像がありません: ${path.join(brandDir, f)}`)
}

// 一時ディレクトリは site/.work の下に作る。ページは Vue の部品として組まれ、vue などを置き場から上へたどって探すので、
// site/node_modules の外（OS の一時ディレクトリ）に置くと解決できない。
// 実パスにしておくのは、別名を含むパス（macOS の /var と /private/var など）だと、VitePress がページの一覧と
// リンクの解決で別々の表記を使い、ページが全部「切れたリンク」に見えるため。
fs.mkdirSync(path.join(siteDir, '.work'), { recursive: true })
const work = fs.realpathSync(fs.mkdtempSync(path.join(siteDir, '.work', 'build-')))
const started = Date.now()
// 全部の版の束に入った npm の包みの置き場（site/ からの相対）。
const packageRoots = new Set()
// 束に入った包みの dist に、さらに束ねて入っていた包み（出どころの注記と site/bundled-inside.json から拾う）。
const bundledInside = new Set()
try {
  fs.mkdirSync(out, { recursive: true })
  console.log(`サイトの土台: ${siteBase}  最新の版: ${latest}  版の数: ${versions.length}`)
  for (const { version, ref, overridden, resolved: r } of versions) {
    const t0 = Date.now()
    const src = path.join(work, version, 'src')
    fs.mkdirSync(src, { recursive: true })
    await extractGuide(r.sha, src)
    const guide = path.join(src, 'docs', 'guide')
    if (!fs.existsSync(path.join(guide, 'README.md'))) throw new Error(`${version}: ${r.name} の docs/guide に README.md がありません`)
    // 中身をどの ref のどのコミットから取ったかを、生成の記録（workflow のログ）に残す。
    console.log(`== ${version}: ${r.name} (${r.kind}${overridden ? '・versions.json の上書き' : ''}) ${r.sha} の docs/guide（${countFiles(guide)} ファイル）`)
    await run(process.execPath, [vitepressBin, 'build', siteDir], {
      env: {
        ...process.env,
        SITE_BASE: siteBase,
        SITE_VERSION: version,
        SITE_REF: ref,
        SITE_VERSIONS: JSON.stringify(versions.map((e) => e.version)),
        SITE_SRC: guide,
        SITE_OUT: path.join(out, version),
        SITE_CACHE: path.join(work, version, 'cache'),
        SITE_MODULES: path.join(work, version, 'modules.json'),
      },
    })
    for (const id of JSON.parse(fs.readFileSync(path.join(work, version, 'modules.json'), 'utf8'))) {
      const root = packageRootOf(id, { siteDir, work })
      if (!root) continue
      packageRoots.add(root)
      const file = id.replace(/^\0/, '').replace(/[?#].*$/, '')
      if (file.includes('/node_modules/') && fs.existsSync(file)) {
        for (const inner of packagesBundledInside(file, root, siteDir)) bundledInside.add(inner)
      }
    }
    console.log(`== ${version}: ${((Date.now() - t0) / 1000).toFixed(1)} 秒`)
  }

  const latestDir = path.join(out, latest)
  for (const rel of listHtml(latestDir)) {
    const page = rel === 'index.html' ? '' : rel.endsWith('/index.html') ? rel.slice(0, -'index.html'.length) : rel
    const dest = path.join(out, 'latest', rel)
    fs.mkdirSync(path.dirname(dest), { recursive: true })
    const ja = !rel.startsWith('ja/') && fs.existsSync(path.join(latestDir, 'ja', rel)) ? `${siteBase}${latest}/ja/${page}` : null
    fs.writeFileSync(dest, redirectPage(`${siteBase}${latest}/${page}`, rel.startsWith('ja/') ? 'ja' : 'en', ja))
  }
  fs.writeFileSync(path.join(out, 'index.html'), redirectPage(`${siteBase}latest/`))
  fs.writeFileSync(path.join(out, '404.html'), notFoundPage())
  for (const f of BRAND_FILES) fs.copyFileSync(path.join(brandDir, f), path.join(out, f))
  for (const inner of packagesFromBundleList(packageRoots, siteDir)) bundledInside.add(inner)
  const direct = new Set([...packageRoots, ...bundledInside])
  const expanded = expandWithDependencies(direct, siteDir)
  const notices = collectNotices(expanded.roots, siteDir)
  const inside = [...bundledInside].filter((r) => !packageRoots.has(r)).map((r) => r.replace(/^.*node_modules\//, ''))
  console.log(`束に入った包み: ${packageRoots.size}  ほかの包みの dist に束ねて入っていた包み: ${inside.length}（${inside.join('・')}）`)
  console.log(`依存をたどって足した包み: ${expanded.roots.size - direct.size}  外した包み（入っていない optional・端末ごとの実行ファイル）: ${expanded.skipped.length}`)
  fs.writeFileSync(path.join(out, 'THIRD-PARTY-NOTICES.txt'), renderNotices(notices))
  const kinds = {}
  for (const e of notices) kinds[e.kind] = (kinds[e.kind] || 0) + 1
  console.log(`ライセンスの表示: ${notices.length} 個の包み  ${Object.entries(kinds).map(([k, n]) => `${k} ${n}`).join('・')}`)
  console.log(`全体: ${((Date.now() - started) / 1000).toFixed(1)} 秒  出力先: ${out}`)
} finally {
  fs.rmSync(work, { recursive: true, force: true })
}
