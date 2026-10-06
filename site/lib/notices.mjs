// 配るサイトの JS と CSS に入った npm の包みを数え、ライセンスの全文を 1 つのファイルにまとめる。
// 数えるもとは、VitePress の画面の側の束に入ったモジュールのパス（.vitepress/config.mjs の bundledModules が書き出す）。
// 依存を増やさないよう、包みの置き場とライセンスのファイルは自前でたどる。
import fs from 'node:fs'
import path from 'node:path'

// 束の中の生成されたモジュール（\0 で始まる）の出どころ。どれも生成物に小さな補助のコードとして入る。
// 一覧に無いものは出どころが分からないので止める（黙って表示から漏らさないため）。
const VIRTUAL_SOURCES = [
  [/^vite\//, 'node_modules/vite'],
  [/^commonjs/, 'node_modules/vite'], // vite が内蔵する @rollup/plugin-commonjs の補助
  [/^plugin-vue:/, 'node_modules/@vitejs/plugin-vue'],
]

// ライセンスのファイルの名前。license-update.mjs のような道具のコードは外す。
const LICENSE_FILE_RE = /^(licen[sc]e|copying|notice)([.\-_][^.]*)?(\.(md|txt|markdown|rst))?$/i

const MIT_TEXT = `Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.`

// license の欄が無い包みの種類を、ファイルの文面から見分ける（表示の見出しと集計に使うだけ。全文はそのまま載せる）。
function kindFromText(text) {
  if (/Permission is hereby granted, free of charge/.test(text)) return 'MIT'
  if (/Permission to use, copy, modify, and\/or distribute this software/.test(text)) return 'ISC'
  if (/Apache License,?\s+Version 2\.0/.test(text)) return 'Apache-2.0'
  return null
}

function authorName(author) {
  if (!author) return null
  if (typeof author === 'string') return author.replace(/\s*[<(].*$/, '').trim()
  return author.name || null
}

// モジュールのパス（問い合わせを外したもの）から、包みの置き場（site/ からの相対）を返す。
// 自分たちのもの（テーマ・ガイドの中身・VitePress が作るデータ）は null を返す。
export function packageRootOf(id, { siteDir, work }) {
  // \0 の後ろが実在のファイルのもの（commonjs の包みを ES モジュールとして読む口）は、そのファイルの包みに数える。
  if (id.startsWith('\0') && !id.startsWith('\0/')) {
    const name = id.slice(1)
    for (const [re, root] of VIRTUAL_SOURCES) if (re.test(name)) return root
    throw new Error(`出どころの分からない生成のモジュールが束に入っています: ${JSON.stringify(id)}`)
  }
  const file = id.replace(/^\0/, '').replace(/[?#].*$/, '')
  const k = file.lastIndexOf('/node_modules/')
  if (k >= 0) {
    const rest = file.slice(k + '/node_modules/'.length).split('/')
    const name = rest[0].startsWith('@') ? rest.slice(0, 2).join('/') : rest[0]
    return path.relative(siteDir, file.slice(0, k) + '/node_modules/' + name).split(path.sep).join('/')
  }
  // VitePress が設定と中身から作るデータ（/@siteData・/@localSearchIndex…）。
  if (file.startsWith('/@')) return null
  if (file.startsWith(siteDir + '/') || file.startsWith(work + '/')) return null
  throw new Error(`出どころの分からないモジュールが束に入っています: ${id}`)
}

// 包みの dist に束ねて入った別の包みを、束ねた道具（esbuild など）が残す出どころの注記（// node_modules/<名前>/…）から拾う。
// 束ねられた包みは束ねた側の devDependencies にあたることが多く、lockfile の依存をたどっても出てこない。
// 注記に版（pnpm の .pnpm/<名前>@<版>/ の形）があれば、入っている版と食い違わないかも確かめる。
// 束ねられた包みが入っていなければ止める。ライセンスの全文を取るために、その版を site/package.json の
// devDependencies に版を固定して足すこと（画面の束には入らない。表示の全文の取り元にだけ使う）。
const BUNDLED_NOTE_RE = /^[ \t]*\/\/ (\S*)node_modules\/((?:@[^/\s]+\/)?[^/\s]+)\//gm
export function packagesBundledInside(file, owner, siteDir) {
  const lock = lockOf(siteDir)
  const text = fs.readFileSync(file, 'utf8')
  const ownerName = owner.slice(owner.lastIndexOf('node_modules/') + 'node_modules/'.length)
  const found = new Set()
  for (const m of text.matchAll(BUNDLED_NOTE_RE)) {
    const name = m[2]
    if (name === ownerName || name === '.pnpm') continue
    const key = resolveInLock(lock, owner, name)
    const v = /\.pnpm\/(?:@[^/+]+\+)?[^/@]+@([0-9][^/_]*)/.exec(m[1])
    if (!key || !fs.existsSync(path.join(siteDir, key, 'package.json'))) {
      throw new Error(`${owner} の束に ${name}${v ? `@${v[1]}` : ''} が入っていますが、site/ に入っていません。` +
        'ライセンスの全文を取るために、その版を site/package.json の devDependencies に版を固定して足してください')
    }
    if (v) {
      const installed = JSON.parse(fs.readFileSync(path.join(siteDir, key, 'package.json'), 'utf8')).version
      if (installed !== v[1]) throw new Error(`${owner} の束の ${name} は ${v[1]} ですが、入っているのは ${installed}（${key}）です`)
    }
    found.add(key)
  }
  return found
}

// 出どころの注記を残さずに別の包みを束ねる包み（rollup の commonjs で束ねたものなど）は、注記からは拾えない。
// そこで site/bundled-inside.json に「この包みの dist はこの包みを束ねている」を書いておき、束ねた側が配る束に
// 入っていれば、束ねられた側も表示に載せる。束ねられた側はライセンスの全文の取り元として devDependencies に
// 版を固定して入れ、その版が束ねた側の宣言する範囲（devDependencies など）に入らなければ止める
// （束ねた側を上げたときに、表示の取り元の版が古いまま残らないように）。
export function packagesFromBundleList(roots, siteDir, file = path.join(siteDir, 'bundled-inside.json')) {
  const bundles = JSON.parse(fs.readFileSync(file, 'utf8')).bundles
  if (!bundles || typeof bundles !== 'object') throw new Error(`${file}: {"bundles": {"<包み>": ["<束ねた包み>", …]}} の形にしてください`)
  const lock = lockOf(siteDir)
  const found = new Set()
  for (const root of roots) {
    const ownerName = root.slice(root.lastIndexOf('node_modules/') + 'node_modules/'.length)
    const inner = bundles[ownerName]
    if (!inner) continue
    const owner = JSON.parse(fs.readFileSync(path.join(siteDir, root, 'package.json'), 'utf8'))
    for (const name of inner) {
      const range = owner.devDependencies?.[name] ?? owner.dependencies?.[name] ?? owner.optionalDependencies?.[name]
      if (!range) throw new Error(`${file}: ${ownerName} ${owner.version} は ${name} を宣言していません（一覧を見直してください）`)
      const key = resolveInLock(lock, root, name)
      if (!key || !fs.existsSync(path.join(siteDir, key, 'package.json'))) {
        throw new Error(`${ownerName} の束に ${name}（${range}）が入っていますが、site/ に入っていません。` +
          'ライセンスの全文を取るために、範囲に入る版を site/package.json の devDependencies に版を固定して足してください')
      }
      const installed = JSON.parse(fs.readFileSync(path.join(siteDir, key, 'package.json'), 'utf8')).version
      if (!satisfies(installed, range)) {
        throw new Error(`${ownerName} ${owner.version} の束の ${name} は ${range} ですが、入っているのは ${installed}（${key}）です`)
      }
      found.add(key)
    }
  }
  return found
}

// semver の範囲の判定。一覧に出てくる形（^・~・版そのもの）だけを扱い、ほかの形は見分けられないので止める。
export function satisfies(version, range) {
  const parse = (v) => {
    const m = /^(\d+)\.(\d+)\.(\d+)$/.exec(v)
    return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : null
  }
  const v = parse(version)
  if (!v) return false // プレリリースの版は、範囲に入るかを決めずに外す
  const m = /^\s*([\^~]?)(\d+\.\d+\.\d+)\s*$/.exec(range)
  if (!m) throw new Error(`扱えない範囲の形です: ${range}`)
  const lo = parse(m[2])
  const cmp = (a, b) => a[0] - b[0] || a[1] - b[1] || a[2] - b[2]
  if (cmp(v, lo) < 0) return false
  if (m[1] === '') return cmp(v, lo) === 0
  let hi
  if (m[1] === '~') hi = [lo[0], lo[1] + 1, 0]
  else if (lo[0] > 0) hi = [lo[0] + 1, 0, 0]
  else if (lo[1] > 0) hi = [0, lo[1] + 1, 0]
  else hi = [0, 0, lo[2] + 1]
  return cmp(v, hi) < 0
}

const lockCache = new Map()
function lockOf(siteDir) {
  if (!lockCache.has(siteDir)) {
    lockCache.set(siteDir, JSON.parse(fs.readFileSync(path.join(siteDir, 'package-lock.json'), 'utf8')).packages || {})
  }
  return lockCache.get(siteDir)
}

// lockfile の中で、置き場 from の包みが名前 name で読む包みの置き場を、Node の解決と同じ順（近い node_modules から上へ）で探す。
function resolveInLock(lock, from, name) {
  let base = from
  for (;;) {
    const key = `${base}/node_modules/${name}`
    if (lock[key]) return key
    const k = base.lastIndexOf('/node_modules/')
    if (k < 0) break
    base = base.slice(0, k)
  }
  return lock[`node_modules/${name}`] ? `node_modules/${name}` : null
}

// 束に入った包みに、その dependencies と optionalDependencies を lockfile でたどった閉包を足す。
// 包みの dist に別の包みが束ねて入っていると、モジュールのパスには束ねた側の包みしか出ないため。
// devDependencies は包みの利用者に届かないので、たどらない。peerDependencies は利用する側が別に入れるもので、
// 束に入れば束の側で数えるので、たどらない。多めに載ることは許す（漏らすより安全な側）。
// 入っていない optional の包み（別の OS 向けなど）と、os か cpu を決めた包み（esbuild の実行ファイルのような、
// 端末ごとの組み立ての道具）は外す。ブラウザに配る束に入りようがなく、生成する機械ごとに入るものも変わるため。
export function expandWithDependencies(roots, siteDir) {
  const lock = JSON.parse(fs.readFileSync(path.join(siteDir, 'package-lock.json'), 'utf8')).packages || {}
  const found = new Set()
  const skipped = []
  const queue = [...roots]
  while (queue.length > 0) {
    const root = queue.shift()
    if (found.has(root)) continue
    const entry = lock[root]
    if (!entry) throw new Error(`${root}: package-lock.json にありません（npm ci で入れ直してください）`)
    if (entry.os || entry.cpu) {
      skipped.push(root)
      continue
    }
    if (!fs.existsSync(path.join(siteDir, root, 'package.json'))) {
      if (entry.optional) {
        skipped.push(root)
        continue
      }
      throw new Error(`${root}: 入っていません（npm ci で入れ直してください）`)
    }
    found.add(root)
    for (const name of Object.keys({ ...entry.dependencies, ...entry.optionalDependencies })) {
      const key = resolveInLock(lock, root, name)
      if (key) {
        queue.push(key)
      } else if (!entry.optionalDependencies || !(name in entry.optionalDependencies)) {
        throw new Error(`${root}: 依存 ${name} が package-lock.json にありません`)
      }
    }
  }
  return { roots: found, skipped }
}

// 包みの置き場の集まりから、表示に載せる項目を作る。ライセンスが分からない包みがあれば止める。
export function collectNotices(roots, siteDir) {
  const lock = JSON.parse(fs.readFileSync(path.join(siteDir, 'package-lock.json'), 'utf8')).packages || {}
  const entries = []
  for (const root of [...roots].sort()) {
    const dir = path.join(siteDir, root)
    const pkg = JSON.parse(fs.readFileSync(path.join(dir, 'package.json'), 'utf8'))
    const files = fs.readdirSync(dir).filter((f) => LICENSE_FILE_RE.test(f) && fs.statSync(path.join(dir, f)).isFile()).sort()
    const texts = files.map((f) => ({ name: f, text: fs.readFileSync(path.join(dir, f), 'utf8').trimEnd() }))
    let license = typeof pkg.license === 'string' ? pkg.license : null
    if (!license && Array.isArray(pkg.licenses)) license = pkg.licenses.map((l) => l.type).join(' OR ') || null
    // UNLICENSED（使わせない）と SEE LICENSE IN …（独自の条件）は、ファイルがあっても機械では扱いを決められないので、
    // 人が中身を確かめるまで止める。
    if (license && /^\s*(UNLICENSED|SEE LICENSE IN\b)/i.test(license)) {
      throw new Error(`${root}: license が「${license}」です。条件を人が確かめるまで、ライセンスの表示に載せられません`)
    }
    let kind = license
    let note = null
    if (!license) {
      if (texts.length === 0) throw new Error(`${root}: ライセンスが分かりません（package.json に license が無く、LICENSE などのファイルもありません）`)
      kind = kindFromText(texts.map((t) => t.text).join('\n'))
      if (!kind) throw new Error(`${root}: package.json に license が無く、ライセンスのファイル（${files.join(', ')}）の種類も見分けられません`)
      note = `package.json has no license field; the type was read from ${files.join(', ')}.`
    } else if (texts.length === 0 && license === 'CC0-1.0') {
      // CC0 は権利の放棄で、表示の義務が無い。包みにファイルが無いので、放棄の文書の URL を添える。
      note = 'CC0-1.0 is a public domain dedication and the package ships no license file. Legal code: https://creativecommons.org/publicdomain/zero/1.0/legalcode'
    } else if (texts.length === 0) {
      // 包みがライセンスのファイルを持たないときは、MIT に限って定型文と package.json の作者から全文を組む。
      // ほかの種類は定型文を持たないので止める（種類の名前だけで「全文」と称さないため）。
      const holder = authorName(pkg.author)
      if (license !== 'MIT' || !holder) {
        throw new Error(`${root}: ${license} のライセンスのファイルが包みにありません（全文を載せられません）`)
      }
      texts.push({ name: 'MIT (standard text)', text: `MIT License\n\nCopyright (c) ${holder}\n\n${MIT_TEXT}` })
      note = 'The package ships no license file; the standard MIT text is given with the author from package.json.'
    }
    const resolved = lock[root]?.resolved || null
    entries.push({ root, name: pkg.name, version: pkg.version, kind, note, resolved, homepage: pkg.homepage || null, texts })
  }
  return entries
}

export function renderNotices(entries) {
  const rule = '='.repeat(78)
  const out = [
    'Third-party notices for the Looptrack user guide site',
    'Looptrack 利用者ガイドのサイトに含まれる第三者のソフトウェアのライセンス',
    '',
    'The JavaScript and CSS served by this site include npm packages from the list below.',
    'The list covers the bundled packages and all of their dependencies, so it may name more than is actually included.',
    'このサイトが配る JavaScript と CSS には、次の一覧の npm の包みが含まれます。',
    '一覧は束に入った包みとその依存の全部なので、実際に含まれるものより多く載っていることがあります。',
    `Packages: ${entries.length}`,
    '',
  ]
  for (const e of entries) {
    out.push(rule, `${e.name} ${e.version}`, `License: ${e.kind}`)
    out.push(`npm: https://www.npmjs.com/package/${e.name}/v/${e.version}`)
    if (e.resolved) out.push(`Source: ${e.resolved}`)
    if (e.homepage) out.push(`Homepage: ${e.homepage}`)
    if (e.note) out.push(`Note: ${e.note}`)
    for (const t of e.texts) out.push('', `--- ${t.name} ---`, '', t.text)
    out.push('')
  }
  return out.join('\n') + '\n'
}
