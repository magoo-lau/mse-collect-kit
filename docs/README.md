# mse-collect-kit —— 文档索引

通用可配置采集器 v2：Go 单二进制核心（零仓库知识）+ recipe 插件（仓库行为全部在 recipes/ 内声明）。

| 文档 | 内容 |
|---|---|
| [../README.md](../README.md) | 工具定位、三段交付管道（采集→打包→安装）中的角色、快速上手 |
| [recipe.md](recipe.md) | recipe 完整规范：recipe.json 字段（scan/params/middleware/actions）、hooks 钩子协议（--repo/--work/--name/--version/--form/--ui/--modules/--middleware/--out/--repush）、studio-preset |
| [../recipes/ai-mse/](../recipes/ai-mse/) | ai-mse 仓库的唯一 recipe 实装（collect / push-images / repush 三动作 + studio-preset） |

## 拆仓说明

本工具自 ai-mse 单仓（源 commit `cc5410a3`）拆分独立：

- `recipes/ai-mse/recipe.json` 的 `repoRoot` 指向 **ai-mse 单仓**（`../../../ai-mse`，相对 recipe 目录解析）——单仓仍是唯一同时含 framework/modules/sample 全貌的采集对象；`scan.modulesDir=mse-modules`、`scan.versionFile=mse-framework/pom.xml` 等均相对 repoRoot。
- 与 deliver-studio（mse-deliver 仓）的集成是纯 HTTP（`--studio` 参数），Go 模块零依赖；`recipes/ai-mse/studio-preset.json` 与 deliver-studio 仓 `presets/ai-mse.json` 字节一致（双份维护）。
- 新仓库接入采集：照 recipes/ai-mse 写一个 recipe（目录约定/构建链/镜像目录/预设），产物合同 build-manifest.json 隔离仓库知识与核心知识。

## 构建 / 测试

```sh
sh build.sh          # ui 构建 → 交叉编译 linux/windows 双平台 → bin/
go test ./...        # 含内置 recipe 可加载校验（preset/hook 就位）与 fakeStudio 端到端
```
