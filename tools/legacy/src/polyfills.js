// /legacy/polyfills.js：先于网页加载，补上旧浏览器缺的功能。
// core-js 补语言自带的（Object.groupBy、Array.prototype.at、structuredClone、Promise.withResolvers……，浏览器自己有的不动）；
// dom.js 补浏览器的（ResizeObserver、AbortController、流、解压、Blob.text……）
import 'core-js-needed' // core-js 里 Chrome 55 缺的（build.mjs 按 core-js-compat 算）
import 'abortcontroller-polyfill/dist/polyfill-patch-fetch'
import './shims.js'
import './dom.js'
