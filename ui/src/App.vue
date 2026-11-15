<template>
  <div class="page">
    <header class="topbar">
      <div class="topbar-inner">
        <el-icon :size="22" class="brand-logo"><Collection /></el-icon>
        <div class="brand-text">
          <div class="brand-title">collect-kit 采集向导<template v-if="meta"> · {{ meta.recipe.title || meta.recipe.name }}</template></div>
          <div v-if="meta" class="subline mono">
            recipe={{ meta.recipe.name }} ｜ 仓库 {{ meta.scan.repoRoot }} ｜ 业务模块 {{ meta.scan.modules.length }} 个 ｜ 基座版本 {{ meta.scan.baseVersion }}
          </div>
          <div v-else class="subline">加载中……</div>
        </div>
        <div class="topbar-actions">
          <el-tooltip :content="dark ? '切换浅色' : '切换深色'" placement="bottom">
            <el-button circle @click="onToggleTheme">
              <el-icon><Sunny v-if="dark" /><Moon v-else /></el-icon>
            </el-button>
          </el-tooltip>
        </div>
      </div>
    </header>

    <main v-loading="loading" class="main" element-loading-text="加载向导数据……">
      <el-alert v-if="banner.type" :type="banner.type" :title="banner.text" show-icon :closable="false" class="banner" />

      <el-card v-if="recipes.length > 1" shadow="never" class="page-card">
        <template #header>
          <div class="card-head">
            <span class="card-title"><el-icon><Collection /></el-icon>采集方案（recipe）</span>
          </div>
        </template>
        <el-select v-model="currentRecipe" :loading="switching" style="width: 380px; max-width: 100%" @change="switchRecipe">
          <el-option v-for="c in recipes" :key="c.name" :value="c.name" :label="(c.title || c.name) + '（' + c.name + '）'" />
        </el-select>
        <div v-if="recipeDesc" class="desc sub">{{ recipeDesc }}</div>
      </el-card>

      <el-card shadow="never" class="page-card">
        <template #header>
          <div class="card-head">
            <span class="card-title"><el-icon><Operation /></el-icon>动作</span>
          </div>
        </template>
        <el-radio-group v-model="form.action">
          <el-radio-button value="collect">构建</el-radio-button>
          <el-radio-button v-if="actions.pushImages" value="push-images">镜像备料</el-radio-button>
          <el-radio-button v-if="actions.repush" value="repush">重推既有制品</el-radio-button>
        </el-radio-group>
        <div class="desc sub">{{ actionDesc }}</div>
      </el-card>

      <el-card v-if="params.length" shadow="never" class="page-card">
        <template #header>
          <div class="card-head">
            <span class="card-title"><el-icon><Document /></el-icon>参数</span>
            <span class="card-sub">按 recipe 声明动态渲染</span>
          </div>
        </template>
        <div class="params-grid">
          <div v-for="p in paramsMain" :key="p.name" class="param-item">
            <div class="param-label">{{ p.label || p.name }}</div>
            <el-select v-if="p.kind === 'choice'" v-model="form.params[p.name]" style="width: 100%">
              <el-option v-for="c in p.choices || []" :key="c.value" :value="c.value" :label="c.label || c.value" />
            </el-select>
            <el-switch
              v-else-if="p.kind === 'bool'"
              v-model="form.params[p.name]"
              active-value="true"
              inactive-value="false"
            />
            <el-input v-else v-model="form.params[p.name]" :placeholder="p.name === 'name' ? 'tenant-a' : ''" />
          </div>
        </div>
        <template v-if="paramsExtra.length">
          <el-divider class="divider">高级</el-divider>
          <div class="params-grid">
            <div v-for="p in paramsExtra" :key="p.name" class="param-item">
              <div class="param-label">{{ p.label || p.name }}</div>
              <el-input v-model="form.params[p.name]" :placeholder="p.name === 'name' ? 'tenant-a' : ''" />
            </div>
          </div>
        </template>
      </el-card>

      <el-card v-if="modules.length" shadow="never" class="page-card">
        <template #header>
          <div class="card-head">
            <span class="card-title"><el-icon><Grid /></el-icon>业务模块</span>
            <span class="card-sub">自动扫描，勾选参与交付</span>
            <span class="spacer" />
            <el-tag size="small" type="primary">{{ form.modules.length }} / {{ modules.length }}</el-tag>
            <el-button link type="primary" @click="setAllModules(true)">全选</el-button>
            <el-button link @click="setAllModules(false)">清空</el-button>
          </div>
        </template>
        <el-checkbox-group v-model="form.modules" class="check-grid">
          <el-checkbox v-for="m in modules" :key="m.short" :value="m.short" border>
            <span class="check-label"><b>{{ m.short }}</b><span class="opt-sub">{{ m.label }}</span></span>
          </el-checkbox>
        </el-checkbox-group>
      </el-card>

      <el-card v-if="middleware.length" shadow="never" class="page-card">
        <template #header>
          <div class="card-head">
            <span class="card-title"><el-icon><Cpu /></el-icon>中间件</span>
            <span class="card-sub">打包编排勾选 + 镜像备料清单</span>
            <span class="spacer" />
            <el-tag size="small" type="primary">{{ form.middleware.length }} / {{ middleware.length }}</el-tag>
            <el-button link type="primary" @click="setAllMw(true)">全选</el-button>
            <el-button link @click="setAllMw(false)">清空</el-button>
          </div>
        </template>
        <el-checkbox-group v-model="form.middleware" class="check-grid">
          <el-checkbox v-for="m in middleware" :key="m.code" :value="m.code" border>
            <span class="check-label"><b>{{ m.code }}</b><span class="opt-sub">{{ m.label }}</span></span>
          </el-checkbox>
        </el-checkbox-group>
      </el-card>

      <el-card shadow="never" class="page-card">
        <template #header>
          <div class="card-head">
            <span class="card-title"><el-icon><Upload /></el-icon>deliver-studio 打包工作台</span>
            <span class="card-sub">构建后动作与登录凭据</span>
          </div>
        </template>
        <el-radio-group v-model="form.uploadMode">
          <el-radio-button value="none">仅本地构建（不推送）</el-radio-button>
          <el-radio-button value="default">默认工作台 127.0.0.1:7860</el-radio-button>
          <el-radio-button value="custom">自定义地址</el-radio-button>
        </el-radio-group>
        <div v-if="form.uploadMode === 'custom'" class="params-grid" style="margin-top: 10px">
          <div class="param-item">
            <div class="param-label">工作台地址</div>
            <el-input v-model="form.uploadUrl" placeholder="http://<host>:7860" style="max-width: 360px" />
          </div>
        </div>
        <div class="params-grid studio-row">
          <div class="param-item">
            <div class="param-label">账号</div>
            <el-input v-model="form.studioUser" style="width: 140px" />
          </div>
          <div class="param-item">
            <div class="param-label">密码（推送必填，见工作台启动控制台）</div>
            <el-input v-model="form.studioPass" show-password style="width: 200px" />
          </div>
          <div class="param-item">
            <div class="param-label">推送后一键打包（出运行时套件 zip）</div>
            <el-switch v-model="form.package" />
          </div>
        </div>
        <div v-if="form.action === 'push-images'" class="params-grid">
          <div class="param-item">
            <div class="param-label">附加镜像（仅镜像备料动作；逗号分隔）</div>
            <el-input v-model="form.extraImages" placeholder="registry/app:tag,registry/other:tag" style="max-width: 420px" />
          </div>
        </div>
      </el-card>

      <el-card shadow="never" class="page-card">
        <template #header>
          <div class="card-head">
            <span class="card-title"><el-icon><VideoPlay /></el-icon>执行</span>
            <el-tag :type="stateMeta.type" size="small">
              <el-icon v-if="run.state === 'running'" class="is-loading"><Loading /></el-icon>
              {{ stateMeta.label }}
            </el-tag>
            <span class="spacer" />
            <el-button link type="primary" @click="doPreview">预览命令</el-button>
          </div>
        </template>
        <div v-if="cmd" class="cmd-box mono">
          <span class="cmd-text">{{ cmd }}</span>
          <el-button link class="cmd-copy" @click="copyCmd">
            <el-icon><CopyDocument /></el-icon>复制
          </el-button>
        </div>
        <div class="run-actions">
          <el-button type="primary" :loading="run.state === 'running'" :disabled="run.state === 'running'" @click="doStart">
            <el-icon v-if="run.state !== 'running'" style="margin-right: 4px"><VideoPlay /></el-icon>开始
          </el-button>
          <el-button
            v-if="run.state === 'done' && run.zipExists"
            tag="a"
            href="/api/download"
            type="success"
            plain
          >
            <el-icon style="margin-right: 4px"><Download /></el-icon>下载套件包
          </el-button>
        </div>
        <pre ref="logBox" class="log mono">{{ run.log || '（尚未开始）' }}</pre>
        <div v-if="run.zipPath && run.state === 'done'" class="sub mono zip-line">套件包：{{ run.zipPath }}</div>
      </el-card>
    </main>
  </div>
