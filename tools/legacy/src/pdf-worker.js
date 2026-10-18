// /legacy/pdf.worker.js：pdf.js 的 worker，普通脚本（旧浏览器的 worker 不支持 ES 模块）。
// 先补上旧浏览器缺的语言功能，pdf.worker.mjs 加载后自己接上主线程
import 'core-js-needed' // core-js 里 Chrome 55 缺的（build.mjs 按 core-js-compat 算）
import 'abortcontroller-polyfill/dist/abortcontroller-polyfill-only'
import './shims.js'
import '/vendor/pdfjs/pdf.worker.mjs'
