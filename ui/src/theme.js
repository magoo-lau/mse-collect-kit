// 深浅色：localStorage 记忆 + 系统偏好默认（独立极简版，不依赖 mse-ui-shared——本工程随 Go 二进制内嵌分发）。
const KEY = 'ck-theme'

export function initTheme() {
  const saved = localStorage.getItem(KEY)
  apply(saved ? saved === 'dark' : matchMedia('(prefers-color-scheme: dark)').matches)
}

export function currentDark() {
  return document.documentElement.classList.contains('dark')
}

export function toggleTheme() {
  apply(!currentDark())
}

function apply(dark) {
  document.documentElement.classList.toggle('dark', dark)
  localStorage.setItem(KEY, dark ? 'dark' : 'light')
}
