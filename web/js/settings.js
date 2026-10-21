import { h, clear, fill, add, icon, toast, toastError, confirmDialog, formDialog, debounce, fmtTime, fmtBytes, fmtDateTime, coverEl, einkDetected, autoUIScale, isEInk } from './ui.js'
import { api, apiUrl, auth, net } from './api.js'
import { isNative, call, callJSON, onNative, refreshDownloads } from './native.js'
import { downloadToPhone } from './library.js'
import { LANGS } from './reader/translate.js'

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
  const toc = h('nav', { class: 'set-toc', 'aria-label': '设置目录' })
  const role = ctx.isAdmin ? '管理员' : '家人'
  main.append(h('div', { class: 'page-head' }, h('div', null, h('h1', null, '设置'), ctx.user ? h('div', { class: 'sub' }, ctx.user.name === role ? role : `${ctx.user.name} · ${role}`) : null)), toc)
  const wrap = h('div', { class: 'settings' }, h('p', { class: 'muted' }, '加载中…'))
  main.append(wrap)
  let s
  try { s = await api('/api/settings'); ctx.settings = s } catch (e) { fill(wrap, h('p', null, e.message)); return }
  // 家人只看到自己用得上的：外观、账号、设备、手机 App、听书试听、其他阅读器、版本
  const list = ctx.isAdmin
    ? [['app', isNative && appSection(ctx)], ['look', appearance(ctx)], ['account', account(s, ctx)], ['family', family(ctx)], ['network', network(s)], ['devices', devices(s)],
      ['libs', libraries(s)], ['trash', trashSec(ctx)], ['storage', storageSec(ctx)], ['backup', backupSec(ctx)], ['recognize', recognition(s, ctx)], ['translate', translation(s, ctx)], ['meta', onlineMeta(s, ctx)],
      ['tts', narration(s, ctx)], ['fulltext', fulltextSec(s)], ['opds', opdsSec(s, ctx)], ['update', updateSec(s, ctx)], ['about', about(s)]]
    : [['app', isNative && appSection(ctx)], ['look', appearance(ctx)], ['account', account(s, ctx)], ['devices', devices(s)],
      ['tts', narrationMember(s)], ['opds', opdsSec(s, ctx)], ['update', updateSec(s, ctx)]]
  const secs = (await Promise.all(list.map(async ([id, el]) => [id, await el]))).filter(([, el]) => el)
  for (const [id, el] of secs) el.id = 'set-' + id
  fill(wrap, secs.map(([, el]) => el))
  fill(toc, secs.map(([id, el]) => h('button', { type: 'button', class: 'chip', onclick: () => document.getElementById('set-' + id)?.scrollIntoView({ behavior: 'smooth', block: 'start' }) }, el.querySelector('h2')?.textContent || id)))
}

const sec = (title, desc, ...children) => h('section', { class: 'set-sec' }, h('h2', null, title), desc ? h('p', null, desc) : null, ...children)

async function putSettings(body) {
  const r = await api('/api/settings', { method: 'PUT', body })
  return r
}

// ---------- 外观 ----------
function appearance(ctx) {
  const p = ctx.appPrefs
  const segOf = (opts, cur, pick) => {
    const seg = h('div', { class: 'seg' })
    const draw = () => fill(seg, ...opts.map(([v, l]) => h('button', { class: String(cur()) === String(v) ? 'on' : '', type: 'button', onclick: () => { pick(v); draw(); drawHints() } }, l)))
    draw()
    return seg
  }
  const colors = segOf([['system', '跟随系统'], ['light', '浅色'], ['dark', '深色']], () => p.appearance, v => ctx.setAppearance(v))
  // 墨水屏阅读器（墨案、文石等）：黑白、没有动画和阴影，阅读时按页翻
  const eink = segOf([['auto', '自动'], ['on', '开'], ['off', '关']], () => p.eink || 'auto', v => ctx.setDisplay('eink', v))
  const scale = segOf([['auto', '自动'], ['0.9', '小'], ['1', '标准'], ['1.15', '大'], ['1.3', '特大']], () => p.uiScale || 'auto', v => ctx.setDisplay('uiScale', v))
  const einkHint = h('div', { class: 'h' })
  const scaleHint = h('div', { class: 'h' })
  function drawHints() {
    einkHint.textContent = (einkDetected() ? '这台设备是墨水屏阅读器，' : '没认出是墨水屏阅读器，') + (isEInk() ? '现在开着' : '现在关着') + '。黑白显示、没有动画，阅读时按页翻'
    scaleHint.textContent = '自动：' + (isEInk() ? `墨水屏按屏幕尺寸放大到 ${Math.round(autoUIScale(true) * 100)}%` : '标准大小') + '。只改书库界面的字，书里的字号在阅读器的“Aa”里调'
  }
  drawHints()
  return sec('外观', '书库界面的深浅色、字的大小，墨水屏阅读器的显示方式。阅读时的夜间模式在阅读器的“Aa”里单独设置，可以按时间自动切换。',
    h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '界面颜色')), colors),
    h('div', { class: 'set-row wrap-sm' }, h('div', null, h('div', { class: 'l' }, '界面字号'), scaleHint), scale),
    h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '墨水屏模式'), einkHint), eink))
}

