// 利用者ガイドと README のスクリーンショットを、合成データだけを入れた使い捨てのサーバから撮る。
//
//   node deploy/dev/screenshots.mjs        （deploy/dev/screenshots.sh から呼ぶ。サーバの起動と後片付けもそちら）
//
// 環境変数: SHOT_BASE（初回設定の前のローカルモードのサーバ。例 http://127.0.0.1:8090/looptrack）、
// SHOT_LANG（ja / en。画面の言語と合成データの言語）、SHOT_OUT（PNG の保存先）。
// Playwright は npm の global（npm i -g @playwright/test）と、端末の Google Chrome を使う。
//
// 写すのはブラウザのページの中だけ（ヘッドレスのビューポート）。データはすべてこのスクリプトが作る架空のもの
// （プロジェクト demo・管理者 admin / 表示名 Alice・サンプルのアプリの課題）。パスワードは乱数で作り、出力しない。
// 検証の記録の host と workspace も架空の値を送る（CLI の verify は手元のホスト名を送るので使わない）。
import { createRequire } from 'node:module';
import { execSync } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';

const require = createRequire(execSync('npm root -g').toString().trim() + '/');
const { chromium } = require('@playwright/test');

const B = process.env.SHOT_BASE;
const LANG = process.env.SHOT_LANG || 'ja';
const OUT = process.env.SHOT_OUT;
if (!B || !OUT || !['ja', 'en'].includes(LANG)) {
  console.error('SHOT_BASE・SHOT_OUT・SHOT_LANG（ja / en）を指定してください');
  process.exit(2);
}
fs.mkdirSync(OUT, { recursive: true });
const ja = LANG === 'ja';

// 合成データ（日英）。ID は作った順に DEMO-0001 から振られる。
const D = ja ? {
  readme: {
    title: 'README に概要を書く',
    body: 'README.md にこのリポジトリの概要を 3 行で書く。\n\n## 受け入れ条件\n\n- [ ] README.md がある\n- [ ] 概要が 3 行ある\n\n' +
      '## 検証コマンド\n\n- `test -f README.md`\n- `test "$(grep -c . README.md)" -ge 3`\n',
    plan: '方針: README.md を新しく作り、目的・使い方・ライセンスの 3 行で概要を書く',
    close: '受け入れ条件 2 件を確認した。verify は 2/2 成功（出力の全文を添付）',
  },
  req: { title: '利用者がメールアドレスでログインできる', body: 'メールアドレスとパスワードでログインし、ログアウトできる。\n\n## 受け入れ条件\n\n- [ ] 正しい組み合わせでログインできる\n- [ ] 誤った組み合わせでは理由を示して断る\n- [ ] ログアウトできる\n' },
  design: { title: 'ログインの画面と API を設計する', body: '画面の項目・API の形・セッションの持ち方を決める。', close: '判断: 設計どおりで了承。セッションは Cookie で持つ' },
  screen: {
    title: 'ログインの画面を作る', body: 'メールアドレス・パスワード・ボタンの 3 つを置いた画面を作る。\n\n## 受け入れ条件\n\n- [ ] 3 つの項目が縦に並ぶ\n- [ ] 幅 360px でも横にはみ出さない\n',
    note: '画面を作った。見た目はスクリーンショットのとおり',
    review: '判断してほしい点: ボタンの文言を 2 案用意した。A「ログイン」と B「サインイン」のどちらにするか',
    form: { h: 'ログイン', email: 'メールアドレス', pw: 'パスワード', btn: 'ログイン' },
  },
  api: { title: 'ログインの API を作る', body: 'POST /api/login を作る。誤った組み合わせは 401 で返す。', note: '方針: パスワードはハッシュで比べる。失敗の回数を数えて 5 回で一時的に止める' },
  test: { title: 'ログインの E2E テストを書く', body: '画面からログインとログアウトを通すテストを書く。' },
  bug: { title: '一覧の日付が 1 日ずれて表示される', body: '夜に作ったイシューの日付が、一覧では前の日になる。', fb: 'フィードバック: テスター A（10/1・一覧の画面で）夜に作った項目の日付が前の日になっている' },
  backlog: { title: 'パスワードを再設定できるようにする', body: '登録したメールアドレスに再設定のリンクを送る。' },
  output: 'verify-output-DEMO-0001.txt',
} : {
  readme: {
    title: 'Write an overview in README',
    body: 'Write a three-line overview of this repository in README.md.\n\n## Acceptance criteria\n\n- [ ] README.md exists\n- [ ] It has three lines of overview\n\n' +
      '## Verify commands\n\n- `test -f README.md`\n- `test "$(grep -c . README.md)" -ge 3`\n',
    plan: 'Plan: create README.md with a three-line overview — purpose, usage and license',
    close: 'Checked both acceptance criteria. verify passed 2/2 (full output attached)',
  },
  req: { title: 'Users can sign in with an email address', body: 'Users sign in with an email address and a password, and can sign out.\n\n## Acceptance criteria\n\n- [ ] A correct pair signs the user in\n- [ ] A wrong pair is refused with a reason\n- [ ] The user can sign out\n' },
  design: { title: 'Design the sign-in screen and API', body: 'Decide the fields on the screen, the shape of the API and how the session is kept.', close: 'Decision: approved as designed. The session lives in a cookie' },
  screen: {
    title: 'Build the sign-in screen', body: 'Build a screen with three parts: email address, password and a button.\n\n## Acceptance criteria\n\n- [ ] The three parts stand in one column\n- [ ] Nothing overflows at a width of 360px\n',
    note: 'Built the screen. It looks like the attached screenshot',
    review: 'Needs your decision: two labels for the button. A "Sign in" or B "Log in" — which one?',
    form: { h: 'Sign in', email: 'Email address', pw: 'Password', btn: 'Sign in' },
  },
  api: { title: 'Build the sign-in API', body: 'Add POST /api/login. A wrong pair returns 401.', note: 'Plan: compare password hashes. Count failures and pause the account after 5' },
  test: { title: 'Write an E2E test for signing in', body: 'Write a test that signs in and out through the screen.' },
  bug: { title: 'Dates in the list are off by one day', body: 'An issue created in the evening shows the previous day in the list.', fb: 'Feedback: tester A (Oct 1, list screen) items created in the evening show the previous day' },
  backlog: { title: 'Let users reset their password', body: 'Send a reset link to the registered email address.' },
  output: 'verify-output-DEMO-0001.txt',
};

