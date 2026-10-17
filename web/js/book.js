// 书籍详情：信息、格式、编辑、确认、重新识别、删除

import { h, clear, fill, add, icon, coverEl, openDrawer, toast, toastError, confirmDialog, fmtBytes, pct } from './ui.js'
import { api, apiUrl, net } from './api.js'
import { isNative, call, onNative, refreshDownloads } from './native.js'

export async function openBook(id, ctx) {
  const d = openDrawer({ title: '书籍详情', body: h('div', { class: 'muted' }, '加载中…') })
  let b
  try { b = await api('/api/books/' + id) } catch (e) { fill(d.body, h('p', null, e.message)); return }
  // App 里：下载到手机，离线也能读
  const dlBtn = isNative ? h('button', { class: 'btn', type: 'button' }) : null
  function drawDl() {
    if (!dlBtn) return
    const st = refreshDownloads().find(x => x.id === b.id)
    dlBtn.disabled = st?.state === 'downloading'
    fill(dlBtn, icon('download'), !st || st.state === 'error' ? (st ? '重新下载' : '下载到手机') : st.state === 'done' ? '已下载 · 删除' : `下载中 ${Math.round((st.progress || 0) * 100)}%`)
    dlBtn.onclick = () => {
      if (st?.state === 'done') { call('removeDownload', b.id); toast('已从手机删除'); drawDl(); return }
      if (net.route === 'offline') { toast('现在连不上书库，联网后再下载', { error: true }); return }
      call('download', b.id, { ...b, files: undefined, description: b.description })
      drawDl()
    }
  }
  if (dlBtn) { const off = onNative('download', drawDl); const t = setInterval(() => { if (!d.panel.isConnected) { off(); clearInterval(t) } }, 3000) }

  render()

  function render() {
    drawDl()
    const cats = ctx.settings?.categories || []
    const f = {}
    const inp = (k, attrs = {}) => (f[k] = h('input', { class: 'input', value: b[k] ?? '', ...attrs }))
    const kind = h('select', { class: 'input' }, h('option', { value: 'book', selected: b.kind === 'book' }, '图书'), h('option', { value: 'comic', selected: b.kind === 'comic' }, '漫画'))
    const cat = h('select', { class: 'input' }, [...new Set([...cats, b.category].filter(Boolean))].map(c => h('option', { value: c, selected: c === b.category }, c)))
    const dir = h('select', { class: 'input' }, h('option', { value: '', selected: b.direction !== 'rtl' }, '从左到右'), h('option', { value: 'rtl', selected: b.direction === 'rtl' }, '从右到左（日漫）'))
    const tags = h('input', { class: 'input', value: (b.tags || []).join('，'), placeholder: '用逗号分隔' })
    const desc = h('textarea', { class: 'input', rows: 4 }, b.description || '')

    const review = b.status === 'review' ? h('div', { class: 'notice' },
      h('div', null, h('b', null, '识别把握不大'), `（${b.source || '规则识别'}，把握 ${pct(b.confidence)}）。文件暂时放在书库的“_待确认”文件夹。核对下面的信息，确认后会按命名规则整理。`),
      h('div', { class: 'muted small-text' }, '原始文件名：' + (b.orig_name || ''))) : null

    const top = h('div', { class: 'detail-top' },
      coverEl({ ...b, formats: '' }, apiUrl, { flags: false }),
      h('div', null,
        h('h2', null, b.title),
        b.orig_title ? h('div', { class: 'orig' }, b.orig_title) : null,
        h('div', { class: 'by' }, [b.author, b.translator ? b.translator + ' 译' : ''].filter(Boolean).join('　')),
        h('div', { class: 'meta' },
          b.category ? h('span', { class: 'tag' }, b.category) : null,
          b.series ? h('a', { class: 'tag', href: '#/series/' + encodeURIComponent(b.series), onclick: () => d.close() }, `${b.series}${b.series_index ? ' · 第 ' + b.series_index + ' 本' : ''}`) : null,
          b.year ? h('span', { class: 'tag' }, b.year + ' 年') : null,
          (b.tags || []).slice(0, 4).map(t => h('span', { class: 'tag' }, t))),
        b.percent > 0 ? h('div', { class: 'muted small-text', style: { marginTop: '10px' } }, b.read_state === 'done' ? '已读完' : `已读 ${pct(b.percent)}`) : null,
        h('div', { class: 'read-row' },
          h('a', { class: 'btn primary', href: '#/read/' + b.id, onclick: () => d.close() }, icon('reading'), b.percent > 0 && b.read_state !== 'done' ? '继续阅读' : '开始阅读'),
          isNative ? dlBtn : null,
          b.author ? h('a', { class: 'btn quiet', href: '#/author/' + encodeURIComponent(b.author), onclick: () => d.close() }, '作者的其他书') : null)))

    const files = h('ul', { class: 'files' }, (b.files || []).map(x => h('li', null,
      h('b', null, x.format.toUpperCase()),
      h('span', { class: 'p', title: x.path }, '‎' + x.path),
      h('span', { class: 'muted' }, fmtBytes(x.size)),
      h('a', { class: 'btn small quiet', href: '#/read/' + b.id + '?file=' + x.id, onclick: () => d.close(), title: '用这个格式阅读' }, '阅读'),
      h('a', { class: 'icon-btn', href: apiUrl(`/api/files/${x.id}/raw?download=1`), title: '下载', 'aria-label': '下载', download: '' }, icon('download')))))

    const form = h('div', { class: 'form-grid' },
      h('label', { class: 'field full' }, h('span', null, '书名'), inp('title')),
      h('label', { class: 'field' }, h('span', null, '作者'), inp('author')),
      h('label', { class: 'field' }, h('span', null, '译者'), inp('translator')),
      h('label', { class: 'field full' }, h('span', null, '原名'), inp('orig_title', { placeholder: '外文书的原书名' })),
      h('label', { class: 'field' }, h('span', null, '系列'), inp('series')),
      h('label', { class: 'field' }, h('span', null, '第几本 / 卷'), inp('series_index', { type: 'number', step: 'any', min: 0, value: b.series_index || '' })),
      h('label', { class: 'field' }, h('span', null, '分类'), cat),
      h('label', { class: 'field' }, h('span', null, '出版年份'), inp('year', { type: 'number', value: b.year || '' })),
      h('label', { class: 'field' }, h('span', null, '类型'), kind),
      b.kind === 'comic' ? h('label', { class: 'field' }, h('span', null, '翻页方向'), dir) : h('label', { class: 'field' }, h('span', null, '出版社'), inp('publisher')),
      h('label', { class: 'field full' }, h('span', null, '标签'), tags),
      h('label', { class: 'field full' }, h('span', null, '简介'), desc))

    const collect = () => ({
      title: f.title.value, author: f.author.value, translator: f.translator.value, orig_title: f.orig_title.value,
      series: f.series.value, series_index: parseFloat(f.series_index.value) || 0, year: parseInt(f.year.value) || 0,
      category: cat.value, kind: kind.value, direction: dir.value, publisher: f.publisher?.value ?? b.publisher,
      tags: tags.value.split(/[,，、]/).map(s => s.trim()).filter(Boolean), description: desc.value,
    })

    let saveIt = async function (confirm) {
      try {
        const r = await api('/api/books/' + b.id, { method: 'PATCH', body: { ...collect(), confirm } })
        b = r.book
        toast(r.message || (confirm ? '已确认，并按命名规则整理好了' : '已保存，文件名和位置已同步更新'), r.message ? { error: true } : {})
        ctx.refreshFacets(); ctx.rerender()
        render()
      } catch (e) { toastError(e) }
    }

    async function identify(btn) {
      btn.disabled = true
      const old = btn.lastChild.textContent
      btn.lastChild.textContent = '识别中…'
      try {
        const r = await api(`/api/books/${b.id}/identify`, { method: 'POST' })
        f.title.value = r.title || f.title.value
        f.author.value = r.author || ''
        f.translator.value = r.translator || ''
        f.orig_title.value = r.original_title || ''
        f.series.value = r.series || ''
        f.series_index.value = r.series_index || ''
        if (r.year) f.year.value = r.year
        if (r.category) { if (![...cat.options].some(o => o.value === r.category)) cat.append(h('option', { value: r.category }, r.category)); cat.value = r.category }
        if (r.tags?.length) tags.value = r.tags.join('，')
        toast(`识别结果已填入（${r.source}，把握 ${pct(r.confidence)}），核对后点保存`)
      } catch (e) { toastError(e) }
      btn.disabled = false
      btn.lastChild.textContent = old
    }

    async function remove() {
      const ok = await confirmDialog({ title: `删除《${b.title}》？`, message: '会从书库移除，文件移到书库的“_回收站”文件夹，不会真正删除。可以在“整理 → 操作记录”里撤销。', ok: '删除', danger: true })
      if (!ok) return
      try { await api('/api/books/' + b.id, { method: 'DELETE' }); toast('已删除'); d.close(); ctx.refreshFacets(); ctx.rerender() } catch (e) { toastError(e) }
    }

    // 联网查找：豆瓣 / Open Library / Google Books / Bangumi，挑一个填进表单
    let pendingCover = null
    const candBox = h('div')
    async function lookup(btn) {
      btn.disabled = true
      fill(candBox, h('p', { class: 'muted small-text' }, '正在查豆瓣、Open Library、Google Books…'))
      candBox.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
      try {
        const r = await api(`/api/books/${b.id}/enrich`, { method: 'POST', body: { title: f.title.value, author: f.author.value } })
        if (!r.candidates.length) {
          fill(candBox, h('div', { class: 'notice' }, h('div', null, h('b', null, '没有找到'), r.errors?.length ? '：' + r.errors.join('；') : '。可以改一下书名或作者再查')))
        } else {
          fill(candBox, h('div', { class: 'sub-title', style: { marginTop: 0 } }, '联网查到的结果', r.errors?.length ? h('span', { class: 'muted small-text', style: { fontWeight: 400, marginLeft: '8px' } }, r.errors.join('；')) : null),
            h('div', { class: 'cands' }, r.candidates.map(c => h('div', { class: 'cand' },
              c.cover ? h('img', { src: c.cover, alt: '', referrerpolicy: 'no-referrer', loading: 'lazy', onerror: e => e.target.replaceWith(h('div', { class: 'noimg' })) }) : h('div', { class: 'noimg' }),
              h('div', { style: { minWidth: 0 } },
                h('div', { class: 't' }, c.title, h('span', { class: 'score' + (c.score >= 0.84 ? ' hi' : '') }, `匹配 ${Math.round(c.score * 100)}%`)),
                h('div', { class: 'm' }, [c.author, c.translator ? c.translator + ' 译' : '', c.year || '', c.publisher, c.source].filter(Boolean).join(' · ')),
                c.description ? h('div', { class: 'd' }, c.description) : null,
                h('div', { class: 'row' },
                  h('button', { class: 'btn small primary', onclick: () => useCand(c, true) }, '用这条信息'),
                  c.cover ? h('button', { class: 'btn small', onclick: () => useCand(c, false) }, '只换封面') : null,
                  c.link ? h('a', { class: 'btn small quiet', href: c.link, target: '_blank', rel: 'noopener' }, '查看来源') : null))))))
        }
      } catch (e) { fill(candBox); toastError(e) }
      btn.disabled = false
    }
    function useCand(c, all) {
      if (all) {
        const setIf = (input, v) => { if (v) input.value = v }
        setIf(f.title, c.title); setIf(f.author, c.author); setIf(f.translator, c.translator); setIf(f.orig_title, c.original_title)
        setIf(f.year, c.year || ''); if (f.publisher) setIf(f.publisher, c.publisher)
        if (c.description) desc.value = c.description
        if (c.tags?.length && !tags.value) tags.value = c.tags.slice(0, 5).join('，')
      }
      pendingCover = c.cover || null
      toast(all ? '已填入表单，核对后点保存' : '封面会在保存时更换')
      fill(candBox)
    }
    const saveOrig = saveIt
    saveIt = async (confirm) => {
      if (pendingCover) {
        try { await api(`/api/books/${b.id}/cover-url`, { method: 'POST', body: { url: pendingCover } }) } catch (e) { toastError(e) }
        pendingCover = null
      }
      return saveOrig(confirm)
    }

    const idBtn = h('button', { class: 'btn', type: 'button', onclick: e => identify(e.currentTarget), title: ctx.settings?.ai?.enabled ? '用 AI 重新识别书名作者' : '用文件名和内嵌信息重新识别' }, icon('sparkle'), h('span', null, '重新识别'))
    const lookupBtn = h('button', { class: 'btn', type: 'button', onclick: e => lookup(e.currentTarget), title: '在豆瓣、Open Library 等网站查找封面和简介' }, icon('search'), h('span', null, '联网查找'))
    fill(d.body, review, top, b.description ? h('p', { class: 'desc' }, b.description) : null,
      h('div', { class: 'sub-title' }, '文件'), files,
      h('div', { class: 'sub-title' }, '编辑信息'), candBox, form)
    const foot = d.panel.querySelector('footer') || d.panel.appendChild(h('footer'))
    fill(foot, 
      b.status === 'review' ? h('button', { class: 'btn primary', onclick: () => saveIt(true) }, icon('check'), '确认并整理') : h('button', { class: 'btn primary', onclick: () => saveIt(false) }, '保存修改'),
      lookupBtn, idBtn,
      h('span', { class: 'grow' }),
      h('button', { class: 'btn quiet danger', onclick: remove, title: '删除' }, icon('trash')))
  }
}