// ---------- 网络 ----------
function network(s) {
  const lan = h('input', { class: 'input', value: s.network?.lan_url || '', placeholder: 'http://192.168.1.10:27200' })
  const remote = h('input', { class: 'input', value: s.network?.remote_url || '', placeholder: 'https://nas.tail1234.ts.net:9443' })
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
      h('label', { class: 'field' }, h('span', null, '内网地址'), lan, h('small', null, 'NAS 在局域网里的地址和端口，比如 http://192.168.1.10:27200')),
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
          if (!await confirmDialog({ title: `移除书库“${l.name}”？`, message: '只是不再管理这个目录，里面的文件不会被删除。这个书库里的书会从书架上消失，大家在这些书上的阅读进度和笔记也会一起清掉（移除前会自动备份一次数据库）。', ok: '移除', danger: true })) return
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
  return sec('书库目录', `书整理后放在这些目录里。投递目录是 ${s.inbox}，把书丢进去会自动入库。`,
    box,
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

// ---------- 翻译 ----------
// 阅读器里“翻译本页”用哪个：微软翻译（免费，不用设置）或上面配置的 AI；默认译成哪种语言（各人在阅读器里还能另选）
function translation(s, ctx) {
  const t = s.translate || { engine: 'microsoft', to: 'zh-Hans' }
  const engines = [['microsoft', '微软翻译'], ['ai', 'AI']]
  let engine = t.engine === 'ai' ? 'ai' : 'microsoft'
  const seg = h('div', { class: 'seg tr-engine' })
  const drawSeg = () => fill(seg, ...engines.map(([v, l]) => h('button', { type: 'button', class: engine === v ? 'on' : '', 'aria-pressed': engine === v ? 'true' : 'false', onclick: () => { engine = v; drawSeg() } }, l)))
  drawSeg()
  const to = h('select', { class: 'input tr-default', style: { width: 'auto' } }, LANGS.map(([v, l]) => h('option', { value: v, selected: t.to === v }, l)))
  async function saveIt() {
    try {
      const r = await putSettings({ translate: { engine, to: to.value } })
      ctx.settings = r
      toast('已保存')
    } catch (e) { toastError(e) }
  }
  const aiOn = !!(s.ai && s.ai.enabled && s.ai.base_url && s.ai.model)
  return sec('翻译', '阅读时点工具栏的“翻译”，把这一页译成另一种语言，译文和原文对照着看。翻过的段落书库会记下来，同一段不会再花一次翻译。',
    h('div', { class: 'stack' },
      h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '用哪个翻译'),
        h('div', { class: 'h' }, '微软翻译免费、不用设置（书库要能访问微软的翻译服务）；AI 用上面“识别与整理”里配置的 AI，译得更顺，按 AI 的价格收费' + (aiOn ? '' : '。AI 还没配置好时会先用微软翻译'))), seg),
      h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '默认译成'), h('div', { class: 'h' }, '家人在阅读器里可以另选')), to),
      h('div', null, h('button', { class: 'btn primary', onclick: saveIt }, '保存'))))
}

// ---------- 账号 ----------
function account(s, ctx) {
  const admin = ctx.isAdmin
  const min = admin ? 6 : 4
  const name = h('input', { class: 'input', value: s.server_name, 'aria-label': '书库名称' })
  const old = h('input', { class: 'input', type: 'password', autocomplete: 'current-password' })
  const nw = h('input', { class: 'input', type: 'password', autocomplete: 'new-password', placeholder: `至少 ${min} 位` })
  const pwBtn = h('button', { class: 'btn', type: 'button' }, '修改密码')
  pwBtn.onclick = async () => {
    if (nw.value.length < min) { toast(`新密码至少 ${min} 位`, { error: true }); nw.focus(); return }
    pwBtn.disabled = true
    try { await api('/api/auth/password', { method: 'POST', body: { Old: old.value, New: nw.value } }); old.value = nw.value = ''; toast('密码已修改，你在其他设备上的登录需要重新输入密码') } catch (e) { toastError(e) }
    pwBtn.disabled = false
  }
  const me = ctx.user
  const logoutBtn = h('button', { class: 'btn quiet', type: 'button', onclick: () => ctx.logout() }, icon('logout'), '退出登录')
  return sec('账号', admin ? '你是管理员：管理书和设置，在“家人账号”里给家人各开一个账号。' : '书架、进度、笔记和阅读统计都是你自己的。书目和设置由管理员管理。',
    h('div', { class: 'stack' },
      me ? h('div', { class: 'dev-row me-row' }, h('span', { class: 'av', 'aria-hidden': 'true' }, [...me.name][0]),
        h('div', { class: 'grow' }, h('div', null, me.name, h('span', { class: 'badge' + (admin ? ' accent' : ''), style: { marginLeft: '8px' } }, admin ? '管理员' : '家人')),
          h('small', null, admin || !(me.libraries || []).length ? '能看全部书库' : '只能看管理员允许的书库')),
        logoutBtn) : null,
      admin ? h('label', { class: 'field' }, h('span', null, '书库名称'), h('div', { class: 'row' }, name, h('button', { class: 'btn', type: 'button', onclick: async () => {
        try { await putSettings({ server_name: name.value }); ctx.server.name = name.value; toast('已保存') } catch (e) { toastError(e) }
      } }, '保存'))) : null,
      h('div', { class: 'form-grid' },
        h('label', { class: 'field' }, h('span', null, '当前密码'), old),
        h('label', { class: 'field' }, h('span', null, '新密码'), nw)),
      h('div', { class: 'row' }, pwBtn, me ? null : h('span', { class: 'grow' }), me ? null : logoutBtn)))
}

// ---------- 家人账号（管理员） ----------
const libNames = (ids, libs) => !ids?.length ? '全部书库' : ids.map(id => libs.find(l => l.id === id)?.name || '已移除的书库').join('、')

