// collect-kit：通用可配置采集器（三段交付管道第一段「构建」，deliver-design.md ADR-9 配套）。
// 核心零仓库知识：一切仓库行为（目录约定/构建链/镜像目录/打包单元构造）来自 recipe（采集方案插件），
// 核心只懂三件事——recipe 合同（参数/扫描规则/镜像目录声明）、产物合同（build-manifest.json）、
// deliver-studio REST。外部 recipe 目录经 --recipe <目录路径> 加载（见 docs/recipe.md）。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ---- recipe 合同（recipe.json，format:1） ----

// Recipe 采集方案：参数声明 + 仓库扫描规则 + 动作（collect 钩子 / push-images 镜像目录）
type Recipe struct {
	Format    int          `json:"format"` // 1
	Name      string       `json:"name"`   // 唯一编码（kebab）
	Title     string       `json:"title"`
	Desc      string       `json:"description,omitempty"`
	Preset    string       `json:"preset,omitempty"` // 随附 deliver-studio 预设 JSON（推送类动作先导入）
	RepoRoot  string       `json:"repoRoot"`         // 仓库根（相对 recipe 目录；hooks 与扫描的基准）
	Scan      *ScanSpec    `json:"scan,omitempty"`   // 模块/版本扫描规则（kind=modules 参数与向导模块清单）
	Middle    []MwDecl     `json:"middleware,omitempty"`
	Params    []ParamDecl  `json:"params"`
	Actions   Actions      `json:"actions"`
	loadedDir string       `json:"-"` // recipe.json 所在目录（解析 hook/preset 相对路径）
}

// ScanSpec 通用仓库扫描规则：模块目录前缀 + 必备子目录后缀 + 探测子目录标注 + 基座版本提取
type ScanSpec struct {
	ModulesDir     string     `json:"modulesDir"`     // 模块根目录（相对仓库根，如 mse-modules）
	ModulePrefix   string     `json:"modulePrefix"`   // 模块目录前缀（短名 = 目录名去前缀，如 mse-）
	RequireSuffix  string     `json:"requireSuffix"`  // 必备子目录后缀（如 -service；缺失则模块不入清单）
	ProbeDirs      []ProbeDir `json:"probeDirs"`      // 探测子目录（向导标注，如 -ui / -mobile-ui）
	VersionFile    string     `json:"versionFile"`    // 基座版本提取文件（相对仓库根，取首个 <version>）
	VersionDefault string     `json:"versionDefault"` // 提取失败兜底
}

// ProbeDir 探测子目录标注：<短名><suffix> 存在即标注（如 oa-ui → ui）
type ProbeDir struct {
	Suffix string `json:"suffix"`
	Label  string `json:"label"`
}

// MwDecl 中间件目录项（勾选驱动：镜像备料清单 + 透传 hook 的 --middleware）
type MwDecl struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// ParamDecl 参数声明（CLI flag 与向导表单同源）
type ParamDecl struct {
	Name    string   `json:"name"`    // 小写驼峰 → CLI --kebab-case；向导表单字段名
	Kind    string   `json:"kind"`    // text|choice|bool|modules|middleware|out
	Label   string   `json:"label"`   // 向导展示
	Default string   `json:"default,omitempty"`
	Pattern string   `json:"pattern,omitempty"` // text 合法性（正则）
	Choices []Choice `json:"choices,omitempty"` // kind=choice
	// DefaultFromScan 非空时缺省值取扫描结果键（当前支持 baseVersion——VersionFile 提取值）
	DefaultFromScan string `json:"defaultFromScan,omitempty"`
}

// Choice 单选项
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Actions 动作声明
type Actions struct {
	Collect    *CollectAction `json:"collect,omitempty"`
	PushImages *PushImages    `json:"pushImages,omitempty"`
}

// CollectAction 构建动作：hook 脚本（sh/ps1 双平台，按 OS 选用）+ 支持重推的 profile 集
type CollectAction struct {
	Hook          HookSpec `json:"hook"`
	RepushProfiles []string `json:"repushProfiles,omitempty"` // 空 = 全部 profile 支持重推；列举 = 仅这些
}

// HookSpec 双平台钩子（相对 recipe 目录；windows 优先 ps1，无 ps1 或非 windows 用 sh）
type HookSpec struct {
	Sh string `json:"sh,omitempty"`
	Ps string `json:"ps1,omitempty"`
}

