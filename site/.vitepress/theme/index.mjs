// 既定のテーマに、版を選ぶ欄と mermaid の図を描く部品を足す。
// フォント（Inter）を同梱しない版のテーマを使う。同梱すると、包みのライセンスのファイルに無いフォントのライセンスを
// 別に配る必要が出る。日本語のページはもともと端末のフォントで描くので、英語のページも端末のフォントにそろえる。
import DefaultTheme from 'vitepress/theme-without-fonts'
import { h } from 'vue'
import VersionSelect from './VersionSelect.vue'
import MermaidDiagram from './MermaidDiagram.vue'
import './style.css'

export default {
  extends: DefaultTheme,
  enhanceApp({ app }) {
    app.component('MermaidDiagram', MermaidDiagram)
  },
  Layout() {
    return h(DefaultTheme.Layout, null, {
      'nav-bar-content-before': () => h(VersionSelect),
    })
  },
}