async function family(ctx) {
  const box = h('div', { class: 'stack', style: { gap: '8px' } })
  let libs = []
  const libOpts = () => libs.map(l => [l.id, `${l.name}（${l.kind === 'comic' ? '漫画' : '图书'}）`])
  async function draw() {
    let users
    try { [users, libs] = await Promise.all([api('/api/users'), api('/api/libraries')]) } catch (e) { fill(box, h('p', { class: 'muted' }, e.message)); return }
    fill(box, users.map(row), h('div', null, h('button', { class: 'btn', type: 'button', onclick: add }, icon('plus'), '添加家人')))
  }
  function row(u) {
    const self = u.id === ctx.user?.id
    return h('div', { class: 'dev-row user-row', dataset: { user: u.name } },
      h('span', { class: 'av', 'aria-hidden': 'true' }, [...u.name][0]),
      h('div', { class: 'grow' },
        h('div', null, u.name, self ? h('span', { class: 'muted small-text' }, '（你）') : null,
          h('span', { class: 'badge' + (u.role === 'admin' ? ' accent' : ''), style: { marginLeft: '8px' } }, u.role === 'admin' ? '管理员' : '家人')),
        h('small', null, [u.role === 'admin' ? '管理书和设置' : '能看：' + libNames(u.libraries, libs), `${u.devices} 台设备`, u.last_seen ? '最近使用 ' + fmtTime(u.last_seen) : '还没登录过', u.books ? `读过 ${u.books} 本` : ''].filter(Boolean).join(' · '))),
      h('button', { class: 'btn small', type: 'button', onclick: () => edit(u) }, '修改'),
      self ? null : h('button', { class: 'btn small quiet danger', type: 'button', onclick: () => remove(u) }, '删除'))
  }
  async function add() {
    const v = await formDialog({
      title: '添加家人', message: '他用自己的名字和密码登录（手机、手表也可以扫配对码），进度、笔记、读完状态都各算各的。',
      fields: [
        { key: 'name', label: '名字', placeholder: '比如：小明' },
        { key: 'password', label: '密码', type: 'password', placeholder: '至少 4 位' },
        { key: 'role', label: '身份', type: 'select', value: 'member', options: [['member', '家人：只能读书'], ['admin', '管理员：能管理书和设置']] },
        libs.length > 1 ? { key: 'libs', label: '能看的书库', type: 'checks', options: libOpts(), value: [], hint: '都不勾 = 能看全部书库' } : null,
      ].filter(Boolean),
      ok: '添加',
      validate: v => !v.name.trim() ? '请填写名字' : v.password.length < 4 ? '密码至少 4 位' : '',
    })
    if (!v) return
    try {
      await api('/api/users', { method: 'POST', body: { name: v.name.trim(), password: v.password, role: v.role, libraries: (v.libs || []).map(Number) } })
      toast(`已添加“${v.name.trim()}”。在他的设备上打开书库，选他的名字、输入密码就能登录`)
      draw()
    } catch (e) { toastError(e) }
  }
  async function edit(u) {
    const v = await formDialog({
      title: `修改“${u.name}”`,
      fields: [
        { key: 'name', label: '名字', value: u.name },
        { key: 'password', label: '新密码', type: 'password', placeholder: '不改就留空', hint: '改了密码，他在其他设备上的登录要重新输入' },
        { key: 'role', label: '身份', type: 'select', value: u.role, options: [['member', '家人：只能读书'], ['admin', '管理员：能管理书和设置']] },
        libs.length > 1 ? { key: 'libs', label: '能看的书库（家人）', type: 'checks', options: libOpts(), value: u.libraries || [], hint: '都不勾 = 能看全部书库。管理员总是能看全部' } : null,
      ].filter(Boolean),
      ok: '保存',
      validate: v => !v.name.trim() ? '名字不能为空' : v.password && v.password.length < 4 ? '密码至少 4 位' : '',
    })
    if (!v) return
    const body = {}
    if (v.name.trim() !== u.name) body.name = v.name.trim()
    if (v.password) body.password = v.password
    if (v.role !== u.role) body.role = v.role
    if (v.libs) {
      const next = v.libs.map(Number).sort((a, b) => a - b), cur = [...(u.libraries || [])].sort((a, b) => a - b)
      if (next.join() !== cur.join()) body.libraries = next
    }
    if (!Object.keys(body).length) return
    try {
      await api('/api/users/' + u.id, { method: 'PATCH', body })
      toast('已保存')
      if (u.id === ctx.user?.id && (body.name || body.role)) { location.reload(); return }
      draw()
    } catch (e) { toastError(e) }
  }
  async function remove(u) {
    if (!await confirmDialog({ title: `删除“${u.name}”的账号？`, message: '他在所有设备上的登录都会退出，他的阅读进度、书签笔记和阅读统计也会删掉。删除前书库会自动备份一次数据库。', ok: '删除', danger: true })) return
    try { await api('/api/users/' + u.id, { method: 'DELETE' }); toast(`已删除“${u.name}”`); draw() } catch (e) { toastError(e) }
  }
  await draw()
  return sec('家人账号', '家里每个人用自己的名字和密码登录：阅读进度、书签笔记、读完状态、评分、阅读统计各算各的。家人只能读书，不能改书目、删书和改设置；可以只让他看某几个书库（比如孩子只看“童书”）。', box)
}

