// collect-kit 内嵌前端资产：ui/dist（Vite + Vue3 + Element Plus 工程的构建产物）。
// dist 仅随库提交 .gitkeep 占位（npm 构建产物不入库）——裸 go build 恒可编译；
// 未构建时向导返回内置提示页兜底，正式构建经 build.sh / build.ps1 / collect-kit.cmd（npm build + go build）覆写。
package main

import (
	"embed"
	"io/fs"
)

//go:embed all:ui/dist
var uiFS embed.FS

// wizardUIDist 取内嵌 dist 子树；index.html 缺失（仅 .gitkeep 占位）时 ok=false。
func wizardUIDist() (fs.FS, bool) {
	dist, err := fs.Sub(uiFS, "ui/dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		return nil, false
	}
	return dist, true
}

// uiPlaceholder 前端未构建时的兜底提示页（无框架依赖，color-scheme 跟随系统深浅色）。
const uiPlaceholder = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>collect-kit 采集向导</title>
<style>
  body { font: 14px/1.8 system-ui, "Microsoft YaHei", sans-serif; margin: 0; display: flex; min-height: 100vh; align-items: center; justify-content: center; }
  .box { max-width: 560px; padding: 24px 28px; margin: 16px; border: 1px solid; border-radius: 8px; }
  h1 { font-size: 16px; margin: 0 0 8px; }
  pre { font-family: Consolas, Monaco, monospace; font-size: 13px; overflow: auto; padding: 8px 10px; border-radius: 6px; }
</style>
</head>
<body>
<div class="box">
  <h1>collect-kit 向导前端尚未构建</h1>
  <p>当前二进制内嵌的 ui/dist 只有占位文件。请在 collect-kit 目录执行一次前端构建，然后重新编译：</p>
  <pre>cd ui
npm install
npm run build
go build</pre>
  <p>或直接运行 build.sh / build.ps1（前端与双平台编译一步完成），双击 collect-kit.cmd（首次自动构建前端）。</p>
</div>
</body>
</html>
`
