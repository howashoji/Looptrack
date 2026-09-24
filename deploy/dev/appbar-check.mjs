// Web 画面の共通ヘッダが幅によって崩れないかを、実際のブラウザで測る。
//
//   node deploy/dev/appbar-check.mjs            （deploy/dev/appbar-check.sh から呼ぶ。サーバと利用者の準備もそちら）
//
// 環境変数: APPBAR_BASE（既定 http://127.0.0.1:18111/im）、APPBAR_LOGIN / APPBAR_PASSWORD（管理者）、
// APPBAR_SLUG（ボードを見るプロジェクト。イシューが 1 件以上あること）、APPBAR_CLIENT_ID（OAuth クライアント。接続の許可の画面）、
// APPBAR_OUT（スクリーンショットと測定値 measure.json の保存先。空なら保存しない）、
// APPBAR_WIDTHS（例 "360,375,600-1280:40"。既定は 360・375・601・605・600〜1280 を 40px 刻み）、APPBAR_SCHEMES（既定 "light,dark"）。
// Playwright は npm の global（npm i -g @playwright/test）と、端末の Google Chrome を使う。
//
// 各画面・各幅で次を確かめ、外れたら一覧を出して終了コード 1 で終わる:
//   - ヘッダが 1 つで、ブランドが左端（x = ヘッダの左余白）・1 行目の中心 y=26
//   - 利用者メニューが 1 行目の右端（中心 y=26、右端 = 幅 − 右余白）。ボードの検索・タブは 2 行目以降へ折り返してよい
//   - ページ固有の要素（パンくず・検索・タブ・レポート作成）がヘッダからはみ出さない・利用者メニューと重ならない
//   - 1 行の画面（ボード以外）はヘッダの高さが 53px
//   - 横スクロールが出ない
import { createRequire } from 'node:module';
import { execSync } from 'node:child_process';
import fs from 'node:fs';

const require = createRequire(execSync('npm root -g').toString().trim() + '/');
const { chromium } = require('@playwright/test');

const env = process.env;
const B = env.APPBAR_BASE || 'http://127.0.0.1:18111/im';
const SLUG = env.APPBAR_SLUG || 'req';
const OUT = env.APPBAR_OUT || '';
const widths = (env.APPBAR_WIDTHS || '360,375,601,605,600-1280:40').split(',').flatMap(w => {
  const m = w.match(/^(\d+)-(\d+):(\d+)$/);
  if (!m) return [Number(w)];
  const r = [];
  for (let x = Number(m[1]); x <= Number(m[2]); x += Number(m[3])) r.push(x);
  return r;
});
const schemes = (env.APPBAR_SCHEMES || 'light,dark').split(',');
const chal = 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM';
const pages = [
  ['hub', '/'], ['board', `/p/${SLUG}/`], ['account', '/account'], ['account_totp', '/account/totp'],
  ['admin_users', '/admin/users'], ['admin_projects', '/admin/projects'], ['admin_security', '/admin/security'],
  ['report_requests', `/p/${SLUG}/report-requests`], ['notfound', '/nope'],
  ['oauth_error', '/oauth/authorize?response_type=code&client_id=unknown'],
];
if (env.APPBAR_CLIENT_ID) {
  pages.push(['authorize', `/oauth/authorize?response_type=code&client_id=${env.APPBAR_CLIENT_ID}` +
    `&redirect_uri=${encodeURIComponent('http://127.0.0.1:9999/cb')}&code_challenge=${chal}&code_challenge_method=S256&state=x`]);
}