// ---------- 回收站（管理员） ----------
async function trashSec(ctx) {
  const box = h('div', { class: 'stack', style: { gap: '8px' } })
  async function draw() {
    let t
    try { t = await api('/api/trash') } catch (e) { fill(box, h('p', { class: 'muted' }, e.message)); return }
    const days = at => Math.max(0, Math.ceil(((at || 0) - Date.now() / 1000) / 86400))
    fill(box,
      t.books.length ? t.books.map(b => h('div', { class: 'dev-row trash-row', dataset: { id: b.id } },
        h('div', { class: 'mini-cv' }, coverEl({ ...b, formats: '' }, apiUrl, { flags: false })),
        h('div', { class: 'grow' }, h('div', null, b.title), h('small', null, [b.author, `${fmtTime(b.deleted_at)} 删除`, `${days(b.expires_at)} 天后彻底删除`].filter(Boolean).join(' · '))),
        h('button', { class: 'btn small', type: 'button', onclick: async e => {
          const btn = e.currentTarget
          btn.disabled = true
          try { const r = await api(`/api/books/${b.id}/restore`, { method: 'POST' }); toast(`《${b.title}》${r.message}`); ctx.refreshFacets(); draw() } catch (err) { toastError(err); btn.disabled = false }
        } }, icon('undo'), '恢复'))) : h('p', { class: 'muted small-text', style: { margin: 0 } }, '回收站是空的。'),
      t.books.length || t.files ? h('div', { class: 'row wrap', style: { marginTop: '6px' } },
        h('span', { class: 'muted small-text grow' }, `回收站文件夹里共 ${t.files} 个文件，占 ${fmtBytes(t.size)}`),
        h('button', { class: 'btn small danger', type: 'button', onclick: async () => {
          if (!await confirmDialog({ title: '清空回收站？', message: `${t.books.length} 本书和回收站文件夹里的 ${t.files} 个文件（${fmtBytes(t.size)}）会彻底删除，大家在这些书上的进度和笔记也一起删掉，不能撤销。`, ok: '彻底删除', danger: true })) return
          try { const r = await api('/api/trash', { method: 'DELETE' }); toast(`已清空回收站（${r.books} 本书，${r.count} 项）`); ctx.refreshFacets(); draw() } catch (e) { toastError(e) }
        } }, '清空回收站')) : null)
  }
  await draw()
  return sec('回收站', '删除的书先放进各书库的 _回收站 文件夹，30 天后自动彻底删除。恢复时文件搬回原来的位置，大家的阅读进度和笔记都还在。', box)
}

// ---------- 存储与状态（管理员） ----------
const KIND_NAME = { arc: '漫画解压', pages: '漫画页面', conv: '格式转换', covers: '封面小图', text: '提取的正文' }
const LIMITS = [[1024, '1 GB'], [2048, '2 GB'], [5120, '5 GB'], [10240, '10 GB'], [20480, '20 GB'], [51200, '50 GB']]

