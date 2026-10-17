// 设置页：用配对码（或密码）连接书库、修改内网 / Tailscale 地址、选择加入方式。

import { getConfig, saveConfig, isConfigured, pair, logout, resolveRoute, routeLabel, norm } from './lib/shuku.js'

const $ = id => document.getElementById(id)

function say(el, text, cls = '') {
  $(el).textContent = text
  $(el).className = 'msg ' + cls
}

async function render() {
  const c = await getConfig()
  const linked = isConfigured(c)
  $('pair').hidden = linked
  $('linked').hidden = !linked
  $('state').hidden = !linked
  if (linked) {
    $('lan').value = c.lan || ''
    $('remote').value = c.remote || ''
    showState(c, null, true)
    const r = await resolveRoute(true)
    showState(c, r, false)
  } else {
    if (!$('addr').value) $('addr').value = (c.lan || c.remote || '').replace(/^http:\/\//, '')
  }
  for (const r of document.querySelectorAll('input[name=mode]')) r.checked = r.value === (c.mode || 'auto')
}

function showState(c, r, checking) {
  const box = $('state')
  box.replaceChildren()
  const b = document.createElement('b')
  b.textContent = c.name || '书库'
  const s = document.createElement('span')
  if (checking) { s.textContent = '正在检查连接…'; s.className = 'muted' }
  else if (r) { s.textContent = `已连接，现在走${routeLabel(r)}（${r.base}）`; s.className = 'ok' }
  else { s.textContent = '已配对，但现在连不上：内网和 Tailscale 地址都不通'; s.className = 'err' }
  box.append(b, s)
}

$('pair').addEventListener('submit', async e => {
  e.preventDefault()
  const addr = $('addr').value
  const code = $('code').value.trim().toUpperCase()
  const pwd = $('pwd').value
  if (!addr.trim()) return say('pair-msg', '请填写书库地址', 'err')
  if (!code && !pwd) return say('pair-msg', '请填写配对码（或者展开下面用密码登录）', 'err')
  say('pair-msg', '正在连接…', 'muted')
  const btn = e.submitter
  if (btn) btn.disabled = true
  try {
    const c = await pair(addr, code, code ? '' : pwd)
    $('code').value = ''
    $('pwd').value = ''
    say('pair-msg', '')
    await render()
    say('linked-msg', `已连接到“${c.name}”`, 'ok')
  } catch (err) {
    say('pair-msg', err.message || String(err), 'err')
  } finally {
    if (btn) btn.disabled = false
  }
})

$('save').onclick = async () => {
  const lan = norm($('lan').value), remote = norm($('remote').value)
  if (!lan && !remote) return say('linked-msg', '至少填一个地址', 'err')
  await saveConfig({ lan, remote })
  say('linked-msg', '已保存，正在检查…', 'muted')
  await render()
  say('linked-msg', '已保存', 'ok')
}

$('test').onclick = async () => {
  say('linked-msg', '正在检查…', 'muted')
  const c = await getConfig()
  const r = await resolveRoute(true)
  showState(c, r, false)
  say('linked-msg', r ? `能连上，走${routeLabel(r)}` : '连不上', r ? 'ok' : 'err')
}

$('logout').onclick = async () => {
  if (!confirm('断开后需要重新输入配对码才能加入书。确定断开？')) return
  await logout()
  say('linked-msg', '')
  await render()
  say('pair-msg', '已断开', 'muted')
}

for (const r of document.querySelectorAll('input[name=mode]')) {
  r.onchange = () => r.checked && saveConfig({ mode: r.value })
}

$('version').textContent = '版本 ' + chrome.runtime.getManifest().version
render()
