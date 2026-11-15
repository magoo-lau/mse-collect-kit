# collect-kit recipe 编写指南（第三方接入手册）

本手册面向**要为自己的仓库编写 / 维护 recipe 的第三方开发者**：一个 recipe（采集方案插件）让通用采集器
collect-kit 认识你的仓库——目录约定、构建链、镜像目录、打包单元构造全部由 recipe 声明，核心与向导零改动。
内容以源码为准（recipe.go / runner.go / scan.go / wizard.go / main.go / studio.go / collect_test.go），
参考实现为内置的 `recipes/ai-mse/`。

> 阅读顺序建议：§0 建立心智模型 → §1 抄最小示例跑通 → 需要什么查 §3–§6 手册 → §8 验证 → §9 对照成熟实现。

---

## §0 心智模型：知识隔离在两侧

collect-kit 是三段交付管道的第一段「构建」（`docs/tech/standalone.md` §四、`docs/tech/deliver-design.md` ADR-9）：

```
collect-kit（构建采集） → deliver-studio（打包/重打包） → deliver-console（安装）
```

核心（Go 单二进制）**零仓库知识**，只懂三件事：

| 核心懂什么 | 载体 |
| --- | --- |
| recipe 合同 | `recipe.json`（参数声明 / 扫描规则 / 镜像目录声明 / 动作） |
| 产物合同 | `build-manifest.json`（钩子产出，核心消费后接管全部 studio 交互） |
| deliver-studio REST | 登录 / 预设导入 / 镜像入仓 / 制品上传去重 / 打包 / 轮询 / 下载 |

**你的仓库的一切知识都写在 recipe 侧**：

| 仓库知识 | 载体 |
| --- | --- |
| 目录约定（模块在哪、怎么识别） | `recipe.json` 的 `scan` 段 |
| 构建链（怎么把源码变成 jar / 镜像 / UI zip） | `hooks/collect.sh`（+ 可选 `collect.ps1`） |
| 第三方镜像备料清单 | `recipe.json` 的 `actions.pushImages` 段 |
| 打包单元构造（镜像 / 制品怎么组成套件） | 钩子写的 `build-manifest.json` 的 `package` 段 |
| 应用 / 组件定义（deliver-studio 预设） | `studio-preset.json`（可选） |

两侧知识被「产物合同」隔离：钩子**只管构建与产物声明**，登录、推送、打包、轮询、下载全在核心；
核心**不预设任何目录约定**——`mse-modules/mse-<m>/<m>-service` 这类路径全是 ai-mse recipe 的数据。

---

## §1 五分钟最小 recipe

一个只声明 collect 动作的极简 recipe，两个文件即可跑通：

**`my-repo/recipe.json`**

```json
{
  "format": 1,
  "name": "my-repo",
  "title": "我的仓库采集",
  "repoRoot": "../..",
  "params": [
    { "name": "name", "kind": "text", "label": "套件名（必填）", "pattern": "^[a-z0-9][a-z0-9-]*$" },
    { "name": "version", "kind": "text", "label": "版本", "default": "1.0.0" }
  ],
  "actions": {
    "collect": { "hook": { "sh": "hooks/collect.sh" } }
  }
}
```

**`my-repo/hooks/collect.sh`**（最小骨架：解析参数 → 构建 → 写 manifest）

```sh
#!/bin/sh
# 钩子只管构建与产物声明；推送/打包由 collect-kit 核心接管
set -e
REPO=; WORK=; NAME=; VERSION=1.0.0
while [ $# -gt 0 ]; do
  case "$1" in
    --repo) REPO="$2"; shift 2 ;;
    --work) WORK="$2"; shift 2 ;;
    --name) NAME="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    *) echo "未知参数: $1" >&2; exit 1 ;;
  esac
done
[ -n "$REPO" ] && [ -n "$WORK" ] && [ -n "$NAME" ] || { echo "缺少 --repo/--work/--name" >&2; exit 1; }

# ↓↓↓ 你的构建链：编译产物到 $WORK 下、构建 docker 镜像 ↓↓↓
mkdir -p "$WORK/build"
echo "hello-$NAME" > "$WORK/build/app.txt"

# 产物声明（路径一律相对 $WORK）
cat > "$WORK/build-manifest.json" <<EOF
{
  "appImages": [],
  "materials": [
    { "path": "build/app.txt", "kind": "artifact", "appCode": "my-app" }
  ],
  "summary": ["构建完成"]
}
EOF
```

**跑通三步**（recipe 放任意目录，`--recipe` 直接传路径，无需放进 collect-kit 发行目录）：

```bash
collect-kit --recipe ./my-repo            # 向导（浏览器表单，127.0.0.1:7861）
collect-kit collect --recipe ./my-repo --name tenant-a --version 1.0.0
collect-kit list-recipes                  # 只有放进内置 recipes/ 目录才在此列出
```

要点：