async function storageSec(ctx) {
  const box = h('div', { class: 'stack storage' })
  const usageBar = (used, total) => h('div', { class: 'bar big', role: 'img', 'aria-label': total ? `已用 ${Math.round(used / total * 100)}%` : '' }, h('i', { style: { width: total ? Math.min(100, used / total * 100).toFixed(1) + '%' : '0' } }))
  async function clean(kind, btn) {
    if (btn) btn.disabled = true
    try { const r = await api('/api/storage/clean', { method: 'POST', body: { kind } }); toast(`已清理 ${fmtBytes(r.freed)}`); draw() } catch (e) { toastError(e); if (btn) btn.disabled = false }
  }
  async function draw() {
    let st
    try { st = await api('/api/storage') } catch (e) { fill(box, h('p', { class: 'muted' }, e.message)); return }
    const limit = st.cache_limit_mb * 1048576
    const limitSel = h('select', { class: 'input', 'aria-label': '缓存上限', style: { width: 'auto' } },
      [...new Set([...LIMITS.map(x => x[0]), st.cache_limit_mb])].sort((a, b) => a - b).map(v => h('option', { value: v, selected: v === st.cache_limit_mb }, LIMITS.find(x => x[0] === v)?.[1] || `${Math.round(v / 1024 * 10) / 10} GB`)))
    limitSel.onchange = async () => { try { await api('/api/storage/clean', { method: 'POST', body: { limit_mb: +limitSel.value } }); toast('缓存上限已改好，超出的部分会自动清理'); draw() } catch (e) { toastError(e) } }
    const up = st.update || {}
    const used = st.data_total ? st.data_total - st.data_free : 0
    const cleanBtn = (key, size) => size ? h('button', { class: 'btn small quiet', type: 'button', onclick: e => clean(key, e.currentTarget) }, '清理') : h('span', { class: 'spacer' })
    fill(box,
      h('div', { class: 'st-block' },
        h('div', { class: 'st-head' }, icon('hdd'), h('b', null, '数据目录'), h('code', null, st.data_dir), h('span', { class: 'grow' }), h('span', { class: 'muted small-text' }, st.data_total ? `剩余 ${fmtBytes(st.data_free)} / 共 ${fmtBytes(st.data_total)}` : '')),
        st.data_total ? usageBar(used, st.data_total) : null,
        st.data_total && st.data_free < 2 * 1073741824 ? h('div', { class: 'notice bad' }, h('div', null, h('b', null, '剩余空间不多了'), '。可以清理下面的缓存，或者把缓存上限调小。')) : null,
        h('div', { class: 'st-list' },
          [['数据库（进度、笔记、书目）', st.db], ['全文索引', st.fulltext], ['封面', st.covers], ['备份', st.backups]].map(([l, v]) => h('div', null, h('span', null, l), h('b', null, fmtBytes(v)), h('span', { class: 'spacer' }))))),
      h('div', { class: 'st-block' },
        h('div', { class: 'st-head' }, icon('folder'), h('b', null, '书库目录')),
        h('div', { class: 'st-list' }, st.libraries.map(l => h('div', null,
          h('span', null, l.name, h('code', null, l.path)),
          l.exists ? h('b', null, l.total ? `剩余 ${fmtBytes(l.free)}` : '') : h('span', { class: 'badge warn' }, '目录不存在'), h('span', { class: 'spacer' }))))),
      h('div', { class: 'st-block' },
        h('div', { class: 'st-head' }, icon('archive'), h('b', null, '缓存'), h('span', { class: 'grow' }), h('span', { class: 'muted small-text' }, `${fmtBytes(st.cache_total)} / 上限 ${fmtBytes(limit)}`)),
        usageBar(st.cache_total, limit),
        h('div', { class: 'st-list' },
          st.caches.map(c => h('div', { dataset: { cache: c.key } }, h('span', null, KIND_NAME[c.key] || c.name), h('b', null, fmtBytes(c.size)), cleanBtn(c.key, c.size))),
          h('div', { dataset: { cache: 'tts' } }, h('span', null, '听书语音（单独限 400 MB）'), h('b', null, fmtBytes(st.tts)), cleanBtn('tts', st.tts))),
        h('div', { class: 'row wrap' }, h('span', { class: 'muted small-text' }, '缓存上限'), limitSel, h('span', { class: 'grow' }),
          h('button', { class: 'btn small', type: 'button', onclick: async e => { const btn = e.currentTarget; if (await confirmDialog({ title: '清理全部缓存？', message: '漫画解压、转换好的格式、封面小图、提取的正文和听书语音都会删掉。不影响书和进度，之后用到时会重新生成（第一次打开会慢一点）。', ok: '清理' })) clean('all', btn) } }, '全部清理'))),
      h('div', { class: 'st-block' },
        h('div', { class: 'st-head' }, icon('activity'), h('b', null, '状态')),
        h('div', { class: 'st-list' },
          h('div', null, h('span', null, '回收站'), h('b', null, `${st.trash_books} 本书`), st.trash_books ? h('button', { class: 'btn small quiet', type: 'button', onclick: () => document.getElementById('set-trash')?.scrollIntoView({ behavior: 'smooth' }) }, '查看') : h('span', { class: 'spacer' })),
          h('div', null, h('span', null, '找不到文件的书'), h('b', { class: st.missing_books ? 'warnish' : '' }, `${st.missing_books} 本`), st.missing_books ? h('a', { class: 'btn small quiet', href: '#/all?missing=1' }, '查看') : h('span', { class: 'spacer' })),
          h('div', null, h('span', null, '自动升级服务'), h('b', { class: up.configured && !up.reachable ? 'warnish' : '' }, !up.configured ? '没有配置' : up.reachable ? '正常' : '连不上'), h('span', { class: 'spacer' }))),
        up.configured && !up.reachable ? h('p', { class: 'muted small-text' }, `连不上 ${up.url}：在 Container Station 里确认 taihaoshuo-updater 在运行。`) : null),
      h('div', { class: 'st-block' },
        h('div', { class: 'st-head' }, icon('sparkle'), h('b', null, '最近的问题'), h('span', { class: 'grow' }), h('a', { class: 'btn small quiet', href: '#/organize?tab=events' }, '全部动态')),
        st.recent_problems.length ? h('ul', { class: 'events compact' }, st.recent_problems.map(e => h('li', { class: e.level }, h('time', null, fmtTime(e.created_at)), h('span', null, e.message))))
          : h('p', { class: 'muted small-text', style: { margin: 0 } }, '最近没有错误和警告。')),
      h('div', null, h('button', { class: 'btn small quiet', type: 'button', onclick: draw }, icon('refresh'), '刷新')))
  }
  await draw()
  return sec('存储与状态', '书库在 NAS 上占了多少地方、各种缓存有多大、最近有没有出错。缓存（漫画解压、格式转换、封面小图等）超过上限时，最久没用的会自动删掉。', box)
}

// ---------- 备份（管理员） ----------
const BACKUP_KIND = { daily: '每日', upgrade: '升级前', manual: '手动', delete: '删除前', restore: '恢复前' }

async function backupSec(ctx) {
  const box = h('div', { class: 'stack', style: { gap: '8px' } })
  const where = h('small', { class: 'muted' })
  const file = h('input', { type: 'file', accept: '.db,application/vnd.sqlite3,application/x-sqlite3,application/octet-stream', hidden: true })
  file.onchange = async () => {
    const f = file.files[0]
    file.value = ''
    if (!f) return
    if (!await confirmDialog({ title: `用“${f.name}”恢复？`, message: '书库会换成这个文件里的数据：阅读进度、笔记、书目、账号和设置都回到备份时的样子。现在的数据库会先另存一份“恢复前”的备份。书的文件不受影响。恢复后书库自动重启。', ok: '恢复', danger: true })) return
    restore(() => api('/api/backups/restore', { method: 'POST', body: f }))
  }
  const nowBtn = h('button', { class: 'btn', type: 'button' }, icon('archive'), '立即备份')
  nowBtn.onclick = async () => {
    nowBtn.disabled = true
    try { const r = await api('/api/backups', { method: 'POST' }); toast('已备份：' + r.name); draw() } catch (e) { toastError(e) }
    nowBtn.disabled = false
  }
  async function draw() {
    let r
    try { r = await api('/api/backups') } catch (e) { fill(box, h('p', { class: 'muted' }, e.message)); return }
    where.textContent = `备份放在 ${r.dir}。只备份数据库（进度、笔记、书目、账号、设置）：书的文件不在里面；全文索引能重建，也不备份。`
    fill(box, r.backups.length ? r.backups.map(b => h('div', { class: 'dev-row backup-row', dataset: { name: b.name } },
      icon('database'),
      h('div', { class: 'grow' }, h('div', null, fmtDateTime(b.created_at), h('span', { class: 'badge', style: { marginLeft: '8px' } }, BACKUP_KIND[b.kind] || b.kind)),
        h('small', null, fmtBytes(b.size))),
      h('a', { class: 'icon-btn', href: apiUrl('/api/backups/' + encodeURIComponent(b.name)), download: b.name, title: '下载', 'aria-label': '下载这个备份' }, icon('download')),
      h('button', { class: 'btn small', type: 'button', onclick: async () => {
        if (!await confirmDialog({ title: '从这个备份恢复？', message: `书库会回到 ${fmtDateTime(b.created_at)} 的样子：之后的阅读进度、笔记、新登记的书和设置改动都会丢失（书的文件不受影响，下次扫描会重新登记）。现在的数据库会先另存一份“恢复前”的备份。恢复后书库自动重启，大约半分钟。`, ok: '恢复', danger: true })) return
        restore(() => api('/api/backups/restore?name=' + encodeURIComponent(b.name), { method: 'POST' }))
      } }, '恢复'),
      h('button', { class: 'icon-btn', type: 'button', title: '删除', 'aria-label': '删除这个备份', onclick: async () => {
        if (!await confirmDialog({ title: '删除这个备份？', message: `${fmtDateTime(b.created_at)}（${BACKUP_KIND[b.kind] || b.kind}）`, ok: '删除', danger: true })) return
        try { await api('/api/backups/' + encodeURIComponent(b.name), { method: 'DELETE' }); draw() } catch (e) { toastError(e) }
      } }, icon('trash')))) : h('p', { class: 'muted small-text', style: { margin: 0 } }, '还没有备份。书库里有书以后，每天会自动备份一次。'))
  }
  await draw()
  return sec('备份', '每天自动备份一次数据库，留最近 7 份；升级前也会自动备份一份。恢复以后书库会自动重启。',
    h('div', { class: 'row wrap', style: { marginBottom: '12px' } }, nowBtn, h('button', { class: 'btn quiet', type: 'button', onclick: () => file.click() }, icon('upload'), '用备份文件恢复…'), file),
    box, h('div', { style: { marginTop: '10px' } }, where))
}

