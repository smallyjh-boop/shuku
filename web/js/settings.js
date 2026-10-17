import { h, clear, fill, add, icon, toast, toastError, confirmDialog, debounce, fmtTime, fmtBytes } from './ui.js'
import { api, auth, net } from './api.js'
import { isNative, call, callJSON, onNative, refreshDownloads } from './native.js'

const AI_PRESETS = [
  ['', '选择服务商…', '', ''],
  ['deepseek', 'DeepSeek', 'https://api.deepseek.com/v1', 'deepseek-chat'],
  ['qwen', '通义千问', 'https://dashscope.aliyuncs.com/compatible-mode/v1', 'qwen-plus'],
  ['kimi', 'Kimi（月之暗面）', 'https://api.moonshot.cn/v1', 'moonshot-v1-8k'],
  ['zhipu', '智谱 GLM', 'https://open.bigmodel.cn/api/paas/v4', 'glm-4-flash'],
  ['openai', 'OpenAI', 'https://api.openai.com/v1', 'gpt-4o-mini'],
  ['ollama', 'NAS 本地 Ollama', 'http://ollama:11434/v1', 'qwen2.5:7b'],
]

export async function renderSettings(main, ctx) {
  main.append(h('div', { class: 'page-head' }, h('div', null, h('h1', null, '设置'))))
  const wrap = h('div', { class: 'settings' }, h('p', { class: 'muted' }, '加载中…'))
  main.append(wrap)
  let s
  try { s = await api('/api/settings'); ctx.settings = s } catch (e) { fill(wrap, h('p', null, e.message)); return }
  fill(wrap, isNative ? appSection(ctx) : null, appearance(ctx), network(s), devices(s), await libraries(s), recognition(s, ctx), onlineMeta(s, ctx), narration(s, ctx), fulltextSec(s), opdsSec(s), account(s, ctx), about(s))
}

const sec = (title, desc, ...children) => h('section', { class: 'set-sec' }, h('h2', null, title), desc ? h('p', null, desc) : null, ...children)

async function putSettings(body) {
  const r = await api('/api/settings', { method: 'PUT', body })
  return r
}

// ---------- 外观 ----------
function appearance(ctx) {
  const opts = [['system', '跟随系统'], ['light', '浅色'], ['dark', '深色']]
  const seg = h('div', { class: 'seg' })
  const draw = () => fill(seg, ...opts.map(([v, l]) => h('button', { class: ctx.appPrefs.appearance === v ? 'on' : '', onclick: () => { ctx.setAppearance(v); draw() } }, l)))
  draw()
  return sec('外观', '书库界面的深浅色。阅读时的夜间模式在阅读器的“Aa”里单独设置，可以按时间自动切换。',
    h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '界面颜色')), seg))
}

// ---------- 网络 ----------
function network(s) {
  const lan = h('input', { class: 'input', value: s.network?.lan_url || '', placeholder: 'http://192.168.1.10:8080' })
  const remote = h('input', { class: 'input', value: s.network?.remote_url || '', placeholder: 'https://nas.tail1234.ts.net' })
  const status = h('div', { class: 'stack', style: { gap: '6px', marginTop: '4px' } })
  const cur = h('div', { class: 'status-line' })
  const drawCur = () => fill(cur, h('span', { class: 'dot ' + ({ lan: 'ok', remote: 'acc', offline: 'bad' }[net.route] || 'ok') }), `当前线路：${net.label()}${net.base ? '（' + net.base + '）' : '（' + location.origin + '）'}`)
  drawCur()
  const off = net.on(drawCur)
  setTimeout(() => { if (!document.body.contains(cur)) off() }, 0)

  async function test() {
    clear(status)
    for (const [label, input] of [['内网地址', lan], ['Tailscale 地址', remote]]) {
      const url = input.value.trim().replace(/\/+$/, '')
      if (!url) continue
      const line = h('div', { class: 'status-line' }, h('span', { class: 'dot' }), `${label}：检测中…`)
      status.append(line)
      const t0 = performance.now()
      try {
        if (location.protocol === 'https:' && url.startsWith('http:')) throw new Error('当前页面是 https，浏览器不允许访问 http 地址')
        const ctl = new AbortController(); setTimeout(() => ctl.abort(), 4000)
        const r = await fetch(url + '/api/ping', { cache: 'no-store', signal: ctl.signal })
        const j = await r.json()
        line.firstChild.className = 'dot ok'
        line.lastChild.textContent = `${label}：可以连接（${Math.round(performance.now() - t0)} 毫秒）${j.id !== net.config.serverId && net.config.serverId ? '，但不是同一台服务器' : ''}`
      } catch (e) {
        line.firstChild.className = 'dot bad'
        line.lastChild.textContent = `${label}：连不上${e.name === 'AbortError' ? '（超时）' : e.message.includes('http') ? '，' + e.message : ''}`
      }
    }
  }
  async function saveNet() {
    try {
      await putSettings({ network: { lan_url: lan.value, remote_url: remote.value } })
      await net.setConfig({ lan: lan.value.trim(), remote: remote.value.trim() })
      toast('已保存，当前线路：' + net.label())
    } catch (e) { toastError(e) }
  }
  return sec('网络', '在家时走内网，出门自动切到 Tailscale。两个地址都填上，网页端和手机、手表客户端会自动选能连上的那个，内网优先。',
    h('div', { class: 'stack' },
      h('label', { class: 'field' }, h('span', null, '内网地址'), lan, h('small', null, 'NAS 在局域网里的地址和端口，比如 http://192.168.1.10:8080')),
      h('label', { class: 'field' }, h('span', null, 'Tailscale 地址'), remote, h('small', null, 'NAS 在 Tailscale 里的地址。用 MagicDNS 名称（nas.xxx.ts.net）或 100.x.x.x 都行；开了 tailscale serve 的话填 https 地址，iPhone 才能把书库添加到主屏幕')),
      cur,
      h('div', { class: 'row' }, h('button', { class: 'btn primary', onclick: saveNet }, '保存'), h('button', { class: 'btn', onclick: test }, icon('wifi'), '检测两个地址')),
      status))
}

