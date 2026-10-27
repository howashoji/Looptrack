<script setup>
// 各版の英日の先頭のページ（README.md から作るページ）の上部に、公開リポジトリの Social Preview と同じ画像を出す。
// ガイドの中身（各版のタグの Markdown）には手を入れずに、どの版にも同じ形で出すため、テーマの側で差し込む。
import { computed } from 'vue'
import { useData } from 'vitepress'

const { theme, page, lang } = useData()
const conf = computed(() => theme.value.looptrack)
const isTop = computed(() => page.value.relativePath === 'index.md' || page.value.relativePath === 'ja/index.md')
const alt = computed(() => (lang.value === 'ja' ? conf.value.imageAlt.ja : conf.value.imageAlt.en))
</script>

<template>
  <div v-if="isTop" class="lt-hero">
    <img
      :src="conf.brand.socialPreview.src"
      :width="conf.brand.socialPreview.width"
      :height="conf.brand.socialPreview.height"
      :alt="alt"
      decoding="async"
    />
  </div>
</template>