// 恢复：服务器检查好文件后重启，这里等它回来再刷新页面
async function restore(send) {
  let r
  try { r = await send() } catch (e) { toastError(e); return }
  if (!r.restart) { toast('已准备好恢复：在 Container Station 里重启书库后生效', { duration: 10000 }); return }
  const msg = h('p', null, '正在换上备份的数据，书库重启中……')
  const veil = h('div', { class: 'restart-veil', role: 'alertdialog', 'aria-live': 'assertive' }, h('div', null, h('h3', null, '书库正在重启'), msg))
  document.body.append(veil)
  const t0 = Date.now()
  let wentDown = false
  while (Date.now() - t0 < 180000) {
    await new Promise(res => setTimeout(res, 1500))
    try {
      const ctl = new AbortController()
      const tm = setTimeout(() => ctl.abort(), 2500)
      const res = await fetch((net.base || '') + '/api/ping', { cache: 'no-store', signal: ctl.signal })
      clearTimeout(tm)
      if (res.ok && (wentDown || Date.now() - t0 > 8000)) { msg.textContent = '已恢复，正在刷新……'; setTimeout(() => location.reload(), 600); return }
    } catch { wentDown = true }
  }
  msg.textContent = '书库很久没有回来：在 Container Station 里看看书库的应用程序是不是在运行（restart 要设成 unless-stopped）。'
}

// ---------- 关于 ----------
// ---------- 版本与升级 ----------
// 服务器由 NAS 上的更新服务（Watchtower）自动升级；手机、手表 App 和插件的新安装包跟着服务器镜像一起到，从书库下载
const newer = (a, b) => {
  const pa = String(a || '').split('.').map(Number), pb = String(b || '').split('.').map(Number)
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) { const d = (pa[i] || 0) - (pb[i] || 0); if (d) return d > 0 }
  return false
}

function updateSec(s, ctx) {
  const box = h('div', { class: 'stack', style: { gap: '8px' } }, h('p', { class: 'muted small-text', style: { margin: 0 } }, '正在读取版本信息…'))
  const row = (ic, label, sub, ...actions) => h('div', { class: 'dev-row' }, icon(ic), h('div', { class: 'grow' }, h('div', null, label), sub ? h('small', null, sub) : null), ...actions)
  const link = (item, text) => h('a', { class: 'btn small quiet', href: (net.base || '') + item.url, download: '' }, icon('download'), text)
  ;(async () => {
    let info
    try { info = await api('/api/update') } catch (e) { fill(box, h('p', { class: 'muted small-text' }, e.message)); return }
    const c = info.clients || {}
    const msg = h('div', { class: 'muted small-text' }, info.updater
      ? 'NAS 上的更新服务每分钟检查一次，发布新版本后自动升级。'
      : '还没开启自动升级：按说明用新的 compose（带更新服务的地址和令牌）重建一次书库。')
    const btn = ctx.isAdmin ? h('button', { class: 'btn small' }, icon('refresh'), '检查更新') : null
    if (btn) btn.onclick = () => checkServer(btn, msg, info.version)
    const rows = [row('stack', `书库服务器 ${info.version}`, null, btn), ctx.isAdmin ? msg : null].filter(Boolean)
    const app = isNative ? callJSON('appInfo') : null
    // 加上在线升级之前的 App 没有 appInfo，也不会自己更新：交给手机浏览器下载新版，装一次以后就能自动更新
    const oldApp = isNative && !app
    const viaBrowser = (item, text) => h('button', { class: 'btn small primary', onclick: () => call('openExternal', (net.base || location.origin) + item.url) }, icon('download'), text)
    if (c.android) {
      if (app) {
        const old = (app.versionCode || 0) < c.android.versionCode
        rows.push(row('phone', '手机 App', `本机 ${app.versionName || ''}，最新 ${c.android.versionName}`,
          old ? h('button', { class: 'btn small primary', onclick: () => call('updateApp') }, '更新') : h('span', { class: 'muted small-text' }, '已是最新')))
      } else if (oldApp) {
        rows.push(row('phone', '手机 App', `最新 ${c.android.versionName}。现在这个 App 还不会自己更新：点“下载新版”，用手机浏览器下载后装上（直接覆盖，书和设置都保留），以后就能自动更新`, viaBrowser(c.android, '下载新版')))
      } else rows.push(row('phone', '安卓手机 App', `最新 ${c.android.versionName}。手机浏览器打开这一页点下载，装好后 App 会自己检查更新`, link(c.android, '下载')))
    }
    if (c.wear) {
      rows.push(app
        ? row('watch', '手表 App', `最新 ${c.wear.versionName}。点“更新手表”会下载安装包，再交给 Wear OS 工具箱装到手表上`, h('button', { class: 'btn small', onclick: () => call('updateWatchApp') }, '更新手表'))
        : oldApp
          ? row('watch', '手表 App', `最新 ${c.wear.versionName}。用手机浏览器下载后，用 Wear OS 工具箱装到手表上（手机 App 更新以后可以直接点“更新手表”）`, viaBrowser(c.wear, '下载'))
          : row('watch', 'Wear OS 手表 App', `最新 ${c.wear.versionName}。用 Wear OS 工具箱或电脑的 adb 安装`, link(c.wear, '下载')))
    }
    if (c.extension) rows.push(row('puzzle', '浏览器插件', `最新 ${c.extension.version}。插件发现新版本时，图标上会显示“新”，按插件里的提示更新`, link(c.extension, '下载')))
    if (!c.android && !c.wear && !c.extension) rows.push(h('p', { class: 'muted small-text', style: { margin: 0 } }, '这个书库里没有各端安装包（自己构建的镜像不带），可以到 GitHub 的 Releases 下载。'))
    fill(box, ...rows)
  })()
  return sec('版本与升级', '服务器、网页自动升级；手机、手表 App 和浏览器插件从这里取新版本。', box)
}