// ---------- 书库目录 ----------
async function libraries(s) {
  const box = h('div', { class: 'stack' })
  async function draw() {
    const libs = await api('/api/libraries')
    fill(box, ...libs.map(libCard), addForm())
  }
  function tplPreview(input, kind, out) {
    const upd = debounce(async () => {
      try { const r = await api(`/api/preview-template?kind=${kind()}&t=` + encodeURIComponent(input.value)); fill(out, '示例：', h('code', null, r.path)) } catch {}
    }, 250)
    input.addEventListener('input', upd)
    upd()
  }
  function libCard(l) {
    const name = h('input', { class: 'input', value: l.name })
    const path = h('input', { class: 'input', value: l.path })
    const kind = h('select', { class: 'input' }, h('option', { value: 'book', selected: l.kind === 'book' }, '图书'), h('option', { value: 'comic', selected: l.kind === 'comic' }, '漫画'))
    const tpl = h('input', { class: 'input', value: l.template })
    const prev = h('div', { class: 'preview' })
    tplPreview(tpl, () => kind.value, prev)
    kind.onchange = () => tpl.dispatchEvent(new Event('input'))
    return h('div', { class: 'lib' },
      h('div', { class: 'head' }, h('b', null, l.name), h('span', { class: 'badge' }, `${l.count} 本`), l.exists ? null : h('span', { class: 'badge warn' }, '目录不存在'), h('span', { class: 'grow' }),
        h('button', { class: 'btn small quiet danger', onclick: async () => {
          if (!await confirmDialog({ title: `移除书库“${l.name}”？`, message: '只是不再管理这个目录，里面的文件不会被删除。', ok: '移除', danger: true })) return
          try { await api('/api/libraries/' + l.id, { method: 'DELETE' }); toast('已移除'); draw() } catch (e) { toastError(e) }
        } }, '移除')),
      h('div', { class: 'form-grid' },
        h('label', { class: 'field' }, h('span', null, '名称'), name),
        h('label', { class: 'field' }, h('span', null, '类型'), kind),
        h('label', { class: 'field full' }, h('span', null, 'NAS 上的目录（容器内路径）'), path),
        h('label', { class: 'field full' }, h('span', null, '命名规则'), tpl, prev)),
      h('div', { class: 'row' }, h('button', { class: 'btn small primary', onclick: async () => {
        try { await api('/api/libraries/' + l.id, { method: 'PUT', body: { name: name.value, path: path.value, kind: kind.value, template: tpl.value } }); toast('已保存。已有的书不会自动移动，可以在“整理”里一键按新规则整理'); draw() } catch (e) { toastError(e) }
      } }, '保存')))
  }
  function addForm() {
    const name = h('input', { class: 'input', placeholder: '比如：技术书' })
    const path = h('input', { class: 'input', placeholder: '/books/tech' })
    const kind = h('select', { class: 'input' }, h('option', { value: 'book' }, '图书'), h('option', { value: 'comic' }, '漫画'))
    const form = h('div', { class: 'lib', hidden: true },
      h('div', { class: 'form-grid' },
        h('label', { class: 'field' }, h('span', null, '名称'), name),
        h('label', { class: 'field' }, h('span', null, '类型'), kind),
        h('label', { class: 'field full' }, h('span', null, '目录'), path, h('small', null, '填 Docker 容器里看到的路径，需要先在 docker-compose.yml 里把 NAS 文件夹挂载进来'))),
      h('div', { class: 'row' }, h('button', { class: 'btn small primary', onclick: async () => {
        try { await api('/api/libraries', { method: 'POST', body: { name: name.value, path: path.value, kind: kind.value } }); toast('已添加，正在扫描这个目录'); draw() } catch (e) { toastError(e) }
      } }, '添加'), h('button', { class: 'btn small quiet', onclick: () => (form.hidden = true) }, '取消')))
    const btn = h('button', { class: 'btn', onclick: () => { form.hidden = false; btn.hidden = true; name.focus() } }, icon('plus'), '添加书库目录')
    return h('div', { class: 'stack' }, form, h('div', null, btn))
  }
  await draw()
  const series = h('input', { class: 'input', value: s.series_template })
  const sprev = h('div', { class: 'preview' })
  const sp = debounce(async () => { try { const r = await api('/api/preview-template?t=' + encodeURIComponent(series.value)); fill(sprev, '示例：', h('code', null, r.path)) } catch {} }, 250)
  series.addEventListener('input', sp); sp()
  const trash = h('div', { class: 'set-row' })
  const drawTrash = async () => {
    try {
      const t = await api('/api/trash')
      fill(trash, h('div', null, h('div', { class: 'l' }, '回收站'), h('div', { class: 'h' }, t.files ? `删除的书先放在各书库的 _回收站 文件夹，现在有 ${t.files} 个文件，共 ${fmtBytes(t.size)}` : '删除的书先放在各书库的 _回收站 文件夹，现在是空的')),
        t.files ? h('button', { class: 'btn small danger', onclick: async () => {
          if (!await confirmDialog({ title: '清空回收站？', message: `会彻底删除 ${t.files} 个文件（${fmtBytes(t.size)}），不能撤销。`, ok: '彻底删除', danger: true })) return
          try { const r = await api('/api/trash', { method: 'DELETE' }); toast(`已清空回收站（${r.count} 项）`); drawTrash() } catch (e) { toastError(e) }
        } }, '清空回收站') : null)
    } catch {}
  }
  drawTrash()
  return sec('书库目录', `书整理后放在这些目录里。投递目录是 ${s.inbox}，把书丢进去会自动入库。`,
    box, trash,
    h('div', { class: 'stack', style: { marginTop: '18px' } },
      h('label', { class: 'field' }, h('span', null, '系列图书的命名规则'), series, sprev,
        h('small', null, '可用：{分类} {作者} {书名} {原名} {系列} {序号:02} {年份} {译者}。花括号里可以带前后缀，比如 { (年份)}，没有年份时整段省略')),
      h('div', null, h('button', { class: 'btn small', onclick: async () => { try { await putSettings({ series_template: series.value }); toast('已保存') } catch (e) { toastError(e) } } }, '保存'))))
}