- `--repo` / `--work` / `COLLECT_KIT_BIN` 环境变量由核心注入，钩子缺了 `--repo/--work/--name` 应自行报错退出；
- 不带 `--studio` 跑 collect = 纯本地构建（推送段整体跳过），最适合先调通钩子；
- 钩子非零退出码 = 本次动作失败（核心打印 `构建失败 ✘` 并退出 1）。

---

## §2 recipe 目录解剖与定位规则

```
recipes/<name>/
├── recipe.json          # 必备：参数/扫描/动作声明（合同见 §3）
├── hooks/
│   ├── collect.sh       # collect 钩子（Linux/macOS；Windows 无 ps1 时也走 sh）
│   └── collect.ps1      # 可选：Windows 版（Windows 下优先；与 sh 行为对等，见 §4）
└── studio-preset.json   # 可选：deliver-studio 应用/组件预设（需要 --package 一键打包时提供，见 §6）
```

**定位规则**（两层，`--recipe` 取值决定走哪层）：

| `--recipe` 取值 | 解析 |
| --- | --- |
| 内置名（如 `ai-mse`，须匹配 `^[a-z][a-z0-9-]*$`） | `<内置根>/<name>/recipe.json` |
| 目录路径（相对或绝对） | `<该目录>/recipe.json` —— **第三方 recipe 推荐用法，位置随意** |

内置根定位：**cwd 逐级向上找 `recipes/` 目录**（仓库内任意子目录运行都能命中），到底仍无 →
**exe 所在目录逐级向上找**（向导构建子进程 cwd=仓库根、任意目录直调 `bin/` 下二进制等场景由此命中），
仍无 → 兜底 **exe 同级 `recipes/`**（官方分发形态 = 二进制 + recipes/ 目录）。

- 向导缺省 `-recipe`：内置恰好 1 个时免选；**多个时页面顶部下拉直接切换**（默认选第一个，`-recipe` 可预选并锁定单一）；CLI 子命令仍须显式 `--recipe`（报错列出全部内置名）。
- CLI 缺省 `--recipe`：同上，报错 `缺少 --recipe（内置: …；或 --recipe <recipe 目录路径>）`。

---

## §3 recipe.json 逐段字段手册（format:1）

### 3.1 顶层字段

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `format` | int | ✔ | 固定 `1`（合同版本号，不匹配直接拒绝加载） |
| `name` | string | ✔ | 唯一编码，**kebab 形态**（`^[a-z][a-z0-9-]*$`） |
| `title` | string | 建议 | 展示名（向导标题） |
| `description` | string | 可选 | 一句话说明（向导展示；建议写清形态/产物口径） |
| `preset` | string | 可选 | 随附 deliver-studio 预设 JSON 文件名（**相对 recipe 目录**）。推送类动作（collect/repush/push-images）启动时先幂等导入；失败仅告警不阻断 |
| `repoRoot` | string | 建议 | 仓库根，**相对 recipe 目录**。钩子 cwd、扫描基准、`out` 相对路径基准；缺省 = recipe 目录本身 |
| `scan` | object | 可选 | 通用仓库扫描规则（见 3.2） |
| `middleware` | array | 可选 | 中间件目录 `[{code,label}]`：向导中间件勾选卡 + `--middleware` 取值域 + pushImages.middleware 的键 |
| `params` | array | ✔ | 参数声明（见 3.3；空数组也合法，但一般至少要有 `name`） |
| `actions` | object | ✔ | 至少声明 `collect` / `pushImages` 其一（见 3.4 / 3.5） |

### 3.2 `scan` —— 通用仓库扫描规则

扫描在动作启动时执行一次（`ScanRepo`），产出：模块清单（供向导勾选卡 + `--modules` 合法性校验）与基座版本
（供 `defaultFromScan: baseVersion` 参数预填）。

| 字段 | 说明 | ai-mse 示例 |
| --- | --- | --- |
| `modulesDir` | 模块根目录（**相对仓库根**）。空 = 跳过模块扫描只提版本 | `mse-modules` |
| `modulePrefix` | 模块目录前缀；**短名 = 目录名去前缀**（短名即 `--modules` 取值） | `mse-`（`mse-oa` → `oa`） |
| `requireSuffix` | 门禁：模块目录下须存在子目录 `<短名><后缀>`，**缺失则整模块不入清单**；命中时自动标注 probe（去 `-` 前缀形式） | `-service`（须有 `oa-service`；标注 `service`） |
| `probeDirs` | 探测标注 `[{suffix,label}]`：存在 `<短名><suffix>` 子目录即标注 label（向导模块卡展示 `service · ui` 这类组合标注） | `[{suffix:"-ui",label:"ui"},{suffix:"-mobile-ui",label:"mobile-ui"}]` |
| `versionFile` | 基座版本提取文件（**相对仓库根**）：正则 `<version>…</version>` 取**首个**匹配（去空白） | `mse-framework/pom.xml` |
| `versionDefault` | 兜底：**仅当提取失败**（文件缺失 / 无标签，版本仍为初始值 `1.0.0-SNAPSHOT`）时替换 | `1.0.0-SNAPSHOT` |