// PushImages 镜像备料动作：全声明式（无 hook）
type PushImages struct {
	Always     []string              `json:"always,omitempty"`     // 恒入仓镜像
	WhenParam  []CondImages          `json:"whenParam,omitempty"`  // 参数条件镜像
	Middleware map[string][]ImageRef `json:"middleware,omitempty"` // 中间件 → 镜像清单
}

// CondImages 参数条件镜像（如 form=micro 追加 nacos）
type CondImages struct {
	Param  string   `json:"param"`
	Equals string   `json:"equals"`
	Images []string `json:"images"`
}

// ImageRef 镜像条目：直接引用（ref）或本地构建（build 镜像名 + context 相对仓库根的 Dockerfile 目录）
type ImageRef struct {
	Ref     string `json:"ref,omitempty"`
	Build   string `json:"build,omitempty"`   // 本地构建目标镜像（存在性检查失败先 docker build）
	Context string `json:"context,omitempty"` // 构建上下文目录（相对仓库根）
}

func (r ImageRef) target() string {
	if r.Ref != "" {
		return r.Ref
	}
	return r.Build
}

// ---- 产物合同（hook 写 <work>/build-manifest.json，core 消费后接管全部 studio 交互） ----

// BuildManifest hook 产物声明：路径一律相对 workdir
type BuildManifest struct {
	AppImages []string           `json:"appImages,omitempty"` // 应用镜像（入仓制品之一）
	Materials []ManifestMaterial `json:"materials,omitempty"` // 上传制品（去重复用由 core 处理）
	Package   *PackagePlan       `json:"package,omitempty"`   // 打包请求构造（--package 时提交）
	Summary   []string           `json:"summary,omitempty"`   // 结果汇总行（core 末尾统一输出）
}

