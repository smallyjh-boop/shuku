// /legacy/pdf.js：pdf.js 降级后打成普通脚本，挂在 window.pdfjsLib 上（src/pdfjs.js 加载它）
import * as lib from '/vendor/pdfjs/pdf.mjs'
window.pdfjsLib = lib