// measure はヘッダの各部品の位置を返す（ブラウザの中で実行する）
const measure = () => {
  const box = el => { const r = el.getBoundingClientRect(); return { l: r.left, r: r.right, t: r.top, b: r.bottom, cy: r.top + r.height / 2 }; };
  const h = document.querySelector('header.appbar');
  if (!h) return null;
  const brand = h.querySelector('.appbar-brand'), menu = h.querySelector('.usermenu');
  const slot = [...h.querySelectorAll('.appbar-row > :not(.appbar-brand):not(.usermenu) , .appbar-slot > *')]
    .filter(e => e.getBoundingClientRect().width > 0 && !e.classList.contains('appbar-slot'))
    .map(e => ({ cls: (e.className || e.tagName).toString().split(' ')[0], ...box(e) }));
  return {
    headers: document.querySelectorAll('header').length, w: document.documentElement.clientWidth,
    hscroll: document.documentElement.scrollWidth > document.documentElement.clientWidth,
    header: box(h), padL: parseFloat(getComputedStyle(h).paddingLeft), padR: parseFloat(getComputedStyle(h).paddingRight),
    brand: brand && box(brand), menu: menu && box(menu), slot,
  };
};

function problems(name, m) {
  const out = [];
  if (!m) return ['共通ヘッダが無い'];
  const near = (a, b) => Math.abs(a - b) <= 1;
  if (m.headers !== 1) out.push(`header が ${m.headers} 個`);
  if (m.hscroll) out.push('横スクロールが出る');
  if (!m.brand || !near(m.brand.l, m.padL) || !near(m.brand.cy, 26)) out.push(`ブランドの位置 x=${m.brand?.l} cy=${m.brand?.cy}`);
  if (!m.menu) out.push('利用者メニューが無い');
  else if (!near(m.menu.cy, 26) || !near(m.menu.r, m.w - m.padR)) out.push(`利用者メニューが 1 行目の右端に無い（右端 ${Math.round(m.menu.r)} / ${m.w - m.padR}、中心 y=${Math.round(m.menu.cy)}）`);
  if (name !== 'board' && !near(m.header.b - m.header.t, 53)) out.push(`ヘッダの高さ ${Math.round(m.header.b - m.header.t)}px`);
  for (const s of m.slot) {
    if (s.r > m.w - m.padR + 1 || s.l < m.padL - 1) out.push(`${s.cls} がはみ出す（${Math.round(s.l)}..${Math.round(s.r)}）`);
    if (m.menu && s.t < m.menu.b && s.b > m.menu.t && s.r > m.menu.l + 1 && s.l < m.menu.r) out.push(`${s.cls} が利用者メニューと重なる`);
  }
  return out;
}

const browser = await chromium.launch({ channel: 'chrome' });
const results = [], failures = [];
if (OUT) fs.mkdirSync(OUT, { recursive: true });
for (const scheme of schemes) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 700 }, colorScheme: scheme });
  const p = await ctx.newPage();
  await p.goto(B + '/login');
  await p.fill('input[name=login]', env.APPBAR_LOGIN || 'root');
  await p.fill('input[name=password]', env.APPBAR_PASSWORD || 'root-password-12');
  await Promise.all([p.waitForNavigation(), p.click('button[type=submit]')]);
  for (const [name, path] of pages) {
    await p.goto(B + path);
    await p.waitForTimeout(500); // ボードは JS で絞り込みの段を描く
    for (const width of widths) {
      await p.setViewportSize({ width, height: 700 });
      await p.waitForTimeout(50);
      const m = await p.evaluate(measure);
      const bad = problems(name, m);
      results.push({ name, scheme, width, ok: bad.length === 0, problems: bad, ...m });
      if (bad.length) failures.push(`${name} ${scheme} ${width}px: ${bad.join(' / ')}`);
      if (OUT) await p.screenshot({ path: `${OUT}/${name}-${scheme}-${width}.png`, clip: { x: 0, y: 0, width, height: Math.min(260, Math.ceil(m?.header.b ?? 60) + 20) } });
    }
  }
  await ctx.close();
}
await browser.close();
if (OUT) fs.writeFileSync(`${OUT}/measure.json`, JSON.stringify(results, null, 1));
console.log(`${results.length} 件（${pages.length} 画面 × ${widths.length} 幅 × ${schemes.length} 配色）を測定`);
if (failures.length) {
  console.log(`崩れ ${failures.length} 件:\n` + failures.join('\n'));
  process.exit(1);
}
console.log('崩れなし');