// ManifestMaterial 上传制品：role 非空（如 ui）时其 materialId 供 Package.Units 按 materialRole 回填
type ManifestMaterial struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`    // artifact|image_tar|extra
	AppCode string `json:"appCode"`
	Version string `json:"version,omitempty"` // 缺省用套件版本
	Remark  string `json:"remark,omitempty"`
	Role    string `json:"role,omitempty"`   // 如 ui（打包单元引用其 materialId）
}

// PackagePlan 打包请求（materialRole 在提交前由 core 换成实际上传 materialId；
// name/version 可省略——core 用 --name/--version 参数补齐，真 studio 的 AssembleRequest 硬要求非空 name）
type PackagePlan struct {
	Name                string        `json:"name"`
	Version             string        `json:"version,omitempty"`
	AppCode             string        `json:"appCode"`
	Units               []PackageUnit `json:"units"`
	ComponentCodes      []string      `json:"componentCodes,omitempty"`
	ConfigCenterInclude []string      `json:"configCenterInclude,omitempty"`
	Standalone          bool          `json:"standalone,omitempty"`
}

// PackageUnit 打包单元 pick：ImageRef/MaterialRole 三选一
type PackageUnit struct {
	Code        string `json:"code"`
	Source      string `json:"source"` // image_ref|artifact|image_tar
	ImageRef    string `json:"imageRef,omitempty"`
	MaterialID  int64  `json:"materialId,omitempty"`
	MaterialRef string `json:"materialRole,omitempty"` // 引用 materials[].role（core 回填 materialId）
}

// ---- recipe 加载与校验 ----

var recipeNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// LoadRecipe 解析 recipe.json；name 为内置名（recipes/ 下目录名）或显式目录路径
func LoadRecipe(builtinRoot, name string) (*Recipe, error) {
	var dir string
	if filepath.IsAbs(name) || filepath.Dir(name) != "." && !recipeNameRe.MatchString(filepath.Base(name)) {
		dir = name
	} else if recipeNameRe.MatchString(name) {
		dir = filepath.Join(builtinRoot, name)
	} else {
		return nil, fmt.Errorf("recipe 参数须为内置名（recipes/ 下目录）或 recipe 目录路径: %s", name)
	}
	data, err := os.ReadFile(filepath.Join(dir, "recipe.json"))
	if err != nil {
		return nil, fmt.Errorf("读取 recipe 失败: %w", err)
	}
	var r Recipe
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("recipe.json 解析失败: %w", err)
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	r.loadedDir = dir
	return &r, nil
}

func (r *Recipe) validate() error {
	if r.Format != 1 {
		return fmt.Errorf("recipe.json format 须为 1（当前 %d）", r.Format)
	}
	if !recipeNameRe.MatchString(r.Name) {
		return fmt.Errorf("recipe name 须为 kebab 形态: %s", r.Name)
	}
	names := map[string]bool{}
	for _, p := range r.Params {
		if p.Name == "" {
			return fmt.Errorf("参数缺少 name")
		}
		if names[p.Name] {
			return fmt.Errorf("参数 %s 重复声明", p.Name)
		}
		names[p.Name] = true
		switch p.Kind {
		case "text", "choice", "bool", "modules", "middleware", "out":
		default:
			return fmt.Errorf("参数 %s 的 kind 非法: %s（text/choice/bool/modules/middleware/out）", p.Name, p.Kind)
		}
		if p.Kind == "choice" && len(p.Choices) == 0 {
			return fmt.Errorf("参数 %s（choice）缺少 choices", p.Name)
		}
		if p.Pattern != "" {
			if _, err := regexp.Compile(p.Pattern); err != nil {
				return fmt.Errorf("参数 %s 的 pattern 非法: %w", p.Name, err)
			}
		}
	}
	if r.Actions.Collect == nil && r.Actions.PushImages == nil {
		return fmt.Errorf("recipe 至少声明一个动作（collect / pushImages）")
	}
	if r.Actions.Collect != nil && r.Actions.Collect.Hook.Sh == "" && r.Actions.Collect.Hook.Ps == "" {
		return fmt.Errorf("collect 动作缺少 hook（sh/ps1 至少其一）")
	}
	// pushImages 可选：未声明时这里曾是空指针 panic（内置 recipe 两动作齐备从未暴露）
	if pi := r.Actions.PushImages; pi != nil {
		for code, refs := range pi.Middleware {
			for i, ref := range refs {
				if ref.target() == "" {
					return fmt.Errorf("pushImages.middleware[%s][%d] 缺少 ref/build", code, i)
				}
				if ref.Build != "" && ref.Context == "" {
					return fmt.Errorf("pushImages.middleware[%s][%d] build 项缺少 context", code, i)
				}
			}
		}
	}
	return nil
}

// Dir recipe.json 所在目录（hook/preset 相对路径基准）
func (r *Recipe) Dir() string { return r.loadedDir }

// RepoRootAbs 仓库根绝对路径
func (r *Recipe) RepoRootAbs() (string, error) {
	if r.RepoRoot == "" {
		return filepath.Abs(r.loadedDir)
	}
	p := filepath.Join(r.loadedDir, r.RepoRoot)
	return filepath.Abs(p)
}

// HookScript 按平台选 hook 脚本（绝对路径）与解释器前缀；无可用脚本报错
func (r *Recipe) HookScript(goos string) (interp []string, script string, err error) {
	h := r.Actions.Collect.Hook
	var rel string
	if goos == "windows" && h.Ps != "" {
		rel = h.Ps
		interp = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File"}
	} else if h.Sh != "" {
		rel = h.Sh
		interp = []string{"sh"}
	} else if h.Ps != "" {
		rel = h.Ps
		interp = []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File"}
	} else {
		return nil, "", fmt.Errorf("当前平台（%s）无可用 hook 脚本", goos)
	}
	if filepath.IsAbs(rel) {
		return interp, rel, nil
	}
	return interp, filepath.Join(r.loadedDir, filepath.FromSlash(rel)), nil
}

// Param 按名取参数声明
func (r *Recipe) Param(name string) *ParamDecl {
	for i := range r.Params {
		if r.Params[i].Name == name {
			return &r.Params[i]
		}
	}
	return nil
}

// ChoiceLabel choice 参数取值展示
func (p *ParamDecl) ChoiceLabel(value string) string {
	for _, c := range p.Choices {
		if c.Value == value {
			return c.Label
		}
	}
	return value
}

// kebabFlag 参数名 → CLI flag（camelCase → kebab；已是单词原样）
var camelRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)

func kebabFlag(name string) string {
	return "--" + strings.ToLower(camelRe.ReplaceAllString(name, "${1}-${2}"))
}

// psParamFlag --kebab-flag → -PascalName（ps1 钩子具名参数；PowerShell 参数记号要求单横线+词首字符，
// GNU 双横线无法绑定 param() 声明，核心对 ps1 钩子统一翻译后传参）
func psParamFlag(kebab string) string {
	var b strings.Builder
	b.WriteByte('-')
	for _, part := range strings.Split(strings.TrimPrefix(kebab, "--"), "-") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}
