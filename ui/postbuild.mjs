// vite build 的 emptyOutDir 会清空 dist（包括随库提交的 .gitkeep 占位——go:embed 恒可编译的锚点），
// 构建成功后重建它，保证 git 状态干净、且后续裸 go build 不因空目录失编译。
import { writeFileSync } from 'node:fs'

writeFileSync('dist/.gitkeep', '')
console.log('dist/.gitkeep placeholder restored')
