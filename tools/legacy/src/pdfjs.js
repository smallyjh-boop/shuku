// 兼容旧系统的那一套网页里，web/js/reader/pdfjs.js 换成这个：用普通脚本加载降级过的 pdf.js（/legacy/pdf.js），
// worker 也是普通脚本（旧浏览器的 worker 不支持 ES 模块）
let loading = null

export function loadPdfjs() {
  if (window.pdfjsLib) return Promise.resolve(window.pdfjsLib)
  if (!loading) {
    loading = new Promise((resolve, reject) => {
      const s = document.createElement('script')
      s.src = '/legacy/pdf.js'
      s.onload = () => {
        const lib = window.pdfjsLib
        if (!lib) { loading = null; reject(new Error('PDF 阅读器没有加载上')); return }
        lib.GlobalWorkerOptions.workerSrc = '/legacy/pdf.worker.js'
        resolve(lib)
      }
      s.onerror = () => { loading = null; reject(new Error('PDF 阅读器没有加载上（/legacy/pdf.js）')) }
      document.head.appendChild(s)
    })
  }
  return loading
}
