// 全版で共有する画像。site/brand/ の中身を build.mjs がサイトの土台の直下へ写し、各版の設定とテーマはそこを指す。
// 版ごとの出力に写さないのは、版が増えるたびに同じ画像が増えないようにするため。
import path from 'node:path'
import { siteDir } from './versions.mjs'

// og:image と og:url に使う公開先の origin。既定の SITE_BASE で公開したときの URL から取り、SITE_BASE はこの下に付く。
export const SITE_ORIGIN = new URL('https://howashoji.github.io/Looptrack/').origin

export const brandDir = path.join(siteDir, 'brand')

// 公開リポジトリの Social Preview と同じ画像。先頭のページの上部と og:image に使う。
export const SOCIAL_PREVIEW = { file: 'social-preview.png', width: 1280, height: 640 }

// アプリのアイコン（256 px）。ヘッダのタイトルの横に出す。
export const ICON = { file: 'icon.png' }

export const BRAND_FILES = [SOCIAL_PREVIEW.file, ICON.file]

// 画像の代替テキスト。画像の中の文字は英語なので、日本語のほうは何が書いてあるかを添える。
export const IMAGE_ALT = {
  en: 'Looptrack: Issue tracker as external memory for AI coding agents',
  ja: 'Looptrack のアイコンと名前。添え書きは「Issue tracker as external memory for AI coding agents」',
}

// ページの説明（meta description と og:description）。
export const DESCRIPTION = {
  en: 'User guide for Looptrack, an issue tracker that works as external memory for AI coding agents.',
  ja: 'AI コーディングエージェントの外部記憶になるイシュー管理ツール Looptrack の利用者ガイドです。',
}

// SNS などに張られたときの絵と説明（Open Graph）の head の要素。og:image は版をまたいで同じ 1 枚を絶対 URL で指す。
// 版ごとのページ（config.mjs）と、build.mjs が作る転送と案内のページの両方が使う。
export function openGraphTags({ siteBase, lang, title, description, url }) {
  const tags = [
    ['meta', { property: 'og:type', content: 'website' }],
    ['meta', { property: 'og:site_name', content: 'Looptrack' }],
    ['meta', { property: 'og:title', content: title }],
    ['meta', { property: 'og:description', content: description }],
    ['meta', { property: 'og:locale', content: lang === 'ja' ? 'ja_JP' : 'en_US' }],
    ['meta', { property: 'og:image', content: `${SITE_ORIGIN}${siteBase}${SOCIAL_PREVIEW.file}` }],
    ['meta', { property: 'og:image:width', content: String(SOCIAL_PREVIEW.width) }],
    ['meta', { property: 'og:image:height', content: String(SOCIAL_PREVIEW.height) }],
    ['meta', { property: 'og:image:alt', content: lang === 'ja' ? IMAGE_ALT.ja : IMAGE_ALT.en }],
    ['meta', { name: 'twitter:card', content: 'summary_large_image' }],
  ]
  if (url) tags.push(['meta', { property: 'og:url', content: url }])
  return tags
}