模块清单按短名字典序输出；`modulesDir` 读取失败（仓库根不对）→ 模块参数校验降级跳过并提示，动作不中断。

### 3.3 `params` —— 参数声明（CLI flag 与向导表单同源）

每条参数：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `name` | ✔ | **小写驼峰**（camelCase）；CLI flag 自动转 `--kebab-case`（`baseVersion` → `--base-version`），向导表单字段同名 |
| `kind` | ✔ | `text` / `choice` / `bool` / `modules` / `middleware` / `out` 六种（见下表） |
| `label` | 建议 | 向导展示文案 |
| `default` | 可选 | 缺省值（字符串；bool 用 `"true"/"false"`） |
| `pattern` | 可选 | **仅 kind=text**：取值合法性正则（CLI/向导双重校验） |
| `choices` | 可选 | **kind=choice 必填**：`[{value,label}]` |
| `defaultFromScan` | 可选 | 非空时缺省取扫描结果键；当前仅支持 `baseVersion`（= `scan.versionFile` 提取值） |

**六种 kind 的三面行为**（向导渲染 / CLI / 传钩子）：

| kind | 向导渲染 | CLI | 传钩子 |
| --- | --- | --- | --- |
| `text` | 文本框；`defaultFromScan=baseVersion` 时预填扫描版本 | `--kebab value` | `--kebab value`（**等于缺省也传**） |
| `choice` | 单选组（choices） | `--kebab value`（取值域校验） | `--kebab value` |
| `bool` | 复选框（default `"true"` 勾选） | `--kebab true/false` | 仅当≠缺省时传 |
| `modules` | 不渲染独立字段——由扫描结果渲染「业务模块」勾选卡 | `--modules a,b,c`（CSV；逐项对扫描清单校验） | 勾选非空时 `--modules a,b,c` |
| `middleware` | 不渲染独立字段——由 `middleware` 目录渲染勾选卡 | `--middleware a,b`（CSV；对目录校验） | 勾选非空时 `--middleware a,b` |
| `out` | 文本框（第二行附加参数区） | `--out <dir>` | 同 text |

> **⚠ 必须显式声明 modules / middleware 参数**：要让模块 / 中间件勾选**流向钩子**，params 里必须包含
> `{"name":"modules","kind":"modules"}` 与 `{"name":"middleware","kind":"middleware"}`——向导按**参数名**把关
> （未声明时勾选被静默丢弃）、核心按 **kind** 决定是否给钩子追加 `--modules/--middleware`。两条都不是
> 可隐式获得的：模块勾选卡由扫描结果渲染、中间件卡由目录渲染，渲染与否**和参数声明无关**，漏声明只会
> 静默丢数据，不会报错。（内置 ai-mse 曾漏声明这两个参数，回归测试已守门。）

命名映射规则：`kebabFlag`（camel→kebab）与 `camelName`（kebab→camel）互逆；已是单词原样（`name/ui`）。
CLI 收到未声明参数会拒绝：`未知参数 --xxx（recipe <名> 未声明；已声明: …）`——防拼错。

惯例参数名（非强制，但钩子生态通用）：`name`（套件名，collect/repush 时必填非空）、`version`、`form`
（部署形态，`repushProfiles` 按它匹配）、`out`（输出目录，参与 workdir 口径，见 §5）。

### 3.4 `actions.collect` —— 构建动作

```jsonc
"collect": {
  "hook": { "sh": "hooks/collect.sh", "ps1": "hooks/collect.ps1" },
  "repushProfiles": ["mono"]
}
```

| 字段 | 说明 |
| --- | --- |
| `hook.sh` / `hook.ps1` | 钩子脚本（**相对 recipe 目录**，可用绝对路径）；**至少其一**。平台选择：Windows 有 ps1 → ps1；否则有 sh → sh；再否则 ps1（非 Windows 也允许，极少用）；全空 → 报错。两脚本**行为必须对等**（见 §4） |
| `repushProfiles` | 支持重推（repush）的形态白名单：空 = 全部；列举 = 仅当 `form` 参数取值在名单内才放行（未命中报错 `形态 x 暂不支持重推（recipe 声明仅 […]；多镜像无统一制品指纹——请直接重跑构建）`）。多镜像产物没有统一制品指纹的形态不要放进名单 |

### 3.5 `actions.pushImages` —— 镜像备料动作（纯声明式，无钩子）

```jsonc
"pushImages": {
  "always": ["mysql:8.4", "redis:7-alpine"],
  "whenParam": [{ "param": "form", "equals": "micro", "images": ["nacos/nacos-server:v3.0.0"] }],
  "middleware": {
    "rocketmq": [{ "ref": "apache/rocketmq:5.3.1" }],
    "elasticsearch": [{ "build": "mse/elasticsearch:9.2.8-smartcn", "context": "mse-framework/docker/elasticsearch" }]
  }
}
```