// ---------- 识别与整理 ----------
function recognition(s, ctx) {
  const ai = s.ai || {}
  const en = h('input', { type: 'checkbox', checked: ai.enabled })
  const preset = h('select', { class: 'input' }, AI_PRESETS.map(([v, l]) => h('option', { value: v }, l)))
  const base = h('input', { class: 'input', value: ai.base_url || '', placeholder: 'https://api.deepseek.com/v1' })
  const key = h('input', { class: 'input', value: ai.api_key || '', placeholder: 'sk-…', autocomplete: 'off' })
  const model = h('input', { class: 'input', value: ai.model || '', placeholder: 'deepseek-chat' })
  preset.onchange = () => { const p = AI_PRESETS.find(x => x[0] === preset.value); if (p && p[2]) { base.value = p[2]; model.value = p[3] } }
  const out = h('div')
  const thr = h('input', { type: 'range', min: 0.5, max: 0.99, step: 0.01, value: s.auto_threshold, style: { flex: 1 } })
  const thrVal = h('span', { class: 'muted', style: { width: '44px', textAlign: 'right' } }, Math.round(s.auto_threshold * 100) + '%')
  thr.oninput = () => (thrVal.textContent = Math.round(thr.value * 100) + '%')
  const cats = h('textarea', { class: 'input', rows: 2 }, (s.categories || []).join('、'))
  const scanMin = h('select', { class: 'input', style: { width: 'auto' } }, [['15', '每 15 分钟'], ['30', '每 30 分钟'], ['60', '每小时'], ['360', '每 6 小时'], ['1440', '每天']].map(([v, l]) => h('option', { value: v, selected: s.scan_minutes === v }, l)))
  const body = () => ({ enabled: en.checked, base_url: base.value.trim(), api_key: key.value.trim(), model: model.value.trim() })

  async function test(btn) {
    btn.disabled = true
    fill(out, h('p', { class: 'muted small-text' }, '正在请求 AI…'))
    try {
      const r = await api('/api/ai/test', { method: 'POST', body: body() })
      const x = r.result
      fill(out, h('div', { class: 'code-block' }, `测试文件名：${r.input}\n识别结果：《${x.title}》 ${x.author}${x.series ? `　系列：${x.series} 第 ${x.series_index} 本` : ''}　分类：${x.category}　把握：${Math.round(x.confidence * 100)}%\n用时 ${(r.ms / 1000).toFixed(1)} 秒`))
    } catch (e) { fill(out, h('p', { class: 'small-text', style: { color: 'var(--danger)' } }, e.message)) }
    btn.disabled = false
  }
  async function saveAll() {
    try {
      const r = await putSettings({ ai: body(), auto_threshold: +thr.value, categories: cats.value.split(/[、,，\s]+/).filter(Boolean), scan_minutes: scanMin.value })
      ctx.settings = r
      key.value = r.ai.api_key
      toast('已保存')
    } catch (e) { toastError(e) }
  }
  return sec('识别与整理', '入库时用 AI 把乱七八糟的文件名恢复成正式书名、作者，并自动分类。只发送文件名、内嵌信息和开头几百字，不上传整本书。',
    h('div', { class: 'stack' },
      h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '用 AI 识别书名'), h('div', { class: 'h' }, '关闭时只用文件名规则和书里的内嵌信息')), h('label', { class: 'switch' }, en, h('i'))),
      h('div', { class: 'form-grid' },
        h('label', { class: 'field' }, h('span', null, '服务商'), preset),
        h('label', { class: 'field' }, h('span', null, '模型'), model),
        h('label', { class: 'field full' }, h('span', null, '接口地址（OpenAI 兼容）'), base),
        h('label', { class: 'field full' }, h('span', null, 'API Key'), key, h('small', null, '本地 Ollama 不需要 Key'))),
      h('div', { class: 'row' }, h('button', { class: 'btn', onclick: e => test(e.currentTarget) }, icon('sparkle'), '用示例测试')),
      out,
      h('div', { class: 'field' }, h('span', null, '自动整理的把握门槛'), h('div', { class: 'row' }, thr, thrVal),
        h('small', null, '识别把握高于这个值，直接改名归档；低于这个值，先放进“待确认”等你核对')),
      h('label', { class: 'field' }, h('span', null, '分类'), cats, h('small', null, '一级文件夹按这些分类建立，用顿号分隔。AI 只会从这里面选')),
      h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '定时扫描书库'), h('div', { class: 'h' }, '发现直接放进书库目录的新文件、清理已删除文件的记录')), scanMin),
      h('div', null, h('button', { class: 'btn primary', onclick: saveAll }, '保存'))))
}

