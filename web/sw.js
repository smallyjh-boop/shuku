// 缓存网页本身（界面、阅读内核），断网或换网络时也能秒开；书和接口数据不缓存
const VERSION = 'shuku-v1'
self.addEventListener('install', e => { self.skipWaiting() })
self.addEventListener('activate', e => {
  e.waitUntil(caches.keys().then(ks => Promise.all(ks.filter(k => k !== VERSION).map(k => caches.delete(k)))).then(() => self.clients.claim()))
})
self.addEventListener('fetch', e => {
  const u = new URL(e.request.url)
  if (e.request.method !== 'GET' || u.origin !== location.origin || u.pathname.startsWith('/api/')) return
  // 先用网络（拿到最新版本），失败时用缓存
  e.respondWith(fetch(e.request).then(r => {
    if (r.ok) { const c = r.clone(); caches.open(VERSION).then(cache => cache.put(e.request, c)) }
    return r
  }).catch(() => caches.match(e.request).then(r => r || caches.match('/index.html'))))
})