合并顺序（全程按镜像名去重）：

1. `always`：无条件入仓清单；
2. `whenParam`：参数条件命中（`param` 终值 `== equals`）追加其 `images`；
3. `middleware`：**只推 `--middleware` 勾选的组；未勾选/未传 → middleware 段一个不推**（勾什么推什么，要全备就显式列全目录）。勾选的 code 必须在中间件目录（顶层 `middleware`）∪ `pushImages.middleware` 键内，未知 code 报错 `未知中间件 "x"（可选: …）` 防打错字静默漏推；勾选了目录项但无镜像分组的 code 合法，提示「无备料镜像分组，跳过」；
4. CLI `--extra-images`：手工追加。

另：`--modules` / `--package` 对本动作无效（镜像备料与业务模块无关、无构建过程故无制品可打包，制品入库走 collect），传入将打印「已忽略」警告。

镜像条目两种形态：

| 形态 | 字段 | 行为 |
| --- | --- | --- |
| 直接引用 | `{"ref": "…"}` | 直接入仓（远端仓库不存在时入仓失败仅告警） |
| 本地构建 | `{"build": "目标镜像名", "context": "Dockerfile 目录（相对仓库根）"}` | 先 `docker image inspect` 检查本地；缺失则 `docker build -t <build> <repoRoot>/<context>` 再入仓 |

`middleware` 条目必须含 `ref` 或 `build` 其一，`build` 必须带 `context`（校验期拦截）。

### 3.6 校验规则速查（加载期全量校验，失败即拒载）

| 条件 | 错误信息 |
| --- | --- |
| `format != 1` | `recipe.json format 须为 1（当前 n）` |
| name 非 kebab | `recipe name 须为 kebab 形态: x` |
| 参数缺 name | `参数缺少 name` |
| 参数重名 | `参数 x 重复声明` |
| kind 非法 | `参数 x 的 kind 非法: y（text/choice/bool/modules/middleware/out）` |
| choice 无 choices | `参数 x（choice）缺少 choices` |
| pattern 非正则 | `参数 x 的 pattern 非法: …` |
| 无任何动作 | `recipe 至少声明一个动作（collect / pushImages）` |
| collect 无钩子 | `collect 动作缺少 hook（sh/ps1 至少其一）` |
| 镜像条目无 ref/build | `pushImages.middleware[码][序] 缺少 ref/build` |
| build 无 context | `pushImages.middleware[码][序] build 项缺少 context` |

---

## §4 钩子合同（hooks/collect.sh + collect.ps1）

### 4.1 职责分工

| 钩子负责（仓库知识侧） | 核心负责（studio 知识侧） |
| --- | --- |
| 按参数构建：编译、打镜像、收集前端 | 登录 deliver-studio（密码预检、token 携带） |
| 产出制品文件到 work 目录 | 预设导入（幂等 upsert） |
| 写 `build-manifest.json` 产物声明 | 镜像入仓（远端失败回退 docker save 上传 tar） |
| `--repush` 分支：校验既有制品、重写 manifest | 制品上传（默认同版本覆盖；`--dedup-materials` 复用跳过） |
| — | 一键打包（materialRole 回填、提交、轮询、下载套件 zip） |

### 4.2 调用现场（精确口径）

```
<解释器> <hook 脚本绝对路径>
    --repo <仓库根绝对路径>          # 固定首位
    --work <work 目录绝对路径>       # 固定次位
    <按 recipe.params 声明顺序逐个参数>  # 见下
    [--repush]                      # 仅 repush 动作追加
```

- 解释器：ps1 → `powershell -NoProfile -ExecutionPolicy Bypass -File`；sh → `sh`；
- **ps1 侧参数翻译**：PowerShell 具名参数绑定不了 GNU kebab（`--base-version` 双横线不是参数记号，
  全部落成位置参数塞爆 `param()` 后报「找不到与形参…匹配的位置形式参数」）——核心对 ps1 钩子
  **自动把 kebab 翻译成 PascalCase 具名参数**传递（`--base-version` → `-BaseVersion`、`--ui` → `-Ui`）；
  sh 钩子原样收 GNU kebab。下方调用现场与规则表统一按 kebab 书写（参数集合两侧一致）；
- **cwd = 仓库根**；stdout/stderr 直通核心进程（向导里实时进日志窗口）；
- 环境变量 **`COLLECT_KIT_BIN`** = collect-kit 可执行文件绝对路径（钩子可调 `"$COLLECT_KIT_BIN" zip-dir <src> <dest.zip>` 等通用工具；缺失说明不是被核心调起的，应当退出）；
- **退出码非零 = 动作失败**（核心打印 `构建失败 ✘` 并退出 1）。

**参数传递规则**（逐条按声明顺序追加）：