// ---------- 账户 ----------
function account(s, ctx) {
  const name = h('input', { class: 'input', value: s.server_name })
  const old = h('input', { class: 'input', type: 'password', autocomplete: 'current-password' })
  const nw = h('input', { class: 'input', type: 'password', autocomplete: 'new-password', placeholder: '至少 6 位' })
  return sec('账户', null,
    h('div', { class: 'stack' },
      h('label', { class: 'field' }, h('span', null, '书库名称'), h('div', { class: 'row' }, name, h('button', { class: 'btn', onclick: async () => {
        try { await putSettings({ server_name: name.value }); ctx.server.name = name.value; toast('已保存') } catch (e) { toastError(e) }
      } }, '保存'))),
      h('div', { class: 'form-grid' },
        h('label', { class: 'field' }, h('span', null, '当前密码'), old),
        h('label', { class: 'field' }, h('span', null, '新密码'), nw)),
      h('div', { class: 'row' },
        h('button', { class: 'btn', onclick: async () => {
          try { await api('/api/auth/password', { method: 'POST', body: { Old: old.value, New: nw.value } }); old.value = nw.value = ''; toast('密码已修改，其他设备需要重新登录') } catch (e) { toastError(e) }
        } }, '修改密码'),
        h('span', { class: 'grow' }),
        h('button', { class: 'btn quiet', onclick: async () => {
          await api('/api/auth/logout', { method: 'POST' }).catch(() => {})
          auth.set(''); location.hash = ''; location.reload()
        } }, icon('logout'), '退出登录'))))
}

