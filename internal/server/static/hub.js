// プロジェクト選択画面。データは /im/api/v1/projects（参加しているプロジェクトだけ。管理者も同じ）から取り、4 秒ごとに見直す。
// 値は必ず esc / attr を通して埋める（現行ビューアにあった属性値の引用符の扱いを直した）。
import { esc, attr, paint, loadTexts, fill } from "./render.js";

const BASE = document.body.dataset.base;
// この画面の文面（サーバが <script type="application/json" id="i18n"> で返す。JS には文面を持たない）。
const TEXT = loadTexts();
const t = (key, kv) => fill(TEXT[key] || "", kv);
const STATUS_COLOR = {
  "Backlog": "#8b8f97", "Todo": "#5c9bd6", "In Progress": "#e0a52b",
  "In Review": "#a26cd6", "Done": "#1f9254", "Canceled": "#8b8f97"
};
const ORDER = ["Backlog", "Todo", "In Progress", "In Review", "Done", "Canceled"];

function num(n) { return String(Number(n) || 0); }

function stat(cls, n, label) {
  return '<div class="' + attr(cls) + '"><b>' + num(n) + "</b>" + esc(label) + "</div>";
}
function card(p) {
  const total = p.counts.total || 1;
  const bar = ORDER.map(s => {
    const n = p.counts.by_status[s] || 0;
    if (!n) return "";
    return '<i data-w="' + (n * 100 / total) + '" data-color="' + attr(STATUS_COLOR[s] || "#8b8f97")
      + '" title="' + attr(s + ": " + n) + '"></i>';
  }).join("");
  return '<a class="card" href="' + attr(BASE + "/p/" + encodeURIComponent(p.slug) + "/") + '">'
    + '<div class="ctop"><span class="cname">' + esc(p.name) + "</span>"
    + '<span class="cpre">' + esc(p.prefix) + "-nnnn</span></div>"
    + (p.description ? '<p class="cdesc">' + esc(p.description) + "</p>" : "")
    + '<div class="bar">' + bar + "</div>"
    + '<div class="stats">'
    + stat("", p.counts.open, t("open"))
    + stat("warn", p.counts.in_progress, t("in_progress"))
    + stat("ok", p.counts.ready, t("ready"))
    + stat("bug", p.counts.open_bugs, t("bugs"))
    + "</div>"
    + '<div class="cfoot"><span>' + esc(t("foot", { closed: num(p.counts.closed), updated: p.counts.updated || "—" }))
    + '</span><span class="go">' + esc(t("go")) + "</span></div>"
    + "</a>";
}
function render(d) {
  const g = document.getElementById("grid");
  const projects = d.projects || [];
  document.getElementById("sub").textContent = t("sub", {
    n: projects.length,
    open: projects.reduce((a, p) => a + p.counts.open, 0),
    at: d.generated || ""
  });
  g.innerHTML = projects.length
    ? projects.map(card).join("")
    : document.body.dataset.admin
      ? '<div class="empty">' + esc(t("empty_admin_1"))
        + '<a href="' + attr(BASE + "/admin/projects") + '">' + esc(t("admin_projects")) + "</a>" + esc(t("empty_admin_2"))
        + '<a href="' + attr(BASE + "/admin/projects#new") + '">' + esc(t("new_project")) + "</a>" + esc(t("empty_admin_3")) + "</div>"
      : '<div class="empty">' + esc(t("empty")) + "</div>";
  paint(g);
}
function load() {
  return fetch(BASE + "/api/v1/projects", { cache: "no-store", credentials: "same-origin" })
    .then(r => { if (r.status === 401) { location.href = BASE + "/login"; throw new Error("unauthorized"); } return r.json(); })
    .then(render).catch(() => {});
}
load();
setInterval(load, 4000);