</template>

<script setup>
import { reactive, ref, computed, onMounted, onBeforeUnmount, nextTick, watch } from 'vue'
import { ElMessage } from 'element-plus'
import * as api from './api'
import { currentDark, toggleTheme } from './theme'

const loading = ref(true)
const meta = ref(null) // /api/meta 响应（首载与切换共用 applyMeta 全量路径）
const currentRecipe = ref('')
const switching = ref(false)
const dark = ref(currentDark())
const cmd = ref('')
const banner = reactive({ type: '', text: '' }) // '' | 'success' | 'error'
const logBox = ref(null)
let pollTimer = null

const form = reactive({
  action: 'collect',
  params: {}, // name -> string（bool 也存字符串 'true'/'false'，与表单合同一致）
  modules: [],
  middleware: [],
  extraImages: '',
  uploadMode: 'none',
  uploadUrl: '',
  package: true,
  studioUser: 'admin',
  studioPass: '',
})

const run = reactive({ state: 'idle', exitCode: 0, log: '', zipPath: '', zipExists: false })

const recipes = computed(() => (meta.value && meta.value.recipes) || [])
const actions = computed(() => (meta.value && meta.value.recipe && meta.value.recipe.actions) || {})
const recipeDesc = computed(() => (meta.value && meta.value.recipe && meta.value.recipe.description) || '')
// modules/middleware 两类参数由勾选卡承载，不在参数表单重复渲染
const params = computed(() => ((meta.value && meta.value.recipe && meta.value.recipe.params) || []).filter(p => p.kind !== 'modules' && p.kind !== 'middleware'))
const paramsMain = computed(() => params.value.filter(p => !(p.kind === 'out' || (p.kind === 'text' && p.name === 'baseVersion'))))
const paramsExtra = computed(() => params.value.filter(p => p.kind === 'out' || (p.kind === 'text' && p.name === 'baseVersion')))
const modules = computed(() => (meta.value && meta.value.scan && meta.value.scan.modules) || [])
const middleware = computed(() => (meta.value && meta.value.recipe && meta.value.recipe.middleware) || [])