// ---------- 关于 ----------
function about(s) {
  const t = s.tools || {}
  const line = (ok, label, hint) => h('div', { class: 'status-line' }, h('span', { class: 'dot ' + (ok ? 'ok' : '') }), label, ok ? null : h('span', { class: 'muted small-text' }, '　' + hint))
  return sec('关于', `书库 ${s.version}。EPUB、MOBI、AZW3、FB2、PDF、TXT、CBZ、CBR 不需要额外组件就能读。`,
    h('div', { class: 'stack', style: { gap: '8px' } },
      line(t.calibre, 'Calibre 格式转换（DOCX、CHM、RTF 等）', '使用 full 版镜像即可'),
      line(t.djvu, 'DJVU 支持', '使用 full 版镜像即可'),
      line(t['7z'], '7z / CB7 漫画包', '使用 full 版镜像即可'),
      line(t.pdf_cover, '服务器生成 PDF 封面', '没有也没关系，第一次打开 PDF 时会自动生成')))
}

// ---------- 联网补全 ----------
function onlineMeta(s, ctx) {
  const m = s.online_meta || {}
  const sw = (key, label, hint) => {
    const input = h('input', { type: 'checkbox', checked: !!m[key] })
    input.onchange = async () => { m[key] = input.checked; try { await putSettings({ online_meta: m }) } catch (e) { toastError(e) } }
    return h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, label), hint ? h('div', { class: 'h' }, hint) : null), h('label', { class: 'switch' }, input, h('i')))
  }
  const prog = h('div', { class: 'muted small-text' })
  const btn = h('button', { class: 'btn' }, icon('refresh'), '补全现有的书')
  let timer
  const poll = async () => {
    try {
      const st = (await api('/api/status')).enrich
      prog.textContent = st.running ? `正在补全 ${st.done} / ${st.total}：${st.current}` : st.total ? `上次补全：检查了 ${st.total} 本，补全了 ${st.filled} 本` : ''
      btn.disabled = st.running
      if (st.running && document.body.contains(prog)) timer = setTimeout(poll, 2500)
    } catch {}
  }
  btn.onclick = async () => {
    try { const r = await api('/api/enrich-all', { method: 'POST' }); toast(r.message); poll() } catch (e) { toastError(e) }
  }
  poll()
  return sec('联网补全', '从豆瓣读书、Open Library、Google Books 和 Bangumi（漫画）查封面、简介、出版年份和 ISBN。只补空着的信息，不会覆盖你改过的内容。',
    h('div', { class: 'stack', style: { gap: '4px' } },
      sw('enabled', '联网查找', '关闭后，入库和详情页都不再联网'),
      sw('on_ingest', '入库时自动核对', '查到同名同作者的书，就提高识别把握，并补上封面、简介'),
      sw('douban', '豆瓣读书', '中文书最全'),
      sw('openlibrary', 'Open Library', '外文书'),
      sw('google', 'Google Books', 'NAS 需要能访问 Google'),
      sw('bangumi', 'Bangumi', '漫画'),
      h('div', { class: 'row', style: { marginTop: '10px' } }, btn, prog)))
}