| 情形 | 是否传 |
| --- | --- |
| kind=modules 且勾选非空 | `--modules a,b,c`（CSV） |
| kind=middleware 且勾选非空 | `--middleware a,b`（CSV） |
| 其余 kind：终值为空 | 跳过 |
| 其余 kind：终值**等于声明缺省且 kind≠text** | 跳过（钩子侧缺省一致，靠默认值兜底） |
| 其余 kind：其他 | `--kebab-name 终值`（text **等于缺省也传**——如 version 缺省 1.0.0 要进钩子） |

终值优先级：显式值 > `defaultFromScan`（baseVersion）> `default`。

**work 目录口径**：`<out>/<name>-<version>`（out 缺省 `./tenant-kit`，相对路径基于仓库根）。
collect 启动时**先清空**该目录；repush **保留**（靠它定位既有制品）。

### 4.3 `--repush` 分支要求

收到 `--repush` 时钩子必须：**跳过全部构建** → 校验 `--work` 下既有制品存在（缺则报错提示先跑一次构建）
→ 以既有制品重写 `build-manifest.json`（appImages/materials 照常声明）→ 正常退出。核心另有防浪费闸：
启动前登录预检；制品默认同版本覆盖推送（`--dedup-materials` 切省流量复用跳过）。

### 4.4 sh / ps1 双平台对等要求与坑位

两脚本必须**行为对等**（同参数 → 同产物 → 同 manifest），维护时成对修改。ai-mse 实战沉淀的坑位：

- **ps1 生成文件禁 BOM**：PowerShell 5.1 的 `Set-Content -Encoding UTF8` 带 BOM，而生成的 pom.xml /
  nginx.conf 会被严格解析器读取，BOM 直接导致解析失败——用 `[IO.File]::WriteAllText($path, $text)`（无 BOM）；
- **ps1 外部命令退出码必须检查**：`$ErrorActionPreference="Stop"` **不拦**原生命令非零退出，mvn/docker/npm
  失败要显式 `if ($LASTEXITCODE -ne 0) { throw … }`；
- sh 侧 `set -e` 起步；ps1 侧参数用具名 `param(...)` 声明（参数名 = kebab 对应 PascalCase，核心自动翻译）；
- 脚本内文本只做 ASCII 或显式 UTF-8 写出，避免依赖控制台代码页；
- sh 收到未知参数应当报错退出（早暴露核心/钩子两侧参数名不同步）。

---

## §5 产物合同：`build-manifest.json`

钩子把产物声明写到 `<work>/build-manifest.json`，核心消费后接管全部 studio 交互。**路径一律相对 work 目录**。

```jsonc
{
  "appImages": ["my/app:1.0.0"],                    // 应用镜像 ref 清单（核心逐个入仓）
  "materials": [                                     // 上传制品清单
    { "path": "build/target/app.jar", "kind": "artifact", "appCode": "my-app",
      "version": "1.0.0",           // 缺省 = 套件 version 参数
      "remark": "collect-kit t-1.0.0", // 缺省 = "collect-kit <name>-<version>"
      "role": "ui" }                  // 非空时供 package.units 按 materialRole 引用
  ],
  "package": {                                       // --package 一键打包请求（可选段）
    "name": "tenant-a",              // 可省略——核心用 --name/--version 参数补齐（真工作台硬要求非空）
    "version": "1.0.0",
    "appCode": "my-app",             // 须在 studio-preset 中有应用定义
    "units": [
      { "code": "app", "source": "image_ref", "imageRef": "my/app:1.0.0" },
      { "code": "web", "source": "artifact", "materialRole": "ui" }   // 引用 materials[].role
    ],
    "componentCodes": ["mysql", "redis"],
    "configCenterInclude": ["share/common.yaml"]     // 可选：配置中心 DataID 白名单裁剪
  },
  "summary": ["fat-jar 就绪", "前端 zip 就绪"]        // 结果汇总行（核心末尾统一输出）
}
```

**PackageUnit 的 `source` 三选一**：`image_ref`（配 `imageRef`）/ `artifact`（配 `materialRole` 或
`materialId`）/ `image_tar`。`materialRole` 由核心在提交前查 `role → 实际上传 materialId` 回填——
所以 **role 必须对应一条上传成功的制品**。

### 核心消费语义分级（哪些错致命、哪些容错）

| 级别 | 情形 |
| --- | --- |
| **致命**（动作失败退出） | manifest 不存在 / JSON 不合法（`hook 未产出 …` / `hook 产物合同不合规`） |
| | manifest 制品文件在 work 下不存在（`manifest 制品不存在: …（hook 与 core 的 workdir 口径不一致？）`） |
| | `--package` 时 manifest 无 `package` 段（`hook 未声明 package…无法一键打包`） |
| | 打包单元引用的 `materialRole` 没有对应**上传成功**的制品 |
| **容错**（WARN 继续） | 预设导入失败（推送段不依赖它必成） |
| | 单个镜像入仓失败（本地有镜像时工作台可回退 docker save，见 studio.go） |
| | 单个制品上传失败（跳过继续传下一个） |