const actionDesc = computed(() => ({
  collect: '源码 → 制品 → 推送 → 可选一键打包',
  'push-images': '第三方镜像入仓，不构建',
  repush: '重推既有制品（推送段失败免重建重试）',
}[form.action] || ''))

const stateMeta = computed(() => ({
  idle: { label: '空闲', type: 'info' },
  running: { label: '运行中', type: 'warning' },
  done: { label: '完成', type: 'success' },
  failed: { label: '失败', type: 'danger' },
}[run.state] || { label: run.state, type: 'info' }))

// applyMeta 按 meta 全量复位（首载与 recipe 切换同路径，防半换装残留）
function applyMeta(m) {
  meta.value = m
  const r = (m && m.recipe) || {}
  const params = {}
  for (const p of r.params || []) {
    if (p.kind === 'modules' || p.kind === 'middleware') continue
    params[p.name] = p.defaultFromScan === 'baseVersion'
      ? ((m.scan && m.scan.baseVersion) || p.default || '')
      : (p.default || '')
  }
  form.params = params
  form.modules = []
  form.middleware = []
  const acts = r.actions || {}
  if ((form.action === 'push-images' && !acts.pushImages) || (form.action === 'repush' && !acts.repush)) {
    form.action = 'collect'
  }
  currentRecipe.value = r.name || ''
  resetRunPanel()
}

