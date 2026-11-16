# AGENTS.md —— mse-collect-kit 采集器协作约定

本文件面向所有在本仓库工作的 AI 助手与开发者。本仓库自 ai-mse 单仓（源 commit `cc5410a3`）拆分独立。

## 一、定位与结构

通用可配置采集器 v2：**Go 单二进制核心（零仓库知识）+ recipe 插件**。

- 核心：main.go（CLI）/ scan.go（仓库扫描）/ recipe.go（recipe 加载与 repoRoot 解析）/ runner.go（动作执行与钩子传参）/ studio.go（deliver-studio REST 客户端）/ wizard.go（127.0.0.1:7861 向导）/ embed.go / ui/（Vue 向导前端）
- recipes/：仓库行为全部声明于此——`recipes/ai-mse/`（collect 构建采集 / push-images 第三方镜像备料 / repush 重推既有制品免重建；钩子 hooks/collect.sh|ps1、声明 recipe.json、预设 studio-preset.json）

## 二、关键约定（红线）

1. **核心零仓库知识**：仓库目录约定、构建链、镜像目录、预设全部在 recipe 内声明；新仓库接入=新写一个 recipe，禁止在核心里 if 仓库名。
2. **repoRoot 相对 recipe 目录解析**（`Recipe.RepoRootAbs()`）：本仓 recipe 指向 `../../../ai-mse`（拆仓适配，ai-mse 单仓为采集对象）；在 ai-mse 仓内的原版 collect-kit 用 `"../../../.."`。
3. **产物合同 build-manifest.json**：核心与 studio 之间的唯一数据契约，改字段需与 mse-deliver 仓 deliver-studio 同步。
4. **studio-preset 双份字节一致**：`recipes/ai-mse/studio-preset.json` ≡ deliver-studio 仓 `presets/ai-mse.json`，改一侧必须同步另一侧。
5. **推送类动作**（push/repush）启动前登录预检 + materials 同名同大小去重复用；工作台服务端按同身份保留最新一条。

## 三、构建、测试与验证

- `sh build.sh`（ui → 交叉编译 linux/windows → bin/，CGO 关闭纯静态）。
- `go test ./...`：内置 ai-mse recipe 必须可加载（preset/hook 就位）+ fakeStudio 端到端用例——**改 recipe.json 或核心加载逻辑后必须跑**。
- 向导与 wizard 端口 7861（deliver-studio 占 7860，错开一位）。

## 四、提交纪律

核心与 recipe 改动分开提交；涉及 studio 接口/preset 的跨仓同步项在提交信息中注明对端仓。
