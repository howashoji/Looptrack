// 版の形・並べ替えと、版ごとの中身の ref の上書きの読み込み。
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

export const siteDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

// サイトに作る版のタグの形。リリースのタグ（正式版と rc）だけを拾い、ほかの用途のタグ（取得元の記録など）は外す。
export const VERSION_TAG_RE = /^v([0-9]+)\.([0-9]+)\.([0-9]+)(?:-rc\.([1-9][0-9]*))?$/

export function parseVersion(v) {
  const m = VERSION_TAG_RE.exec(v)
  if (!m) return null
  return { nums: [Number(m[1]), Number(m[2]), Number(m[3])], rc: m[4] === undefined ? null : Number(m[4]) }
}

// semver の順序。rc は同じ数字の正式版より前に来る。
export function compareVersions(a, b) {
  const pa = parseVersion(a)
  const pb = parseVersion(b)
  for (let i = 0; i < 3; i++) {
    if (pa.nums[i] !== pb.nums[i]) return pa.nums[i] - pb.nums[i]
  }
  if (pa.rc === pb.rc) return 0
  if (pa.rc === null) return 1
  if (pb.rc === null) return -1
  return pa.rc - pb.rc
}

// 新しい版を先にして返す。
export function sortNewestFirst(versions) {
  return [...versions].sort((a, b) => compareVersions(b, a))
}

// site/versions.json は「版 → 中身を取る ref」の上書きだけを持つ。書いていない版はタグそのものから作る。
// 書き損じは生成の前に止める（意図した ref から作られないまま黙って通らないように）。
export function loadOverrides(file = path.join(siteDir, 'versions.json')) {
  const data = JSON.parse(readFileSync(file, 'utf8'))
  const overrides = data && data.overrides
  if (!overrides || typeof overrides !== 'object' || Array.isArray(overrides)) {
    throw new Error(`${file}: {"overrides": {"<版>": "<ref>"}} の形にしてください`)
  }
  for (const [version, ref] of Object.entries(overrides)) {
    if (!parseVersion(version)) throw new Error(`${file}: 版のタグの形ではありません: ${version}`)
    if (typeof ref !== 'string' || ref === '') throw new Error(`${file}: ${version} の ref が空です`)
  }
  return overrides
}