// resetRunPanel 切换 recipe / 重渲染时执行面板归零（服务端任务态已同步重置）
function resetRunPanel() {
  stopPoll()
  cmd.value = ''
  banner.type = ''
  banner.text = ''
  Object.assign(run, { state: 'idle', exitCode: 0, log: '', zipPath: '', zipExists: false })
}

async function switchRecipe() {
  const target = currentRecipe.value
  banner.type = ''
  banner.text = ''
  switching.value = true
  try {
    applyMeta(await api.selectRecipe(target)) // select 响应与 /api/meta 同构
  } catch (e) {
    ElMessage.error(e.message)
    currentRecipe.value = (meta.value && meta.value.recipe && meta.value.recipe.name) || ''
  } finally {
    switching.value = false
  }
}

function setAllModules(on) {
  form.modules = on ? modules.value.map(m => m.short) : []
}

function setAllMw(on) {
  form.middleware = on ? middleware.value.map(m => m.code) : []
}

// formBody 与后端 wizardForm 合同一致：params 值全部字符串、空值剔除
function formBody() {
  const params = {}
  for (const [k, v] of Object.entries(form.params)) {
    if (typeof v === 'string' && v !== '') params[k] = v
  }
  return {
    action: form.action,
    params,
    modules: [...form.modules],
    middleware: [...form.middleware],
    extraImages: form.extraImages,
    uploadMode: form.uploadMode,
    uploadUrl: form.uploadUrl,
    package: form.package,
    studioUser: form.studioUser,
    studioPass: form.studioPass,
  }
}

function showOk(text) {
  banner.type = 'success'
  banner.text = text
}

function showError(text) {
  banner.type = 'error'
  banner.text = text
}

async function doPreview() {
  banner.type = ''
  banner.text = ''
  try {
    const data = await api.preview(formBody())
    cmd.value = data.command || ''
  } catch (e) {
    cmd.value = '✘ ' + e.message
    showError(e.message)
  }
}

async function doStart() {
  banner.type = ''
  banner.text = ''
  try {
    const data = await api.start(formBody())
    if (data.command) cmd.value = data.command
    startPoll()
  } catch (e) {
    showError(e.message)
  }
}

function startPoll() {
  stopPoll()
  const tick = async () => {
    try {
      const st = await api.getStatus()
      Object.assign(run, {
        state: st.state || 'idle',
        exitCode: st.exitCode || 0,
        log: st.log || '（无输出）',
        zipPath: st.zipPath || '',
        zipExists: !!st.zipExists,
      })
      if (st.state !== 'running') {
        stopPoll()
        if (st.state === 'done') showOk('完成 ✔' + (st.zipPath ? '，套件包：' + st.zipPath : ''))
        else if (st.state === 'failed') showError('失败 ✘（exit=' + st.exitCode + '，见日志末尾）')
      }
    } catch { /* 网络闪断：下轮轮询自愈 */ }
  }
  tick()
  pollTimer = setInterval(tick, 1500)
}

function stopPoll() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

// 剪贴板复制：clipboard API 优先 + execCommand 兜底（http 非 localhost 环境无 navigator.clipboard）
async function copyCmd() {
  const text = cmd.value
  if (!text) return
  try {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(text)
    } else {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      ta.remove()
    }
    ElMessage.success('已复制')
  } catch {
    ElMessage.error('复制失败，请手动选择复制')
  }
}

function onToggleTheme() {
  toggleTheme()
  dark.value = currentDark()
}