**制品推送语义（默认同版本覆盖）**：缺省总是上传——deliver-studio 服务端按同身份（类型+文件名+应用+版本）
保留最新一条：不同版本各自共存、同版本重推覆盖为最新（记录/文件/上传时间刷新，旧记录由服务端清理）。
`--dedup-materials` 省流量模式：上传前查既有制品，`文件名 + 大小 + appCode + version` **四元组全等 →
复用既有 materialId 跳过上传**（内容未变时免重传，汇总显示「复用制品」）。

**产物流向**：镜像 → 工作台镜像仓；制品 → 工作台制品库；`--package` → 工作台打包任务（核心轮询至完成，
3s × 600 次上限 30 分钟）→ 套件 zip 下载到 `<out 父目录>/<name>-<version>.zip`。

---

## §6 `studio-preset.json`（可选：deliver-studio 预设）

**何时需要**：只用 collect 本地构建/推送制品 → 不需要；要 `--package` **一键打包成套件** → 需要
（打包请求的 `appCode`/`componentCodes` 必须能在工作台应用/组件目录中命中）。

结构概要（完整格式属 deliver-studio 契约，见 `docs/tech/deliver-studio.md` 与
`tools/mse-deliver/deliver-studio/presets/` 内置示例）：

| 顶层段 | 内容 |
| --- | --- |
| `apps[]` | 应用定义：`code` / `defaultVersion` / `units[]`（单元 code、source、端口、健康检查、Dockerfile/compose 模板）/ `variables[]`（安装变量）/ `defaultComponents` |
| `components[]` | 组件定义：`code` / `category` / `images[]` / `composeServices[]` / `endpoints[]`（占位符→服务端口）/ `credentials[]`（ask/const/random） |

**install 变量与交付附件预填**：应用声明的 `when:"install"` 变量（如 `TENANT_ID/TENANT_CODE/TENANT_NAME/
LICENSE_PUBLIC_KEY` 四件套）会被交付链用于附件 `vars.env` 预填——套件重打包最终交付包时，平台签发的租户
变量按**同名变量**自动预填到安装控制台表单；未声明同名变量则仅出非阻断告警（安装时手填）。

**导入机制**：`preset` 声明的文件在推送类动作启动时先幂等导入（upsert，超时 1 分钟）；也可单独执行
`collect-kit upload-preset --recipe <名/路径> --studio <url> --studio-pass ***`。

**维护口径**（仓库内官方 recipe）：`recipes/ai-mse/studio-preset.json` 是权威源，与
`tools/mse-deliver/deliver-studio/presets/ai-mse.json` **双份字节一致**维护（改动两侧同步 cp+cmp 验证）。
第三方自有 recipe 自行维护单份即可。

---

## §7 向导与 CLI 联动（recipe 声明的运行面表现）

**向导**（无参启动，`127.0.0.1:7861`，`-addr/-recipe/-repo/-open/-dry-run` 可调）：

- 多内置 recipe 时页面顶部出现「采集方案（recipe）」下拉，切换即重新扫描 + 重渲染表单并重置执行面板（任务运行中拒绝切换；`-recipe` 启动参数可预选并锁定单一）；
- 表单按 params 动态渲染（六种 kind 见 3.3 表）；模块勾选卡 = 扫描结果（短名 + probe 标注），中间件勾选卡 =
  `middleware` 目录——**两张卡与 params 声明无关都会渲染，但只有声明了对应参数勾选才生效**；
- 动作单选按 actions 声明显隐（collect/push-images/repush；repush 可用性 = 声明了 collect）；
- `preview` 按钮 = 只校验 + 回显将执行的命令（密码打码）；`-dry-run` = start 也只回显不执行；
- 提交后单任务串行执行，页面 1.5s 轮询日志/状态，打包完成出下载按钮。

**通用校验**（CLI 与向导同源）：套件名 `^[a-z0-9][a-z0-9-]*$`、版本 `^[A-Za-z0-9][A-Za-z0-9._-]*$`、
`--modules` 逐项对扫描清单、`--middleware` 对目录、choice 对取值域、text 对 pattern；
`name` 参数在 collect/repush 时必填非空。

**CLI 固定 flag**（与参数声明无关，永远可用）：`--recipe` `--repo`（覆盖 repoRoot 推导）`--studio`
`--studio-user`（缺省 admin）`--studio-pass` `--package` `--modules` `--middleware` `--extra-images`
`--dedup-materials`（制品省流量复用：同名同大小同版本命中跳过上传；缺省总是上传、服务端同版本覆盖为最新）。
推送类动作启动前登录预检：密码错/工作台未启动当场拦截；`collect --package` 不带 `--studio` 时缺省
`http://127.0.0.1:7860`。

