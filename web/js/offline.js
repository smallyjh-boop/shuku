// 离线读书（浏览器 / iPhone 主屏幕上的书库）：把书存进浏览器的 Cache Storage，断网时也能打开，
// 进度在离线时排队、联网后再同步。安卓 App 有自己的“下载到手机”，不用这里。
// 浏览器只在 https（比如 Tailscale 地址）或 localhost 下才有 Cache Storage。
//
// 接口（书架、书的详情页用）：
//   offlineSupported            —— 这个浏览器能不能离线存书
//   isOfflineBook(id)           —— 这本书已经存好了
//   offlineButton(book, ctx)    —— 书的详情里的“离线可读”按钮（不支持时返回 null）
//   renderOffline(main, ctx)    —— #/offline 离线书架

import { isNative } from './native.js'

export const offlineSupported = !isNative && typeof caches !== 'undefined' && window.isSecureContext

export function isOfflineBook(id) { return false }

export function offlineButton(book, ctx) { return null }

export async function renderOffline(main, ctx) {}