// 日志自动滚底（新输出到达时保持视口贴尾）
watch(() => run.log, async () => {
  await nextTick()
  if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
})

onMounted(async () => {
  try {
    applyMeta(await api.getMeta())
    // 刷新页面时恢复服务端任务现场（提示「刷新页面可继续查看日志」）：在跑则续看轮询，已出终态则回放结果
    const st = await api.getStatus()
    if (st.state === 'running') {
      startPoll()
    } else if ((st.state === 'done' || st.state === 'failed') && st.log) {
      Object.assign(run, {
        state: st.state,
        exitCode: st.exitCode || 0,
        log: st.log,
        zipPath: st.zipPath || '',
        zipExists: !!st.zipExists,
      })
      if (st.command) cmd.value = st.command
      if (st.state === 'done') showOk('完成 ✔' + (st.zipPath ? '，套件包：' + st.zipPath : ''))
      else showError('失败 ✘（exit=' + st.exitCode + '，见日志末尾）')
    }
  } catch (e) {
    showError(e.message)
  } finally {
    loading.value = false
  }
})

onBeforeUnmount(stopPoll)
</script>

<style scoped>
.page {
  min-height: 100vh;
  background: var(--el-fill-color-lighter);
}

.topbar {
  position: sticky;
  top: 0;
  z-index: 10;
  background: var(--el-bg-color);
  border-bottom: 1px solid var(--el-border-color-light);
}

.topbar-inner {
  max-width: 980px;
  margin: 0 auto;
  padding: 10px 16px;
  display: flex;
  align-items: center;
  gap: 12px;
}

.brand-logo {
  color: var(--el-color-primary);
  flex: none;
}

.brand-title {
  font-size: 16px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.subline {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  word-break: break-all;
  margin-top: 2px;
}

.topbar-actions {
  margin-left: auto;
  flex: none;
}

.main {
  max-width: 980px;
  margin: 14px auto 24px;
  padding: 0 16px;
  min-height: 200px;
}

.banner {
  margin-bottom: 10px;
}

.page-card + .page-card {
  margin-top: 10px;
}

.card-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.card-title {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 14px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.card-sub {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.spacer {
  flex: 1;
}

.desc {
  margin-top: 6px;
  font-size: 12px;
}

.params-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 10px 18px;
}

.param-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.param-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.studio-row {
  margin-top: 10px;
  grid-template-columns: none;
  grid-auto-flow: column;
  justify-content: start;
  align-items: end;
  gap: 18px;
  flex-wrap: wrap;
}

.divider {
  margin: 14px 0 12px;
}

.check-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(250px, 1fr));
  gap: 6px 10px;
}

.check-grid :deep(.el-checkbox) {
  margin-right: 0;
  height: auto;
  padding: 6px 12px;
  align-items: center;
}

.check-grid :deep(.el-checkbox.is-bordered) {
  border-radius: 6px;
}

.check-label {
  display: inline-flex;
  align-items: baseline;
  gap: 8px;
  min-width: 0;
}

.check-label b {
  color: var(--el-text-color-primary);
  font-weight: 600;
}

.opt-sub {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cmd-box {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  background: var(--el-fill-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  padding: 10px 12px;
  font-size: 12px;
  line-height: 1.6;
}

.cmd-text {
  flex: 1;
  white-space: pre-wrap;
  word-break: break-all;
  color: var(--el-text-color-regular);
}

.cmd-copy {
  flex: none;
  margin-left: auto;
}

.run-actions {
  margin: 10px 0;
  display: flex;
  gap: 10px;
  align-items: center;
}

.log {
  background: var(--el-fill-color-darker);
  color: var(--el-text-color-regular);
  border-radius: 6px;
  padding: 10px 12px;
  font-size: 12px;
  line-height: 1.6;
  overflow: auto;
  max-height: 420px;
  white-space: pre-wrap;
  word-break: break-all;
  margin: 0;
}

.zip-line {
  margin-top: 8px;
  word-break: break-all;
}

.mono {
  font-family: Consolas, Monaco, 'Courier New', monospace;
}
</style>