// ---------- 听书 ----------
function narration(s, ctx) {
  const t = s.tts || { openai: {} }
  t.openai ||= {}
  const engine = h('select', { class: 'input' },
    [['edge', '微软自然语音（推荐，免费）'], ['openai', '自定义语音接口（OpenAI 兼容）'], ['browser', '设备自带语音（不经过 NAS）']].map(([v, l]) => h('option', { value: v, selected: t.engine === v }, l)))
  const voiceSel = h('select', { class: 'input' })
  const base = h('input', { class: 'input', value: t.openai.base_url || '', placeholder: 'https://api.openai.com/v1 或 http://kokoro:8880/v1' })
  const key = h('input', { class: 'input', value: t.openai.api_key || '', placeholder: '本地服务可以不填', autocomplete: 'off' })
  const model = h('input', { class: 'input', value: t.openai.model || 'tts-1' })
  const ovoice = h('input', { class: 'input', value: t.openai.voice || 'alloy' })
  const ovoices = h('input', { class: 'input', value: t.openai.voices || '', placeholder: '可选，比如 alloy,nova,zf_xiaoxiao' })
  const openaiBox = h('div', { class: 'form-grid' },
    h('label', { class: 'field full' }, h('span', null, '接口地址'), base),
    h('label', { class: 'field full' }, h('span', null, 'API Key'), key),
    h('label', { class: 'field' }, h('span', null, '模型'), model),
    h('label', { class: 'field' }, h('span', null, '默认音色'), ovoice),
    h('label', { class: 'field full' }, h('span', null, '可选音色列表'), ovoices, h('small', null, '阅读器里可以在这些音色之间切换，用逗号分隔')))
  const voiceRow = h('label', { class: 'field' }, h('span', null, '默认音色'), voiceSel, h('small', null, '阅读时还可以随时换'))
  const body = () => ({ engine: engine.value, voice: voiceSel.value || t.voice, openai: { base_url: base.value.trim(), api_key: key.value.trim(), model: model.value.trim(), voice: ovoice.value.trim(), voices: ovoices.value.trim() } })
  async function loadVoices() {
    if (engine.value !== 'edge') return
    try {
      const r = await api('/api/tts/voices')
      fill(voiceSel, r.voices.map(v => h('option', { value: v.id, selected: v.id === (t.voice || r.default) }, `${v.name}（${v.lang}${v.gender ? ' · ' + v.gender : ''}）${v.desc ? '　' + v.desc : ''}`)))
    } catch {}
  }
  const sync = () => { openaiBox.hidden = engine.value !== 'openai'; voiceRow.hidden = engine.value !== 'edge' }
  engine.onchange = async () => { sync(); await saveIt(true); loadVoices() }
  sync(); loadVoices()
  async function saveIt(quiet) {
    try { const r = await putSettings({ tts: body() }); key.value = r.tts.openai.api_key || ''; if (!quiet) toast('已保存') } catch (e) { toastError(e) }
  }
  let audio
  const status = h('span', { class: 'muted small-text' })
  async function listen(btn) {
    btn.disabled = true
    status.textContent = '正在合成…'
    try {
      if (engine.value === 'browser') {
        const u = new SpeechSynthesisUtterance('夜色渐深，城市的灯一盏一盏亮了起来。这是设备自带的语音。')
        u.lang = 'zh-CN'; speechSynthesis.cancel(); speechSynthesis.speak(u)
        status.textContent = ''
      } else {
        await saveIt(true)
        const res = await api('/api/tts', { method: 'POST', raw: true, body: { text: '夜色渐深，城市的灯一盏一盏亮了起来。他合上书，听见窗外的雨声。', voice: engine.value === 'edge' ? voiceSel.value : '' } })
        audio?.pause()
        audio = new Audio(URL.createObjectURL(await res.blob()))
        audio.play()
        status.textContent = ''
      }
    } catch (e) { status.textContent = e.message }
    btn.disabled = false
  }
  return sec('听书', '阅读器里点耳机图标开始朗读：高亮当前句子，自动翻页，可以定时停止。合成过的段落会缓存在 NAS 上，换设备接着听不用重新合成。',
    h('div', { class: 'stack' },
      h('label', { class: 'field' }, h('span', null, '语音引擎'), engine,
        h('small', null, '微软自然语音和 Edge 浏览器“大声朗读”是同一套，最自然，需要 NAS 能访问 speech.platform.bing.com。也可以接 OpenAI、硅基流动，或在 NAS 上跑 Kokoro 之类的本地语音模型。')),
      voiceRow, openaiBox,
      h('div', { class: 'row' }, h('button', { class: 'btn primary', onclick: () => saveIt() }, '保存'), h('button', { class: 'btn', onclick: e => listen(e.currentTarget) }, icon('play'), '试听'), status)))
}

