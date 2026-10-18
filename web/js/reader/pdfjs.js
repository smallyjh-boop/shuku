// 加载 pdf.js（第一次打开 PDF 时才加载，800 KB 不拖慢书库的启动）。
// 兼容旧系统的那一套网页（tools/legacy）构建时，这个文件换成 tools/legacy/src/pdfjs.js：用普通脚本加载降级过的 pdf.js
export async function loadPdfjs() {
  const lib = await import('/vendor/pdfjs/pdf.mjs')
  lib.GlobalWorkerOptions.workerSrc = '/vendor/pdfjs/pdf.worker.mjs'
  return lib
}