**deliver-studio REST 面**（核心封装，recipe 无感，排障时知晓即可）：`POST /api/login`（token 走
`X-Studio-Token`）、`POST /api/components/import`（预设）、`POST /api/images`（镜像；`{"ref"}` 失败回退
docker save tar 上传）、`GET/POST /api/materials`（查重/上传）、`POST /api/packages`（打包）、
`GET /api/packages/{id}`（轮询）、`GET /api/packages/{id}/download`（下载）。

---

## §8 测试与验证

**手工验证流程**（新 recipe / 改动后的标准动线）：

1. `collect-kit list-recipes`（内置）/ `--recipe <路径>` 能加载 → 校验通过；
2. 向导打开 → 表单/勾选卡渲染符合预期 → **preview 回显命令**核对参数（含勾选是否带上 `--modules/--middleware`）；
3. `-dry-run` 或不带 `--studio` 跑 collect → 钩子产物与 `build-manifest.json` 落位正确（work 目录、
   制品路径相对口径、role 引用）；
4. 带 `--studio`（本地起一个 deliver-studio）实跑 → 制品去重、镜像入仓、`--package` 打包链路。

**Go 回归测试范式**（合入 collect-kit 源码树的 recipe 建议配测试，参考 `collect_test.go`）：

- `goodRecipe` 常量 + `writeRecipe` 落临时目录 → `LoadRecipe` 校验通过 + 反例族（format 错 / 名字错 /
  kind 非法 / choice 空 / pattern 非正则）逐一拒载；
- e2e：临时 recipe + **假钩子 sh 脚本**（解析参数、产 jar/zip、写完整 manifest）+ `httptest` **假工作台**
  （fakeStudio：login/import/images/materials/packages/download 全端点）→ 断言预设导入次数、镜像/制品
  上传数、打包请求体（name 补齐、materialRole 回填 materialId）、去重复用；
- 内置 recipe 守门断言（防回退）：`TestBuiltinAiMseRecipe` 校验 preset 就位、双平台 hook 存在、
  params 含 `modules`/`middleware`（kind 正确）、preset 应用声明附件四件套——自建内置 recipe 照此守门。

跑法：`go vet ./... && go test ./...`（collect-kit 目录）。

---

## §9 参考实现导读：`recipes/ai-mse/`

**recipe.json 声明意图**：

- `scan`：`mse-modules` 下 `mse-` 前缀目录、须含 `<短名>-service` 子目录才算业务模块；`-ui/-mobile-ui`
  探测标注；基座版本取 `mse-framework/pom.xml` 首个 `<version>`；
- `middleware` 目录七项（mysql/redis/rocketmq/elasticsearch/rustfs/milvus/openim），对应 pushImages.middleware
  七组镜像；mysql/redis 同时是打包组件勾选项——collect 钩子的 componentCodes 按 `--middleware` 勾选生成
  （micro 形态恒含 nacos），未勾 mysql/redis 时套件不含对应组件、DB/Redis 端点经 preset 应用安装变量接外部库；
- params 六个业务参数（name/version/form/ui/out/baseVersion）+ modules/middleware 两条功能声明；
- `repushProfiles: ["mono"]`：仅单体形态支持重推（micro 多镜像无统一制品指纹）；
- pushImages：无 always（mysql/redis 走中间件勾选组）；form=micro 追加 nacos；elasticsearch 走 `{build,context}` 本地构建
  （智能分词镜像），其余 `{ref}` 直推。

**钩子执行流程**（collect.sh / collect.ps1 对等）：

1. 解析 `--repo/--work/--name/--version/--form/--ui/--base-version/--out/--modules/--middleware/--repush`
   （ps1 侧为同名 PascalCase 具名参数，核心自动翻译）；
   `COLLECT_KIT_BIN` 缺失即退出（必须被核心调起）；`--base-version` 未传时自行从 pom 提取兜底；
2. `--repush` 分支：校验既有 fat-jar（+ 可选 UI zip）存在 → 不构建，直接以既有产物重写 manifest；
3. `--form=micro`：逐服务 `mvn package` + `docker build`（基座五件套固定 + 勾选业务 boot，端口表硬编码）；
4. `--form=mono`：动态生成聚合 Maven 工程（tenant-starter + 勾选模块 service）→ fat-jar → 应用镜像；
5. `--ui=true`：基座/移动基站/勾选模块的 PC 与移动前端 dist 收集 + nginx.conf（`/api` 反代 app 或 gateway）
   → `$COLLECT_KIT_BIN zip-dir` 打 UI zip（role=ui）；
6. 写 `build-manifest.json`：appImages（应用镜像）/ materials（jar + UI zip）/ package（appCode 按形态与
   UI 开关三选一；units = 镜像单元 + web 制品单元 materialRole=ui；componentCodes = mysql+redis+形态追加+
   勾选中间件；micro 另有 configCenterInclude 白名单）/ summary。