// ---------- 书内搜索 ----------
function fulltextSec(s) {
  const ft = s.fulltext || {}
  const input = h('input', { type: 'checkbox', checked: !!ft.enabled, disabled: !ft.available })
  const st = h('div', { class: 'muted small-text' })
  const draw = f => { st.textContent = !f.available ? '这个版本没有编译全文搜索支持' : `已索引 ${f.indexed} / ${f.total} 个文件${f.skipped ? `，${f.skipped} 个无法提取文字（扫描版 PDF 等）` : ''}${f.running ? '，正在索引：' + f.current : ''}` }
  draw(ft)
  input.onchange = async () => { try { const r = await putSettings({ fulltext: input.checked }); draw(r.fulltext) } catch (e) { toastError(e) } }
  return sec('书内搜索', '给书的正文建索引，在书架搜索框里输入两个字以上，就能找到哪本书的哪一章提到过。扫描版 PDF 和漫画没有文字，不参与。索引大约占书本身大小的一半。',
    h('div', { class: 'stack', style: { gap: '6px' } },
      h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '建立正文索引')), h('label', { class: 'switch' }, input, h('i'))),
      st,
      h('div', null, h('button', { class: 'btn small', onclick: async () => {
        if (!await confirmDialog({ title: '重建全部索引？', message: '会删除现有索引，在后台重新建立。书多的话需要一段时间。', ok: '重建' })) return
        try { await api('/api/fulltext/rebuild', { method: 'POST' }); toast('已开始重建') } catch (e) { toastError(e) }
      } }, '重建索引'))))
}

// ---------- OPDS ----------
function opdsSec(s) {
  const urls = [location.origin + '/opds']
  const remote = s.network?.remote_url
  if (remote && !remote.startsWith(location.origin)) urls.push(remote.replace(/\/$/, '') + '/opds')
  return sec('用其他阅读器访问（OPDS）', '在 KOReader、静读天下、Librera、Panels、Yomu 等支持 OPDS 的阅读器里添加书库，就能直接浏览、搜索和下载。用户名随便填，密码是书库密码。',
    h('div', { class: 'stack', style: { gap: '8px' } },
      urls.map(u => h('div', { class: 'row' }, h('code', { class: 'code-block grow' }, u), h('button', { class: 'btn small', onclick: () => { navigator.clipboard?.writeText(u).then(() => toast('已复制'), () => toast(u)) } }, '复制'))),
      h('small', { class: 'muted' }, 'KOReader：菜单 → 搜索 → OPDS 目录 → 添加，填上面的地址。')))
}

// ---------- 手机、手表、浏览器插件 ----------
function devices(s) {
  const pairBox = h('div')
  const list = h('div', { class: 'stack', style: { gap: '6px' } })
  let timer = 0
  async function pair(btn) {
    btn.disabled = true
    try {
      const p = await api('/api/pair', { method: 'POST' })
      const left = h('span')
      let end = Date.now() + p.expires_in * 1000
      clearInterval(timer)
      const tick = () => {
        const sec = Math.max(0, Math.round((end - Date.now()) / 1000))
        left.textContent = sec ? `${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')} 后失效` : '已失效，请重新生成'
        if (!sec || !document.body.contains(left)) clearInterval(timer)
      }
      timer = setInterval(tick, 1000); tick()
      fill(pairBox, h('div', { class: 'pair' },
        p.qr ? h('img', { class: 'qr', alt: '配对二维码', src: 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(p.qr) }) : null,
        h('div', null,
          h('div', { class: 'muted small-text' }, '配对码'),
          h('div', { class: 'pair-code' }, p.code.slice(0, 4) + ' ' + p.code.slice(4)),
          h('div', { class: 'muted small-text' }, left),
          h('ol', { class: 'pair-steps' },
            h('li', null, '安卓手机：用相机扫二维码，或者打开书库 App 选“输入配对码”。连接后手表会自动从手机拿到连接信息。'),
            h('li', null, '浏览器插件：在插件设置里填书库地址和上面的配对码。'),
            h('li', null, '配对码只能用一次，10 分钟内有效。')))))
    } catch (e) { toastError(e) }
    btn.disabled = false
  }
  async function drawSessions() {
    try {
      const ss = await api('/api/sessions')
      fill(list, ss.map(x => h('div', { class: 'dev-row' },
        icon(/手表|Watch|Wear/i.test(x.device) ? 'watch' : /App|安卓|Android|iPhone|手机/i.test(x.device) ? 'phone' : /插件|Chrome|Edge/i.test(x.device) ? 'puzzle' : 'globe'),
        h('div', { class: 'grow' }, h('div', null, x.device, x.current ? h('span', { class: 'badge', style: { marginLeft: '8px' } }, '当前') : null),
          h('small', null, `最近使用：${fmtTime(x.last_seen) || '—'} · 登录于 ${fmtTime(x.created_at)}`)),
        x.current ? null : h('button', { class: 'btn small quiet danger', onclick: async () => {
          if (!await confirmDialog({ title: `移除“${x.device}”？`, message: '这台设备需要重新登录或配对才能再访问书库。', ok: '移除', danger: true })) return
          try { await api('/api/sessions/' + x.id, { method: 'DELETE' }); drawSessions() } catch (e) { toastError(e) }
        } }, '移除'))))
    } catch {}
  }
  drawSessions()
  return sec('手机、手表和浏览器插件', '安卓 App 能离线读书、后台听书、用音量键翻页，自动在内网和 Tailscale 之间切换；Wear OS 手表从手机获取连接信息；浏览器插件能把任何网站上的电子书一键加入书库。',
    h('div', { class: 'stack' },
      h('div', { class: 'row wrap' }, h('button', { class: 'btn primary', onclick: e => pair(e.currentTarget) }, icon('phone'), '显示配对二维码')),
      pairBox,
      h('div', { class: 'sub-title', style: { margin: '10px 0 0' } }, '已登录的设备'),
      list))
}

