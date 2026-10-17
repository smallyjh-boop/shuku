// 拖拽 / 选择文件上传到投递目录，服务器会自动识别、改名、归类

import { h, clear, icon, iconBtn, toast, fmtBytes, pct } from './ui.js'
import { uploadFile } from './api.js'

const EXT = /\.(epub|kepub|mobi|azw3?|prc|fb2|fbz|txt|pdf|djvu?|cb[zrt7]|zip|rar|7z|docx?|rtf|odt|html?|md|chm|lit|pdb|lrf|snb|umd)$/i
const queue = []
let active = 0, panel, listEl, headEl, ctxRef, doneCount = 0, failCount = 0

export function initUploads(ctx) {
  ctxRef = ctx
  if (document.body.dataset.uploadsReady) return
  document.body.dataset.uploadsReady = '1'
  const veil = h('div', { class: 'drop-veil' }, h('div', null, h('b', null, '松开即可加入书库'), h('span', null, '支持整个文件夹。书名、作者、分类会自动整理')))
  document.body.append(veil)
  let depth = 0
  const inReader = () => !!document.querySelector('.reader')
  const hasFiles = e => [...(e.dataTransfer?.types || [])].includes('Files')
  addEventListener('dragenter', e => { if (!hasFiles(e) || inReader()) return; e.preventDefault(); depth++; veil.classList.add('show') })
  addEventListener('dragleave', e => { if (!hasFiles(e)) return; depth = Math.max(0, depth - 1); if (!depth) veil.classList.remove('show') })
  addEventListener('dragover', e => { if (hasFiles(e) && !inReader()) e.preventDefault() })
  addEventListener('drop', async e => {
    if (!hasFiles(e) || inReader()) return
    e.preventDefault(); depth = 0; veil.classList.remove('show')
    const items = [...e.dataTransfer.items].map(i => i.webkitGetAsEntry?.()).filter(Boolean)
    const files = []
    if (items.length) for (const ent of items) await walk(ent, '', files)
    else for (const f of e.dataTransfer.files) files.push([f, f.name])
    enqueue(files)
  })
}

async function walk(entry, prefix, out) {
  if (entry.isFile) {
    const f = await new Promise((res, rej) => entry.file(res, rej)).catch(() => null)
    if (f) out.push([f, prefix + f.name])
    return
  }
  if (!entry.isDirectory) return
  const reader = entry.createReader()
  for (;;) {
    const batch = await new Promise(res => reader.readEntries(res, () => res([])))
    if (!batch.length) break
    for (const e of batch) await walk(e, prefix + entry.name + '/', out)
  }
}

export function pickFiles() {
  const input = h('input', { type: 'file', multiple: true, accept: '.epub,.kepub,.mobi,.azw,.azw3,.prc,.fb2,.fbz,.txt,.pdf,.djvu,.cbz,.cbr,.cb7,.cbt,.zip,.rar,.7z,.docx,.doc,.rtf,.odt,.html,.htm,.md,.chm,.umd' })
  input.onchange = () => enqueue([...input.files].map(f => [f, f.webkitRelativePath || f.name]))
  input.click()
}

function enqueue(files) {
  const ok = files.filter(([f, p]) => EXT.test(p) && !p.split('/').some(s => s.startsWith('.')))
  const skipped = files.length - ok.length
  if (!ok.length) { toast(skipped ? '这些文件不是支持的电子书格式' : '没有找到文件', { error: !!skipped }); return }
  if (skipped) toast(`跳过了 ${skipped} 个不支持的文件`)
  ensurePanel()
  for (const [file, path] of ok) {
    const row = { file, path, li: h('li', null, h('div', { class: 'n' }, h('span', { title: path }, path.split('/').pop()), h('span', { class: 's' }, '等待'))) }
    queue.push(row)
    listEl.append(row.li)
  }
  updateHead()
  pump()
}

function ensurePanel() {
  if (panel && document.body.contains(panel)) return
  listEl = h('ul')
  headEl = h('span')
  panel = h('div', { class: 'uploads', role: 'status' }, h('header', null, icon('upload'), headEl, iconBtn('close', '收起', () => { panel.remove(); panel = null })), listEl)
  document.body.append(panel)
  doneCount = 0; failCount = 0
}

function updateHead() {
  if (!headEl) return
  const left = queue.length + active
  headEl.textContent = left ? `正在上传 ${left} 个文件` : `已上传 ${doneCount} 个${failCount ? `，${failCount} 个失败` : ''}，正在识别入库`
}

function pump() {
  while (active < 3 && queue.length) {
    const row = queue.shift()
    active++
    const st = row.li.querySelector('.s')
    uploadFile(row.file, row.path, p => (st.textContent = pct(p)))
      .then(() => { row.li.className = 'ok'; st.textContent = fmtBytes(row.file.size); doneCount++ })
      .catch(e => { row.li.className = 'err'; st.textContent = e.message; failCount++ })
      .finally(() => {
        active--; updateHead(); pump()
        if (!active && !queue.length) {
          // 上传完，入库结果会以提示的形式陆续出现
          setTimeout(() => ctxRef?.refreshFacets(), 3000)
          setTimeout(() => { if (panel && !queue.length && !active && !failCount) { panel.remove(); panel = null } }, 12000)
        }
      })
  }
}
