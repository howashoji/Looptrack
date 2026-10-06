<script setup>
// mermaid の図を画面の側で描く。mermaid は大きいので、図のあるページを開いたときだけ読み込む。
// 描けるまで（スクリプトが動かないときも）は元のコードを出しておく。
import { onMounted, ref, watch } from 'vue'
import { useData } from 'vitepress'

const props = defineProps({ code: { type: String, required: true } })
const source = decodeURIComponent(props.code)
const svg = ref('')
const { isDark } = useData()
let seq = 0

async function draw() {
  const mine = ++seq
  const { default: mermaid } = await import('mermaid')
  mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: isDark.value ? 'dark' : 'default' })
  const id = `lt-mermaid-${Math.random().toString(36).slice(2)}`
  try {
    const { svg: out } = await mermaid.render(id, source)
    // 表示の色を続けて切り替えたときに、古い描画で上書きしないように。
    if (mine === seq) svg.value = out
  } catch (err) {
    console.error('mermaid の図を描けませんでした', err)
  }
}

onMounted(draw)
watch(isDark, draw)
</script>

<template>
  <div v-if="svg" class="lt-mermaid" v-html="svg"></div>
  <div v-else class="language-mermaid"><pre><code>{{ source }}</code></pre></div>
</template>