// ---------- 安卓 App 自己的设置（只在 App 里显示） ----------
function appSection(ctx) {
  const prefs = callJSON('prefs') || {}
  const sw = (key, label, hint) => {
    const input = h('input', { type: 'checkbox', checked: !!prefs[key] })
    input.onchange = () => { prefs[key] = input.checked; call('setPref', key, String(input.checked)) }
    return h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, label), hint ? h('div', { class: 'h' }, hint) : null), h('label', { class: 'switch' }, input, h('i')))
  }
  const ttsSel = h('select', { class: 'input', style: { width: 'auto' } },
    [['server', '书库的语音（微软自然语音等）'], ['device', '手机自带语音（不联网也能用）']].map(([v, l]) => h('option', { value: v, selected: (prefs.tts_engine || 'server') === v }, l)))
  ttsSel.onchange = () => call('setPref', 'tts_engine', ttsSel.value)
  const dl = h('div', { class: 'stack', style: { gap: '6px' } })
  const draw = () => {
    const list = refreshDownloads()
    const used = list.reduce((a, d) => a + (d.size || 0), 0)
    fill(dl, list.length ? list.map(d => h('div', { class: 'dev-row' }, icon('book'),
      h('div', { class: 'grow' }, h('div', null, d.title), h('small', null, d.state === 'done' ? `${(d.format || '').toUpperCase()} · ${fmtBytes(d.size)}` : d.state === 'error' ? '下载失败：' + (d.error || '') : `下载中 ${Math.round((d.progress || 0) * 100)}%`)),
      h('button', { class: 'btn small quiet danger', onclick: () => { call('removeDownload', d.id); draw() } }, '删除'))) : h('p', { class: 'muted small-text', style: { margin: 0 } }, '还没有下载到手机的书。在书的详情里点“下载到手机”，没网的时候也能读。'),
    list.length ? h('div', { class: 'muted small-text' }, `共占用 ${fmtBytes(used)}`) : null)
  }
  draw()
  const off = onNative('download', draw)
  setTimeout(() => { const t = setInterval(() => { if (!document.body.contains(dl)) { off(); clearInterval(t) } }, 2000) }, 0)
  return sec('手机 App', `书库 App ${call('version') || ''}。`,
    h('div', { class: 'stack', style: { gap: '4px' } },
      sw('volume_keys', '音量键翻页', '阅读时按音量键上一页、下一页'),
      sw('keep_screen_on', '阅读时屏幕常亮'),
      sw('auto_download', '自动下载在读的书', '连着内网时，把“在读”里的书下载到手机'),
      h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '听书的声音'), h('div', { class: 'h' }, '锁屏、切到别的 App 也会继续读')), ttsSel),
      h('div', { class: 'row wrap', style: { margin: '8px 0 14px' } },
        h('button', { class: 'btn', onclick: () => { const r = callJSON('syncWatch'); toast(r?.message || '已把连接信息发给手表') } }, icon('watch'), '连接手表'),
        h('button', { class: 'btn quiet', onclick: async () => { if (await confirmDialog({ title: '断开这台手机？', message: '下载到手机的书会保留。之后需要重新扫码或输入配对码。', ok: '断开' })) call('logout') } }, '断开并重新配对')),
      h('div', { class: 'sub-title', style: { margin: '6px 0 6px' } }, '下载到手机的书'),
      dl))
}
