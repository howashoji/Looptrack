// 1 つの版のガイドを作る設定。build.mjs が版ごとに環境変数を変えて呼ぶ。
// 各版の中身（docs/guide）には設定を持たせない。設定を持たない古いタグからも同じ形で作るため。
import fs from 'node:fs'
import path from 'node:path'
import { defineConfig } from 'vitepress'

// 公開リポジトリの名前は Looptrack だが、GitHub の URL は大小を区別しないので、ほかの公開物と同じ小文字で書く。
const REPO_URL = 'https://github.com/howashoji/looptrack'

function required(name) {
  const v = process.env[name]
  if (!v) throw new Error(`環境変数 ${name} がありません（site/build.mjs から呼んでください）`)
  return v
}

const siteBase = process.env.SITE_BASE || '/Looptrack/'
const version = required('SITE_VERSION')
const ref = required('SITE_REF')
const srcDir = required('SITE_SRC')
const outDir = required('SITE_OUT')
const versions = JSON.parse(required('SITE_VERSIONS'))

// README.md を各ディレクトリの先頭のページ（index）にする。
const toIndex = (p) => p.replace(/(^|\/)README\.md$/, '$1index.md')

// ガイドの相対リンクを直す。
// - docs/guide の外を指すものは、サイトには無いので、その版の ref の GitHub のファイルへ向ける。
// - README.md を指すものは、rewrites の後の名前（index.md）へ向ける。VitePress はリンク先の名前を
//   rewrites に合わせて直さないので、直さないと README.html という無いページへのリンクが残る。
// 描画の前（core の段）に直すので、VitePress の切れたリンクの検査は直した後の行き先を見る。
function guideLinks(md) {
  md.core.ruler.push('looptrack_guide_links', (state) => {
    const real = state.env.realPath || state.env.path
    if (!real) return
    const fromDir = path.posix.dirname(path.relative(srcDir, real).split(path.sep).join('/'))
    for (const block of state.tokens) {
      if (block.type !== 'inline' || !block.children) continue
      for (const tok of block.children) {
        if (tok.type !== 'link_open') continue
        const href = tok.attrGet('href')
        if (!href || href.startsWith('#') || href.startsWith('/') || /^[a-z][a-z0-9+.-]*:/i.test(href)) continue
        const cut = href.search(/[?#]/)
        const target = cut < 0 ? href : href.slice(0, cut)
        const suffix = cut < 0 ? '' : href.slice(cut)
        if (target === '') continue
        const inGuide = path.posix.normalize(path.posix.join(fromDir, target))
        if (inGuide === '..' || inGuide.startsWith('../')) {
          const inRepo = path.posix.normalize(path.posix.join('docs/guide', fromDir, target))
          // リポジトリの外まで出るものは直さない（切れたリンクとして生成を止める）。
          if (inRepo === '..' || inRepo.startsWith('../')) continue
          tok.attrSet('href', `${REPO_URL}/blob/${ref}/${inRepo}${suffix}`)
        } else if (/(^|\/)README\.md$/.test(target)) {
          tok.attrSet('href', toIndex(target) + suffix)
        }
      }
    }
  })
}

// mermaid の図のコードの枠を、図を描く部品（テーマの MermaidDiagram）に置き換える。
// コードは属性に URI の形で入れる。ページは Vue のテンプレートとして組まれるので、{{ や引用符をそのまま置けないため。
function mermaidFence(md) {
  const fence = md.renderer.rules.fence
  md.renderer.rules.fence = (tokens, idx, options, env, self) => {
    const tok = tokens[idx]
    if (tok.info.trim().split(/\s+/)[0] === 'mermaid') {
      return `<MermaidDiagram code="${encodeURIComponent(tok.content)}" />\n`
    }
    return fence(tokens, idx, options, env, self)
  }
}

// 検索の索引と検索語の分かち方。MiniSearch の既定は空白と句読点で切るだけで、日本語の文は句読点までが 1 語になり、
// 文の途中の語（「受け入れ条件」の「条件」など）が引けない。英数字だけの片は既定と同じに残し、
// それ以外の文字を含む片だけを Intl.Segmenter で語に分ける（英語の検索の結果を変えないため）。
// VitePress はこの関数を文字列にして画面にも送るので、外の変数を使わずにこの中だけで完結させる。
function searchTokenize(text) {
  const pieces = String(text).split(/[\n\r\p{Z}\p{P}]+/u)
  const words = []
  let seg = globalThis.__looptrackSearchSegmenter
  if (seg === undefined) {
    seg = typeof Intl !== 'undefined' && Intl.Segmenter ? new Intl.Segmenter('ja', { granularity: 'word' }) : null
    globalThis.__looptrackSearchSegmenter = seg
  }
  for (const piece of pieces) {
    if (piece === '') continue
    if (!seg || /^[\x00-\x7f]*$/.test(piece)) {
      words.push(piece)
      continue
    }
    for (const s of seg.segment(piece)) {
      if (s.isWordLike) words.push(s.segment)
    }
  }
  return words
}

// 画面に配る JS と CSS に入ったモジュールのパスを書き出す。build.mjs がそこから npm の包みを数え、
// ライセンスの表示（THIRD-PARTY-NOTICES.txt）を作る。サーバの側の描画（SSR）の束は配らないので数えない。
// 木の揺すりで落ちて中身の無いモジュールは数えない。CSS は JS の束の中では長さ 0 になるので、拡張子と問い合わせで見分ける。
function bundledModules(file) {
  let ssr = false
  return {
    name: 'looptrack-bundled-modules',
    apply: 'build',
    configResolved(config) {
      ssr = Boolean(config.build.ssr)
    },
    generateBundle(_, bundle) {
      if (ssr) return
      const ids = new Set()
      for (const item of Object.values(bundle)) {
        if (item.type !== 'chunk') continue
        for (const [id, mod] of Object.entries(item.modules)) {
          if (mod.renderedLength > 0 || /\.css($|\?)|[?&]type=style/.test(id)) ids.add(id)
        }
      }
      fs.writeFileSync(file, JSON.stringify([...ids].sort(), null, 1))
    },
  }
}

const versionSelect = { siteBase, current: version, versions }

export default defineConfig({
  base: `${siteBase}${version}/`,
  srcDir,
  outDir,
  cacheDir: required('SITE_CACHE'),
  title: 'Looptrack',
  titleTemplate: `:title | Looptrack ${version}`,
  rewrites: toIndex,
  vite: { plugins: [bundledModules(required('SITE_MODULES'))] },
  // ガイドは生の HTML を使わず、本文に <version> や <ID> のような置き換えの印を書く。
  // HTML として読ませると Vue のタグとして解釈されて生成が止まる（<time> のように実在のタグもある）ので、文字として出す。
  markdown: {
    html: false,
    config(md) {
      guideLinks(md)
      mermaidFence(md)
    },
  },
  themeConfig: {
    looptrack: versionSelect,
    socialLinks: [{ icon: 'github', link: REPO_URL }],
    footer: { message: `<a href="${siteBase}THIRD-PARTY-NOTICES.txt">Third-party licenses</a>` },
    outline: { level: [2, 3] },
    search: {
      provider: 'local',
      options: {
        miniSearch: { options: { tokenize: searchTokenize } },
        locales: {
          ja: {
            translations: {
              button: { buttonText: '検索', buttonAriaLabel: '検索' },
              modal: {
                displayDetails: '詳しく表示',
                resetButtonTitle: '消す',
                backButtonTitle: '閉じる',
                noResultsText: '見つかりません',
                footer: { selectText: '選ぶ', navigateText: '移る', closeText: '閉じる' },
              },
            },
          },
        },
      },
    },
  },
  locales: {
    root: { label: 'English', lang: 'en' },
    ja: {
      label: '日本語',
      lang: 'ja',
      link: '/ja/',
      themeConfig: {
        outline: { level: [2, 3], label: '目次' },
        docFooter: { prev: '前のページ', next: '次のページ' },
        darkModeSwitchLabel: '表示の色',
        lightModeSwitchTitle: '明るい表示にする',
        darkModeSwitchTitle: '暗い表示にする',
        sidebarMenuLabel: 'メニュー',
        returnToTopLabel: '先頭へ戻る',
        langMenuLabel: '言語',
        footer: { message: `<a href="${siteBase}THIRD-PARTY-NOTICES.txt">第三者のソフトウェアのライセンス</a>` },
        notFound: {
          title: 'ページが見つかりません',
          quote: 'この版のガイドには、このページがありません。',
          linkLabel: 'この版の先頭へ',
          linkText: 'この版の先頭へ',
        },
      },
    },
  },
})