async function checkServer(btn, msg, cur) {
  btn.disabled = true
  msg.textContent = '正在检查新版本……有新版本时会自动下载并重启书库，大约一两分钟。'
  try {
    // 能收到回复就说明书库没被换掉：已是最新，或者更新服务有问题（原因在回复里）
    const r = await api('/api/update/check', { method: 'POST', retry: false })
    msg.textContent = r.reason; btn.disabled = false; return
  } catch (e) {
    if (e.status) { msg.textContent = e.message; btn.disabled = false; return }
    // 连接断开：书库正在被换成新版本
  }
  msg.textContent = '书库正在升级重启，稍等……'
  for (let i = 0; i < 90; i++) {
    await new Promise(r => setTimeout(r, 2000))
    try {
      const r = await api('/api/clients', { retry: false })
      if (r.server && r.server !== cur) { msg.textContent = `已升级到 ${r.server}，正在刷新……`; setTimeout(() => location.reload(), 800); return }
      if (i > 15 && r.server === cur) { msg.textContent = '已经是最新版本'; btn.disabled = false; return }
    } catch {}
  }
  msg.textContent = '书库还没重启好，过一会儿刷新页面看看'
  btn.disabled = false
}

function about(s) {
  const t = s.tools || {}
  const line = (ok, label, hint) => h('div', { class: 'status-line' }, h('span', { class: 'dot ' + (ok ? 'ok' : '') }), label, ok ? null : h('span', { class: 'muted small-text' }, '　' + hint))
  return sec('关于', `书库 ${s.version}。EPUB、MOBI、AZW3、FB2、PDF、TXT、CBZ、CBR 不需要额外组件就能读。`,
    h('div', { class: 'stack', style: { gap: '8px' } },
      line(t.calibre, 'Calibre 格式转换（DOCX、CHM、RTF 等）', '镜像里没装'),
      line(t.djvu, 'DJVU 支持', '镜像里没装'),
      line(t['7z'], '7z / CB7 漫画包', '镜像里没装'),
      line(t.dict, '本地字典（选词查字典：中文、英文）', '镜像里没装，查字典时只能用 AI'),
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

// ---------- 听书（家人）：引擎由管理员设置，这里只能试听 ----------
function narrationMember(s) {
  const t = s.tts || {}
  const status = h('span', { class: 'muted small-text' })
  let audio
  const engine = { edge: '微软自然语音', openai: '自定义语音接口', browser: '设备自带语音' }[t.engine] || '微软自然语音'
  const btn = h('button', { class: 'btn', type: 'button' }, icon('play'), '试听')
  btn.onclick = async () => {
    btn.disabled = true
    status.textContent = '正在合成…'
    try {
      if (t.engine === 'browser') {
        const u = new SpeechSynthesisUtterance('夜色渐深，城市的灯一盏一盏亮了起来。')
        u.lang = 'zh-CN'; speechSynthesis.cancel(); speechSynthesis.speak(u)
      } else {
        const res = await api('/api/tts', { method: 'POST', raw: true, body: { text: '夜色渐深，城市的灯一盏一盏亮了起来。他合上书，听见窗外的雨声。' } })
        audio?.pause(); audio = new Audio(URL.createObjectURL(await res.blob())); audio.play()
      }
      status.textContent = ''
    } catch (err) { status.textContent = err.message }
    btn.disabled = false
  }
  return sec('听书', `阅读器里点耳机图标开始朗读，音色和语速可以在阅读器里换。现在用的语音：${engine}（由管理员设置）。`,
    h('div', { class: 'row' }, btn, status))
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
function opdsSec(s, ctx) {
  const urls = [location.origin + '/opds']
  const remote = s.network?.remote_url
  if (remote && !remote.startsWith(location.origin)) urls.push(remote.replace(/\/$/, '') + '/opds')
  return sec('用其他阅读器访问（OPDS）', `在 KOReader、静读天下、Librera、Panels、Yomu 等支持 OPDS 的阅读器里添加书库，就能直接浏览、搜索和下载。用户名填你在书库里的名字（${ctx.user?.name || '管理员'}），密码是你的书库密码。`,
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
// 连接手表要联网、等蓝牙，原生在后台做完后发 watch 事件
function syncWatch(btn) {
  btn.disabled = true
  toast('正在把连接信息发给手表…')
  const off = onNative('watch', r => { off(); clearTimeout(t); btn.disabled = false; toast(r.message || (r.ok ? '已把连接信息发给手表' : '连接手表失败'), r.ok ? {} : { error: true, duration: 6000 }) })
  const t = setTimeout(() => { off(); btn.disabled = false }, 30000)
  call('syncWatch')
}

// 下载到手机的书：新版 App 有 downloadInfo（总占用、上限），旧版只有 downloads
function phoneDownloads() {
  const info = callJSON('downloadInfo')
  if (info && Array.isArray(info.items)) return info
  const items = callJSON('downloads') || []
  return { items, total_size: items.reduce((a, d) => a + (d.size || 0), 0), limit_mb: (callJSON('prefs') || {}).download_limit_mb || 0 }
}

const DL_LIMITS = [[0, '不限'], [512, '512 MB'], [1024, '1 GB'], [2048, '2 GB'], [5120, '5 GB'], [10240, '10 GB'], [20480, '20 GB']]

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
  const limitMb = +(prefs.download_limit_mb || 0)
  const limitOpts = [...new Map([...DL_LIMITS, [limitMb, `${limitMb} MB`]])].sort((a, b) => a[0] - b[0])
  const limitSel = h('select', { class: 'input', style: { width: 'auto' }, 'aria-label': '下载到手机最多占用' },
    limitOpts.map(([v, l]) => h('option', { value: v, selected: v === limitMb }, l)))
  limitSel.onchange = () => {
    call('setPref', 'download_limit_mb', String(limitSel.value))
    draw()
    toast(+limitSel.value ? '超过上限时，先删最早下载、已经读完的书' : '不限制下载占用的空间')
  }
  const dl = h('div', { class: 'stack', style: { gap: '6px' } })
  const draw = () => {
    refreshDownloads()
    const info = phoneDownloads()
    const list = info.items || []
    const limit = +(info.limit_mb ?? limitSel.value) || 0
    fill(dl, list.length ? list.map(d => h('div', { class: 'dev-row' + (d.state === 'error' ? ' err' : '') }, icon('book'),
      h('div', { class: 'grow' }, h('div', null, d.title),
        h('small', null, d.state === 'done' ? `${(d.format || '').toUpperCase()} · ${fmtBytes(d.size)}` : d.state === 'error' ? '下载失败：' + (d.error || '原因不明') : `下载中 ${Math.round((d.progress || 0) * 100)}%`)),
      d.state === 'error' ? h('button', { class: 'btn small', type: 'button', onclick: async e => {
        e.currentTarget.disabled = true
        // 和书的详情里“下载到手机”一样的参数；连不上书库时用下载记录里的信息（已经下好的部分会跳过）
        if (net.route === 'offline') call('download', d.id, { id: d.id, title: d.title, author: d.author || '' })
        else await downloadToPhone(d.id)
        setTimeout(draw, 300)
      } }, '重试') : null,
      h('button', { class: 'btn small quiet danger', type: 'button', onclick: () => { call('removeDownload', d.id); draw() } }, '删除'))) : h('p', { class: 'muted small-text', style: { margin: 0 } }, '还没有下载到手机的书。在书的详情里点“下载到手机”，没网的时候也能读。'),
    list.length || limit ? h('div', { class: 'muted small-text' }, `共占用 ${fmtBytes(info.total_size || 0)}${limit ? `，上限 ${fmtBytes(limit * 1048576)}` : ''}`) : null)
  }
  draw()
  const off = onNative('download', draw)
  setTimeout(() => { const t = setInterval(() => { if (!document.body.contains(dl)) { off(); clearInterval(t) } }, 2000) }, 0)
  return sec('手机 App', `书库 App ${call('version') || ''}。`,
    h('div', { class: 'stack', style: { gap: '4px' } },
      sw('volume_keys', '音量键翻页', '阅读时按音量键上一页、下一页'),
      sw('keep_screen_on', '阅读时屏幕常亮'),
      sw('auto_download', '自动下载在读的书', '连着内网时，把“在读”里的书下载到手机'),
      sw('auto_clean_finished', '读完的书自动从手机删掉', '只删下载以后才读完的书（打开 App、连上书库时检查）；以前就读完、专门下回来重温的书不会删'),
      h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '下载到手机最多占用'), h('div', { class: 'h' }, '超过时先删最早下载、已经读完的书')), limitSel),
      h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '听书的声音'), h('div', { class: 'h' }, '锁屏、切到别的 App 也会继续读')), ttsSel),
      h('div', { class: 'row wrap', style: { margin: '8px 0 14px' } },
        h('button', { class: 'btn', onclick: e => syncWatch(e.currentTarget) }, icon('watch'), '连接手表'),
        h('button', { class: 'btn quiet', onclick: async () => { if (await confirmDialog({ title: '断开这台手机？', message: '下载到手机的书会保留。之后需要重新扫码或输入配对码。', ok: '断开' })) call('logout') } }, '断开并重新配对')),
      h('div', { class: 'sub-title', style: { margin: '6px 0 6px' } }, '下载到手机的书'),
      dl))
}
