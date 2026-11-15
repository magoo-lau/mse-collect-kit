# collect-kit —— 通用可配置采集器（v2，插件化）

三段交付管道第一段「构建」（`docs/tech/standalone.md` §四、`docs/tech/deliver-design.md` ADR-9）：
**采集构建 → deliver-studio 打包/重打包 → deliver-console 安装**。v2 为全面插件化重写——
核心（Go 单二进制）零仓库知识，一切仓库行为来自 **recipe（采集方案插件）**；
仓库知识与 studio 知识被「产物合同」隔离在钩子与核心两侧。

```
┌────────────────────────── collect-kit（核心，通用） ──────────────────────────┐
│ 向导（浏览器动态表单） / CLI 子命令 collect·repush·push-images·upload-preset  │
│ deliver-studio REST（登录/预设/镜像入仓/制品同版本覆盖上传/打包/轮询/下载）          │
│ 通用仓库扫描（recipe.scan 规则驱动）· zip-dir 工具                            │
└──────────────▲──────────────────────────────────────▲───────────────────────┘
               │ recipe.json（参数/扫描/镜像目录声明）  │ build-manifest.json（产物合同）
   ┌───────────┴──────────┐              ┌─────────────┴─────────────┐
   │ recipes/ai-mse/      │  hooks（sh + ps1 双平台）                    │
   │  ├ recipe.json       │  构建聚合 fat-jar / 逐服务镜像 / UI zip 等   │
   │  ├ studio-preset.json│  （仓库特有的一切，全部在 recipe 侧）          │
   │  └ hooks/collect.*   │                                              │
   └──────────────────────┘              └───────────────────────────┘
```

## 用法

```bash
# 向导（浏览器表单，按 recipe 声明动态渲染；Windows 双击 collect-kit.cmd 同效）
collect-kit                      # 唯一内置 recipe 时免选；多个时页面顶部直接切换（-recipe 可预选并锁定）

# CLI（参数名 = recipe params 的 kebab-case：baseVersion → --base-version）
collect-kit collect   --recipe ai-mse --name tenant-a --modules oa,fs \
     --studio http://127.0.0.1:7860 --studio-user admin --studio-pass *** --package
collect-kit repush    --recipe ai-mse --name tenant-a --version 1.0.0 --ui true \
     --studio http://127.0.0.1:7860 --studio-pass *** --package   # 推送段失败免重建（mono）
collect-kit push-images --recipe ai-mse --form micro --middleware rocketmq,elasticsearch \
     --studio http://127.0.0.1:7860 --studio-pass ***            # 第三方镜像备料（纯声明式）
collect-kit upload-preset --recipe ai-mse --studio http://127.0.0.1:7860 --studio-pass ***
collect-kit zip-dir <src-dir> <dest.zip>                        # 通用工具（钩子脚本可调）
collect-kit list-recipes
```

推送类动作启动前先登录预检（密码错当场拦截）；`--package` 缺省工作台地址 `127.0.0.1:7860`。

## 构建（Windows / Linux 双平台）

```bash
sh build.sh        # Linux / Git Bash：前端构建 + 双平台交叉编译 → bin/
```

Windows 双击 `build.cmd`（或 `powershell -ExecutionPolicy Bypass -File build.ps1`）。
前置：`node/npm`（前端）+ `go`。产物 `bin/collect-kit-linux`、`bin/collect-kit-windows.exe`（CGO 关闭纯静态 amd64，任一平台宿主均可编译出双端）；分发形态 = 二进制 + `recipes/` 目录。

向导前端在 `ui/`（Vite + Vue3 + Element Plus，仓库紧凑风格 + 深浅双模式）：`build.sh` / `build.ps1` 先 `npm run build` 产出 `ui/dist` 再 `go:embed` 内嵌。`ui/dist` 仅随库提交 `.gitkeep` 占位——裸 `go build` 恒可编译，未构建时向导页显示构建提示；双击 `collect-kit.cmd` 首次检测到前端缺失会自动 `npm run build`（无 npm 则降级提示页）。前端本地开发：`cd ui && npm run dev`（:5181，`/api` 代理到向导 7861）。

## recipe 合同（`recipes/<name>/recipe.json`，format:1）

> 完整手册（逐段字段/六种参数 kind/钩子合同/产物合同/preset/参考实现导读/错误速查）见 **[docs/recipe.md](docs/recipe.md)**——第三方为新仓库编写 recipe 的接入手册。本节仅速览：

