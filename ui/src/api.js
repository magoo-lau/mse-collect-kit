// 向导后端 REST 封装（同源 /api/*；错误统一抛 Error(message)——后端合同 {ok:false,error}）
async function call(method, url, body) {
  const resp = await fetch(url, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  })
  let data = {}
  try { data = await resp.json() } catch { /* 非 JSON 响应：留给下方空对象，调用方按 undefined 字段处理 */ }
  if (data && data.error) throw new Error(data.error)
  return data
}

export const getMeta = () => call('GET', '/api/meta')
export const selectRecipe = (recipe) => call('POST', '/api/select', { recipe })
export const preview = (form) => call('POST', '/api/preview', form)
export const start = (form) => call('POST', '/api/start', form)
export const getStatus = () => call('GET', '/api/status')
