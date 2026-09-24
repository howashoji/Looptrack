// data-copy="<要素の id>" のボタンで、その要素の文字列をクリップボードにコピーする（初回設定の最後の画面）。
// 文面はサーバが <script type="application/json" id="i18n"> で返す（JS には文面を持たない）。
function text(key) {
  try {
    const el = document.getElementById("i18n");
    return el ? (JSON.parse(el.textContent)[key] || "") : "";
  } catch (err) {
    return "";
  }
}

document.addEventListener("click", e => {
  const b = e.target.closest("[data-copy]");
  if (!b) return;
  const el = document.getElementById(b.dataset.copy);
  if (!el || !navigator.clipboard) return;
  navigator.clipboard.writeText(el.textContent).then(() => {
    const t = b.textContent;
    b.textContent = text("copied");
    setTimeout(() => { b.textContent = t; }, 1500);
  });
});