| 段 | 作用 |
| --- | --- |
| `preset` | 随附 deliver-studio 预设 JSON（推送类动作先幂等导入） |
| `repoRoot` | 仓库根（相对 recipe 目录；钩子与扫描的基准） |
| `scan` | 通用扫描规则：`modulesDir`/`modulePrefix`/`requireSuffix`（模块入选条件）/`probeDirs`（子目录探测标注）/`versionFile`+`versionDefault`（基座版本提取） |
| `middleware` | 中间件目录（勾选项清单，向导展示与 `--middleware` 取值域） |
| `params` | 参数声明（`kind`: text/choice/bool/modules/middleware/out）——CLI flag 与向导表单同源；text 可带 `pattern`；`defaultFromScan: baseVersion` 缺省取扫描提取值 |
| `actions.collect` | 构建钩子 `hook: {sh, ps1}`（windows 优先 ps1，无则 sh）+ `repushProfiles`（支持重推的形态白名单，空=全部） |
| `actions.pushImages` | 镜像备料目录（全声明式无钩子）：`always` + `whenParam`（参数条件镜像）+ `middleware`（中间件 → 镜像，只推 `--middleware` 勾选组、未勾不推；条目可 `{ref}` 直推或 `{build, context}` 本地缺失先构建） |

## 产物合同（钩子写 `<work>/build-manifest.json`，核心消费）

```jsonc
{
  "appImages": ["mse/mse-monolith:1.0.0"],          // 应用镜像（核心入仓）
  "materials": [                                     // 上传制品（路径相对 work；默认总是上传，服务端同身份保留最新；--dedup-materials 复用跳过）
    {"path": "build/target/mse-tenant-standalone.jar", "kind": "artifact", "appCode": "mse-monolith"},
    {"path": "mse-ui-1.0.0.zip", "kind": "artifact", "appCode": "mse-monolith", "role": "ui"}
  ],
  "package": {                                       // --package 打包请求（核心提交 deliver-studio）
    "appCode": "mse-monolith",
    "units": [{"code": "app", "source": "image_ref", "imageRef": "mse/mse-monolith:1.0.0"},
              {"code": "web", "source": "artifact", "materialRole": "ui"}],  // materialRole 由核心回填 materialId
    "componentCodes": ["mysql", "redis"],
    "configCenterInclude": ["mse-micro-common.yaml"]  // 可选（DataID 白名单裁剪）
  },
  "summary": ["fat-jar 就绪"]                         // 结果汇总行（核心末尾统一输出）
}
```

钩子约定：核心以 `--repo/--work/<params…>/--modules/--middleware/[--repush]` 调起，
`COLLECT_KIT_BIN` 环境变量指向 collect-kit 可执行文件（`zip-dir` 等通用工具可直接调用）；
`--repush` 时钩子跳过构建、校验既有制品后重写 manifest（免重建重推）。

## 新仓库接入（写一个 recipe 即可）

1. 建 `recipes/<name>/recipe.json`（声明参数/扫描规则/镜像目录）；
2. 写 `hooks/collect.sh`（+ 可选 `collect.ps1`）：构建你的制品并输出 build-manifest.json；
3. （可选）放 `studio-preset.json`（应用/组件定义，权威维护在 recipe 侧）；
4. `collect-kit -recipe <name>` 起向导或直接 CLI。核心与向导零改动。

内置 recipe 定位：cwd 逐级向上找 `recipes/` → exe 所在目录逐级向上找（向导构建子进程 cwd=仓库根、任意目录直调 `bin/` 下二进制由此命中）→ exe 同级 `recipes/`（分发形态 = 二进制 + recipes/ 目录）。

## v1 → v2 迁移

- `build-tenant-kit.sh/.ps1/.cmd` 已删除（git 历史留档）：`--name/--version/--modules/--ui/--middleware/--form/--out/--base-version/--upload/--package/--studio-user/--studio-pass` 语义不变；
- `--upload-preset <url>` → `upload-preset --studio <url>`；`--push-images <url>` → `push-images --studio <url>`；`--repush <url>` → `repush --studio <url>`；
- studio-preset.json 权威源移至 `recipes/ai-mse/studio-preset.json`（与 `deliver-studio/presets/ai-mse.json` 仍双份字节一致维护）；
- 向导并入主程序（端口仍 7861），表单按 recipe 动态渲染。

## 测试

`go test ./...`：recipe 合同校验、通用扫描、参数名往返、端到端（假钩子 → 产物合同 → 假 deliver-studio：登录/同版本覆盖上传与去重选项/打包请求 materialRole 回填/套件下载/重推形态拦截）。
