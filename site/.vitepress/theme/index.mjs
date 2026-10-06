// 既定のテーマに、版を選ぶ欄・mermaid の図を描く部品・ヘッダのアイコン・先頭のページの画像を足す。
// フォント（Inter）を同梱しない版のテーマを使う。同梱すると、包みのライセンスのファイルに無いフォントのライセンスを
// 別に配る必要が出る。日本語のページはもともと端末のフォントで描くので、英語のページも端末のフォントにそろえる。
import DefaultTheme from 'vitepress/theme-without-fonts'
import { h } from 'vue'
import { useData } from 'vitepress'
import VersionSelect from './VersionSelect.vue'
import MermaidDiagram from './MermaidDiagram.vue'
import SocialPreview from './SocialPreview.vue'
import './style.css'

const IconImage = {
  setup() {
    const { theme } = useData()
    return () => h('img', { class: 'lt-logo', src: theme.value.looptrack.brand.icon, width: 256, height: 256, alt: '' })
  },
}

export default {
  extends: DefaultTheme,
  enhanceApp({ app }) {
    app.component('MermaidDiagram', MermaidDiagram)
  },
  Layout() {
    return h(DefaultTheme.Layout, null, {
      // アイコンは全版で共有する 1 枚を土台の直下から読む（themeConfig.logo だと版の base が前に付く）。
      // 名前はすぐ横に文字で出ているので、画像は飾りとして読み上げさせない。
      'nav-bar-title-before': () => h(IconImage),
      'nav-bar-content-before': () => h(VersionSelect),
      'doc-before': () => h(SocialPreview),
    })
  },
}
