<script setup>
// 版を選ぶ欄。版ごとに別の VitePress のサイトなので、版をまたぐ移動はページの読み込み直しで行う。
// 移った先にそのページが無ければ、サイトの直下の 404.html がその版の先頭へ案内する。
import { computed } from 'vue'
import { useData } from 'vitepress'

const { theme, lang } = useData()
const conf = computed(() => theme.value.looptrack)
const label = computed(() => (lang.value === 'ja' ? '版' : 'Version'))

function go(event) {
  const target = event.target.value
  const { siteBase, current } = conf.value
  if (target === current) return
  const prefix = `${siteBase}${current}/`
  const rest = location.pathname.startsWith(prefix) ? location.pathname.slice(prefix.length) : ''
  location.href = `${siteBase}${target}/${rest}${location.search}${location.hash}`
}
</script>

<template>
  <div class="lt-version">
    <select :aria-label="label" :title="label" :value="conf.current" @change="go">
      <option v-for="v in conf.versions" :key="v" :value="v" :selected="v === conf.current">{{ v }}</option>
    </select>
  </div>
</template>