const headers = { 'Accept-Language': ja ? 'ja' : 'en', 'Content-Type': 'application/json' };
async function api(method, path, body) {
  const res = await fetch(B + '/api/v1' + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${text.slice(0, 300)}`);
  return text ? JSON.parse(text) : {};
}
async function attach(id, filename, type, data) {
  const res = await fetch(`${B}/api/v1/issues/${id}/attachments`, {
    method: 'POST', body: data,
    headers: { 'Accept-Language': headers['Accept-Language'], 'Content-Type': type, 'X-Looptrack-Filename': encodeURIComponent(filename) },
  });
  if (!res.ok) throw new Error(`attach ${id}: ${res.status} ${(await res.text()).slice(0, 300)}`);
  return (await res.json()).attachment.id;
}
const create = (it, extra = {}) => api('POST', '/projects/demo/issues', { title: it.title, body: it.body, ...extra }).then(r => r.issue.id);
const comment = (id, text, attachments) => api('POST', `/issues/${id}/comments`, { text, attachments });
const status = (id, s, c = '') => api('POST', `/issues/${id}/status`, { status: s, comment: c, assignee: 'me' });

const browser = await chromium.launch({ channel: 'chrome' });
const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, locale: ja ? 'ja-JP' : 'en-US', timezoneId: 'UTC', colorScheme: 'light' });
const p = await ctx.newPage();
const shots = [];
async function shot(name, opts = {}) {
  const path = `${OUT}/${name}.png`;
  await p.screenshot({ path, ...opts });
  shots.push(`${name}.png ${fs.statSync(path).size}`);
}

// 1. 初回設定の画面（管理者が 0 人のローカルモードだけに出る）
await p.setViewportSize({ width: 1100, height: 800 });
await p.goto(B + '/');
const password = crypto.randomBytes(18).toString('base64url'); // 画面には伏せ字で出る。出力しない
await p.fill('input[name=name]', 'Alice');
await p.fill('input[name=password]', password);
await p.fill('input[name=confirm]', password);
await p.check('input[name=two_factor][value=optional]');
await p.fill('input[name=project]', 'demo');
await p.fill('input[name=project_prefix]', 'DEMO');
await p.fill('input[name=project_name]', 'Demo');
await p.locator('input[name=project_name]').blur();
await shot('first-run', { fullPage: true });

// 2. 初回設定を終えた画面（MCP の接続設定。ローカルモードはトークンを使わない）
await Promise.all([p.waitForNavigation(), p.click('button[type=submit]')]);
await shot('first-run-done', { fullPage: true });

// 3. 合成データ（AI がループを回した後の形）
const r1 = await create(D.readme, { type: 'task' });
await api('POST', '/projects/demo/next', {});
await comment(r1, D.readme.plan);
const plan = await api('GET', `/issues/${r1}/verify`);
const cmds = plan.commands;
const out = cmds.map(c => `$ ${c}\n\n[ok exit=0]\n`).join('\n');
const outID = await attach(r1, D.output, 'text/plain; charset=utf-8', Buffer.from(out));
await api('POST', `/issues/${r1}/verify`, {
  body_sha256: plan.body_sha256, host: 'dev-laptop', workspace: 'demo-app', attachments: [outID],
  results: cmds.map((c, i) => ({ command: c, status: 'ok', exit_code: 0, duration_ms: 4 + i, output_tail: '' })),
});
await status(r1, 'Done', D.readme.close);

const req = await create(D.req, { type: 'requirement', priority: 'P1' });
const design = await create(D.design, { type: 'design', traces: [req] });
await status(design, 'Done', D.design.close);
const screen = await create(D.screen, { type: 'task', traces: [req] });
const apiTask = await create(D.api, { type: 'task', traces: [req] });
await create(D.test, { type: 'test', traces: [req], blocked_by: [screen, apiTask] });
const bug = await create(D.bug, { type: 'bug', priority: 'P1' });
await comment(bug, D.bug.fb);
await create(D.backlog, { type: 'task', status: 'Backlog', priority: 'P3' });

await status(apiTask, 'In Progress');
await comment(apiTask, D.api.note);
await status(screen, 'In Progress');
// 添付する画像も、ここで描いた架空の画面
const f = D.screen.form;
const mock = await ctx.newPage();
await mock.setViewportSize({ width: 420, height: 300 });
await mock.setContent(`<body style="margin:0;font-family:system-ui,sans-serif;background:#f4f5f7;display:grid;place-items:center;height:100vh">
  <form style="background:#fff;padding:24px 28px;border-radius:10px;box-shadow:0 1px 4px #0002;width:300px">
  <h2 style="margin:0 0 14px;font-size:20px">${f.h}</h2>
  <label style="font-size:13px;color:#555">${f.email}<input style="display:block;width:100%;box-sizing:border-box;margin:4px 0 10px;padding:8px"></label>
  <label style="font-size:13px;color:#555">${f.pw}<input type="password" style="display:block;width:100%;box-sizing:border-box;margin:4px 0 14px;padding:8px" value="xxxxxxxxxxxx"></label>
  <button type="button" style="width:100%;padding:9px;border:0;border-radius:6px;background:#2f6fde;color:#fff;font-size:14px">${f.btn}</button>
  </form></body>`);
const png = await mock.screenshot();
await mock.close();
const imgID = await attach(screen, 'login-screen.png', 'image/png', png);
await comment(screen, D.screen.note, [imgID]);
await status(screen, 'In Review', D.screen.review);

// 4. プロジェクトの選択
await p.setViewportSize({ width: 1280, height: 440 });
await p.goto(B + '/');
await p.waitForTimeout(300);
await shot('hub');

// 5. ボード（状態の列が 6 つとも収まる幅）
await p.setViewportSize({ width: 1760, height: 480 });
await p.goto(`${B}/p/demo/`);
await p.waitForSelector(`text=${D.backlog.title}`);
await p.waitForTimeout(500);
await shot('board');

// 6. トレース（要件から下位の作業をたどる）
await p.setViewportSize({ width: 1280, height: 360 });
await p.click('button[data-view=matrix]');
await p.waitForTimeout(600);
await shot('trace');
await p.click('button[data-view=board]');
await p.waitForTimeout(300);

// 7. 1 周を終えたイシュー（コメント・検証の結果・添付）
async function drawer(id, name, height) {
  await p.setViewportSize({ width: 1280, height });
  await p.goto('about:blank'); // 同じページの # だけを変えても読み直さないので、いったん離れる
  await p.goto(`${B}/p/demo/#${id}`);
  await p.waitForTimeout(900);
  await shot(name);
}
await drawer(r1, 'issue-done', 1110);

// 8. 人の判断待ち（In Review）と画像の添付
await drawer(screen, 'in-review', 1290);

await browser.close();
console.log(`${LANG}: ${shots.length} 枚\n` + shots.join('\n'));
