import { createApp } from 'vue'
import ElementPlus from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'
import * as Icons from '@element-plus/icons-vue'
import App from './App.vue'
import { initTheme } from './theme'

const app = createApp(App)
// 紧凑风格：全局 small（与仓库前端基线一致的密度优先口径）
app.use(ElementPlus, { size: 'small', locale: zhCn })
for (const [name, comp] of Object.entries(Icons)) app.component(name, comp)
initTheme()
app.mount('#app')