**studio-preset.json**：三应用（mse-monolith / mse-monolith-headless / mse-micro）+ 八组件（mysql/redis/
rocketmq/elasticsearch/rustfs/milvus/openim/nacos）；三应用均声明附件四件套 install 变量（vars.env 预填）。

---

## §10 FAQ / 坑位清单

- **勾选了模块但钩子收不到 `--modules`** → params 漏声明 `{"name":"modules","kind":"modules"}`（见 3.3 ⚠）；
- **push-images 只推了 mysql/redis** → 没勾选中间件（勾什么推什么，未勾/未传 `--middleware` 时中间件段一个不推；要全备就显式列全目录）；同理 `--modules`/`--package` 对本动作无效，传入只打「已忽略」警告——制品入库/打包走 collect；
- **manifest 制品不存在（致命）** → materials.path 必须相对 **work 目录**，不是仓库根也不是绝对路径；
- **打包提示引用的制品角色未上传成功** → `package.units[].materialRole` 必须命中一条 `materials[].role`
  **且该制品上传成功**（上游上传失败只是 WARN，打包引用它才致命）；
- **`--package` 报 hook 未声明 package** → manifest 缺 `package` 段；本地构建不需要可不写；
- **Windows 走了 sh 而不是 ps1** → 平台选择只看声明：Windows + 有 ps1 才用 ps1；
- **ps1 生成文件被下游解析器拒收** → BOM 问题，用 `[IO.File]::WriteAllText`（§4.4）；
- **同版本重推后旧制品记录消失** → 这是设计：服务端按同身份（类型+文件名+应用+版本）保留最新一条，
  旧记录连同文件清理、上传时间刷新（不同版本各自共存）；要省流量跳过重传用 `--dedup-materials`
  （四元组全等复用，汇总显示「复用制品」）；
- **向导里改了 recipe.json 不生效** → 向导启动时加载一次，改完重启向导；
- **collect 报「当前平台无可用 hook 脚本」** → Windows 只声明了 sh 且无 sh 可用环境，补 ps1；
- **想在仓库外放 recipe** → `--recipe <目录路径>` 直接加载，合法且推荐（§2）。

## 附录：错误信息速查表

| 错误信息（原文） | 来源 | 触发条件 / 排查 |
| --- | --- | --- |
| `recipe 参数须为内置名（recipes/ 下目录）或 recipe 目录路径: x` | LoadRecipe | --recipe 取值既非 kebab 名也非路径 |
| `recipe.json format 须为 1（当前 n）` | validate | 合同版本不符 |
| `recipe name 须为 kebab 形态: x` | validate | name 非 `^[a-z][a-z0-9-]*$` |
| `参数 x 的 kind 非法: y（…）` | validate | kind 拼错 |
| `参数 x（choice）缺少 choices` | validate | choice 参数空选项 |
| `recipe 至少声明一个动作（collect / pushImages）` | validate | actions 全空 |
| `collect 动作缺少 hook（sh/ps1 至少其一）` | validate | hook 空声明 |
| `缺少 --recipe（内置: …；或 --recipe <recipe 目录路径>）` | CLI | 多内置未指定 |
| `未知参数 --x（recipe y 未声明；已声明: …）` | checkParams | flag 拼错 / params 未声明 |
| `未知业务模块: x（不在 <dir> 扫描清单内）` | validateModules | --modules 取值不在扫描结果（requireSuffix 门禁？） |
| `未知中间件 x（可选: …）` | assemblePushRefs（CLI）/ 向导表单校验（`未知中间件: x`） | --middleware 取值不在中间件目录 ∪ pushImages 分组键（勾什么推什么配套防呆） |
| `参数 x 的取值 "y" 不合法（pattern）` | checkParams | text 违反 pattern |
| `形态 x 暂不支持重推（recipe 声明仅 […]）` | checkRepushSupport | form 不在 repushProfiles |
| `当前平台（os）无可用 hook 脚本` | HookScript | 平台无匹配脚本 |
| `hook 脚本不存在: 路径` | runHook | hook 相对路径基于 recipe 目录，检查落位 |
| `hook 未产出 <work>/build-manifest.json` | readManifest | 钩子没写 / 写错位置 |
| `hook 产物合同不合规: …` | RunCollect | manifest 不存在或 JSON 非法 |
| `manifest 制品不存在: 路径` | RunCollect | materials.path 相对 work 口径错 |
| `hook 未声明 package（build-manifest.json 缺 package 段），无法一键打包` | RunCollect | --package 但 manifest 无 package |
| `打包单元 x 引用的制品角色 y 未上传成功` | RunCollect | materialRole 无对应成功上传 |
| `推送工作台需要登录密码（…）` | CLI/向导 | 有 --studio 无 --studio-pass |
| `工作台无法连接（… 未启动/地址不对？）` | studio.go | --studio 地址错 / 工作台未启动 |
| `读取模块目录失败（仓库根不对？）` | ScanRepo | repoRoot/modulesDir 配错（模块校验降级跳过） |
