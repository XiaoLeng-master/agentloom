import { createApp, ref, computed } from 'vue'
import './style.css'

const headers = { Authorization: 'Bearer local-demo' }
async function request(path, options = {}) {
  const response = await fetch(path, { ...options, headers: { ...headers, ...(options.headers || {}) } })
  const body = await response.json()
  if (!response.ok) throw new Error(body.error || body.decision?.Reason || `HTTP ${response.status}`)
  return body
}

createApp({
  setup() {
    const tab = ref('chat')
    const message = ref('Planner、Executor 和 Verifier 分别负责什么？')
    const busy = ref(false)
    const error = ref('')
    const reply = ref(null)
    const trace = ref(null)
    const experiments = ref([])
    const selected = ref(null)
    const hitPercent = value => `${((value || 0) * 100).toFixed(1)}%`
    const resolved = computed(() => selected.value?.failures?.filter(f => f.resolved_by_candidate).length || 0)

    async function send() {
      if (!message.value.trim()) return
      busy.value = true; error.value = ''; trace.value = null
      try {
        reply.value = await request('/v1/chat', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ message: message.value }) })
        trace.value = await request(`/v1/traces/${reply.value.trace_id}`)
      } catch (e) { error.value = e.message } finally { busy.value = false }
    }
    async function loadExperiments() {
      busy.value = true; error.value = ''
      try {
        experiments.value = await request('/v1/evolutions')
        selected.value = experiments.value[0] || null
      } catch (e) { error.value = e.message } finally { busy.value = false }
    }
    async function runExperiment() {
      busy.value = true; error.value = ''
      try {
        selected.value = await request('/v1/evolve', { method: 'POST' })
        await loadExperiments()
      } catch (e) { error.value = e.message; busy.value = false }
    }
    function switchTab(next) {
      tab.value = next; error.value = ''
      if (next === 'benchmark') loadExperiments()
    }
    return { tab, message, busy, error, reply, trace, experiments, selected, resolved, hitPercent, send, runExperiment, switchTab }
  },
  template: `
    <div class="shell">
      <header><div><div class="eyebrow">AGENTLOOM / LOCAL WORKBENCH</div><h1>让每次执行，都有证据可查。</h1><p>聊天、执行轨迹与检索复测，共用同一个 Go 服务。</p></div><span class="badge">本地演示</span></header>
      <nav aria-label="工作台页面"><button v-for="item in [['chat','Chat'],['trace','Trace'],['benchmark','Benchmark']]" :key="item[0]" :class="{active:tab===item[0]}" @click="switchTab(item[0])">{{item[1]}}</button></nav>
      <div v-if="error" class="error" role="alert">{{error}}</div>
      <main v-if="tab==='chat'" class="grid">
        <section class="card"><h2>发起一次请求</h2><p>问题由执行器处理；回答依据可在 Trace 查看。</p><textarea v-model="message" rows="6" aria-label="问题"></textarea><button class="primary" :disabled="busy || !message.trim()" @click="send">{{busy?'运行中…':'发送问题'}}</button></section>
        <section class="card"><h2>回答</h2><div v-if="reply" class="answer"><div class="meta"><span>{{reply.mode}}</span><span>Trace {{reply.trace_id}}</span></div><p>{{reply.reply}}</p><button @click="switchTab('trace')">查看执行轨迹 →</button></div><p v-else class="muted">发送问题后显示实际回答。</p></section>
      </main>
      <main v-else-if="tab==='trace'"><section class="card"><h2>执行轨迹</h2><p v-if="!trace" class="muted">请先在 Chat 发送问题。</p><template v-else><div class="meta"><span>配置 v{{trace.config_version || '—'}}</span><span>切分 {{trace.chunk_strategy || '—'}}</span><span>{{trace.id}}</span></div><div class="flow"><article v-for="(step,index) in trace.contracts" :key="index"><b>{{step.phase}}</b><span>{{step.from}} → {{step.to}}</span><small>{{step.task_id}}</small><pre>{{step.payload}}</pre></article></div><h3>实际检索依据</h3><p v-if="!trace.retrievals?.length" class="muted">本次没有检索记录。</p><article class="source" v-for="(item,index) in trace.retrievals" :key="index"><div class="meta"><b>{{item.source}}</b><span>score {{item.score.toFixed(3)}}</span></div><small>query: {{item.query}}</small><pre>{{item.text}}</pre></article></template></section></main>
      <main v-else><section class="card"><div class="row"><div><h2>检索复测</h2><p>同一数据与语料快照，对比 fixed 和 recursive 的 Top-3 来源命中。</p></div><button class="primary" :disabled="busy" @click="runExperiment">{{busy?'运行中…':'运行一次复测'}}</button></div><p class="note">这是检索指标；回答质量与安全性当前未评测。重启服务会重置生效配置，历史实验仍可读取。</p><p v-if="!experiments.length" class="muted">暂无保存的实验。</p><div v-else class="row"><label>历史实验 <select v-model="selected"><option v-for="item in experiments" :key="item.id" :value="item">{{new Date(item.created_at).toLocaleString()}} · {{item.id.slice(0,8)}}</option></select></label></div><template v-if="selected"><div class="metrics"><div><small>优化前 · {{selected.before.strategy}}</small><strong>{{hitPercent(selected.before.hit_rate)}}</strong></div><div><small>优化后 · {{selected.after.strategy}}</small><strong>{{hitPercent(selected.after.hit_rate)}}</strong></div><div><small>变化 · {{selected.before.cases}} 条</small><strong>{{hitPercent(selected.actual_delta)}}</strong></div></div><div class="bar"><span :style="{width:hitPercent(selected.before.hit_rate)}"></span><span :style="{width:hitPercent(selected.after.hit_rate)}"></span></div><div class="meta"><span>数据哈希 {{selected.before.dataset_hash?.slice(0,16)}}…</span><span>门禁 {{selected.decision.Passed?'通过':'未通过'}}</span><span>预测 {{selected.decision.PredictionMatched?'匹配':'未匹配'}}</span><span>配置 v{{selected.decision.Version || '—'}}</span></div><h3>失败样例 <small>候选修复 {{resolved}} / {{selected.failures?.length || 0}}</small></h3><div class="cases"><details v-for="item in selected.failures" :key="item.case_id"><summary>{{item.case_id}} · {{item.resolved_by_candidate?'候选已命中':'仍未命中'}}</summary><p>预期来源：{{item.expected_source}}</p><p>实际来源：{{item.retrieved_sources?.join('、') || '无'}}</p><p>归因：{{item.cause}}</p></details></div></template></section></main>
      <footer>可复现检索实验 · 本地演示数据 · 不将检索命中视作答案正确</footer>
    </div>`
}).mount('#app')
