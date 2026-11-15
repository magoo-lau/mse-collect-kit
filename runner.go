// 动作编排：collect（hook 构建 → build-manifest → 预设/镜像/制品推送 → 可选一键打包）、
// push-images（纯声明式镜像备料）、repush（hook 以 --repush 重写 manifest，免重建重推）。
// 仓库知识与 studio 知识被「产物合同」隔离在 hook 与 core 两侧。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Options 一次动作的全部输入（CLI/向导同源）
type Options struct {
	Recipe     string // 内置名或目录路径
	Action     string // collect|push-images|repush|upload-preset
	Repo       string // 仓库根覆盖（缺省 recipe.repoRoot 推导）
	Params     map[string]string
	Modules    []string
	Middleware []string
	ExtraImages []string

	StudioURL   string
	StudioUser  string
	StudioPass  string
	Package     bool
	DedupMaterials bool // --dedup-materials：同名同大小同版本命中复用跳过上传（省流量）；缺省总是上传，同版本覆盖交给服务端同身份保留最新
}

// Result 汇总（向导下载位/CLI 尾部输出）
type Result struct {
	KitZip string
	SummaryOK   []string
	SummaryWarn []string
}

// summary 收集器（OK/WARN 分栏，末尾统一输出——同旧脚本行为）
type summary struct{ r *Result }

func (s summary) ok(format string, args ...any)   { s.r.SummaryOK = append(s.r.SummaryOK, fmt.Sprintf(format, args...)) }
func (s summary) warn(format string, args ...any) { s.r.SummaryWarn = append(s.r.SummaryWarn, fmt.Sprintf(format, args...)) }

// paramValue 取参数终值（显式值 > 声明缺省 > 扫描缺省）；bool 型缺省 false
func paramValue(r *Recipe, scan *ScanResult, params map[string]string, name string) string {
	if v, ok := params[name]; ok && v != "" {
		return v
	}
	p := r.Param(name)
	if p == nil {
		return ""
	}
	if p.DefaultFromScan == "baseVersion" && scan != nil {
		return scan.BaseVersion
	}
	return p.Default
}

// workDir collect 输出目录：<out>/<name>-<version>（与旧脚本口径一致，repush 靠它定位既有制品）
func workDir(r *Recipe, scan *ScanResult, params map[string]string) string {
	out := paramValue(r, scan, params, "out")
	if out == "" {
		out = "./tenant-kit"
	}
	if !filepath.IsAbs(out) {
		root, err := r.RepoRootAbs()
		if err == nil {
			out = filepath.Join(root, out)
		}
	}
	name := paramValue(r, scan, params, "name")
	version := paramValue(r, scan, params, "version")
	return filepath.Join(out, name+"-"+version)
}

// RunCollect 构建/重推动作主流程
func RunCollect(r *Recipe, o *Options, logf func(string, ...any)) (*Result, error) {
	res := &Result{}
	s := summary{r: res}
	repoRoot, err := resolveRepoRoot(r, o)
	if err != nil {
		return nil, err
	}
	scan, err := ScanRepo(repoRoot, r.Scan)
	if err != nil {
		logf("提示：仓库扫描失败（%v）——模块参数校验跳过", err)
		scan = &ScanResult{RepoRoot: repoRoot}
	}
	if err := validateModules(r, scan, o); err != nil {
		return nil, err
	}
	repush := o.Action == "repush"
	if err := checkRepushSupport(r, o, repush); err != nil {
		return nil, err
	}

	work := workDir(r, scan, o.Params)
	if !repush {
		_ = os.RemoveAll(work)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(work, "build-manifest.json")

	// ---- hook 执行（构建或 --repush 重写 manifest）----
	logf("==> [1/3] 执行 recipe hook（%s）", r.Name)
	if err := runHook(r, repoRoot, scan, o, work, repush, logf); err != nil {
		return nil, err
	}
	manifest, err := readManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("hook 产物合同不合规: %w", err)
	}
	for _, line := range manifest.Summary {
		s.ok("%s", line)
	}
	if repush {
		s.ok("重推既有制品（跳过构建，复用 %s）", work)
	}

	// ---- 推送（--upload/--package/repush 必有目标）----
	if o.StudioURL == "" {
		logf("==> [2/3] 跳过推送（未指定工作台地址）——制品目录: %s", work)
		logf("==> [3/3] 完成")
		return res, nil
	}
	logf("==> [2/3] 推送到 deliver-studio（%s）", o.StudioURL)
	client := NewStudioClient(o.StudioURL, orDefault(o.StudioUser, "admin"), o.StudioPass)
	if err := client.Login(); err != nil {
		return nil, err
	}
	if r.Preset != "" {
		presetPath := filepath.Join(r.Dir(), filepath.FromSlash(r.Preset))
		if err := client.ImportPreset(presetPath); err != nil {
			logf("    警告：预设推送失败（%v）", err)
			s.warn("推送组件预设：失败")
		} else {
			s.ok("推送组件预设（%s）", filepath.Base(presetPath))
		}
	}
	version := paramValue(r, scan, o.Params, "version")
	name := paramValue(r, scan, o.Params, "name")
	for _, img := range manifest.AppImages {
		if err := client.PushImage(img, logf); err != nil {
			logf("    警告：镜像入仓失败 %s（本地/工作台均不可得）", img)
			s.warn("镜像入仓 %s：失败", img)
		} else {
			s.ok("镜像入仓 %s", img)
		}
	}
	roleIDs := map[string]int64{}
	for _, m := range manifest.Materials {
		path := filepath.Join(work, filepath.FromSlash(m.Path))
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("manifest 制品不存在: %s（hook 与 core 的 workdir 口径不一致？）", path)
		}
		ver := orDefault(m.Version, version)
		id, reused, err := client.UploadMaterial(path, orDefault(m.Kind, "artifact"), m.AppCode, ver,
			orDefault(m.Remark, fmt.Sprintf("collect-kit %s-%s", name, version)), o.DedupMaterials, logf)
		if err != nil {
			logf("    警告：上传制品失败 %s（%v）", m.Path, err)
			s.warn("上传制品 %s：失败", filepath.Base(m.Path))
			continue
		}
		if reused {
			s.ok("复用制品 %s（#%d 同名同大小，未重传）", filepath.Base(m.Path), id)
		} else {
			s.ok("上传制品 %s（materialId=%d）", filepath.Base(m.Path), id)
		}
		if m.Role != "" {
			roleIDs[m.Role] = id
		}
	}

	// ---- 一键打包 ----
	logf("==> [3/3] 打包")
	if !o.Package {
		logf("    未指定 --package：到工作台「打包交付」向导手工打包，或加 --package 一站式出套件")
		return res, nil
	}
	if manifest.Package == nil {
		return nil, fmt.Errorf("hook 未声明 package（build-manifest.json 缺 package 段），无法一键打包")
	}
	plan := *manifest.Package
	if plan.Name == "" {
		plan.Name = name
	}
	if plan.Version == "" {
		plan.Version = version
	}
	for i := range plan.Units {
		u := &plan.Units[i]
		if u.MaterialRef != "" {
			id, ok := roleIDs[u.MaterialRef]
			if !ok {
				return nil, fmt.Errorf("打包单元 %s 引用的制品角色 %s 未上传成功", u.Code, u.MaterialRef)
			}
			u.MaterialID = id
			u.MaterialRef = ""
		}
	}
	pkgID, err := client.StartPackage(&plan)
	if err != nil {
		return nil, fmt.Errorf("打包任务提交失败: %w", err)
	}
	logf("    打包任务 #%d 已提交，轮询进度……", pkgID)
	if err := client.WaitPackage(pkgID, logf); err != nil {
		return nil, err
	}
	kitZip := filepath.Join(filepath.Dir(work), name+"-"+version+".zip")
	if err := client.DownloadPackage(pkgID, kitZip); err != nil {
		return nil, err
	}
	s.ok("运行时套件 %s（工作台打包 #%d）", kitZip, pkgID)
	res.KitZip = kitZip
	return res, nil
}

// RunPushImages 纯声明式镜像备料（always + 参数条件 + 勾选中间件 + 附加镜像；build 项先本地构建）
func RunPushImages(r *Recipe, o *Options, logf func(string, ...any)) (*Result, error) {
	res := &Result{}
	s := summary{r: res}
	if len(o.Modules) > 0 {
		logf("警告：--modules 对 push-images 无效（镜像备料与业务模块无关，制品入库走 collect），已忽略")
	}
	if o.Package {
		logf("警告：--package 对 push-images 无效（打包属 collect 动作），已忽略")
	}
	if o.StudioURL == "" {
		return nil, fmt.Errorf("镜像备料需要工作台地址（--studio <url>）")
	}
	pi := r.Actions.PushImages
	if pi == nil {
		return nil, fmt.Errorf("recipe %s 未声明 pushImages 动作", r.Name)
	}
	repoRoot, err := resolveRepoRoot(r, o)
	if err != nil {
		return nil, err
	}
	scan, _ := ScanRepo(repoRoot, r.Scan)
	refs, noImage, err := assemblePushRefs(r, scan, o)
	if err != nil {
		return nil, err
	}
	for _, code := range noImage {
		logf("中间件 %s 无备料镜像分组，跳过", code)
	}
	client := NewStudioClient(o.StudioURL, orDefault(o.StudioUser, "admin"), o.StudioPass)
	if err := client.Login(); err != nil {
		return nil, err
	}
	logf("==> 镜像备料（%s，%d 个）", o.StudioURL, len(refs))
	for _, ref := range refs {
		if ref.Build != "" {
			if exec.Command("docker", "image", "inspect", ref.Build).Run() != nil {
				ctx := filepath.Join(repoRoot, filepath.FromSlash(ref.Context))
				logf("    本地缺 %s，先构建（%s）", ref.Build, ref.Context)
				if out, err := exec.Command("docker", "build", "-t", ref.Build, ctx).CombinedOutput(); err != nil {
					return nil, fmt.Errorf("镜像 %s 构建失败: %v: %s", ref.Build, err, truncate(string(out), 200))
				}
			}
		}
		if err := client.PushImage(ref.target(), logf); err != nil {
			logf("    警告：镜像 %s 入仓失败（本地不存在且工作台不可得——docker pull 后重跑）", ref.target())
			s.warn("镜像入仓 %s：失败", ref.target())
			continue
		}
		s.ok("镜像入仓 %s", ref.target())
	}
	return res, nil
}

// assemblePushRefs 组装备料镜像清单：always + whenParam 命中 + --middleware 勾选组 + --extra-images（按 target 去重）。
// 中间件段只认勾选——未勾选任何中间件时一个不推（勾什么推什么，要全备就显式列全目录）；
// 未知 code 报错防打错字静默漏推；noImage 返回勾选了目录项但 pushImages 无镜像分组的 code（供调用方提示）。
func assemblePushRefs(r *Recipe, scan *ScanResult, o *Options) (refs []ImageRef, noImage []string, err error) {
	pi := r.Actions.PushImages
	if pi == nil {
		return nil, nil, fmt.Errorf("recipe %s 未声明 pushImages 动作", r.Name)
	}
	seen := map[string]bool{}
	add := func(list []string) {
		for _, t := range list {
			if t != "" && !seen[t] {
				seen[t] = true
				refs = append(refs, ImageRef{Ref: t})
			}
		}
	}
	add(pi.Always)
	for _, cond := range pi.WhenParam {
		if paramValue(r, scan, o.Params, cond.Param) == cond.Equals {
			add(cond.Images)
		}
	}
	known := map[string]bool{} // --middleware 合法取值 = 顶层中间件目录 ∪ pushImages 分组键
	for _, mw := range r.Middle {
		known[mw.Code] = true
	}
	for code := range pi.Middleware {
		known[code] = true
	}
	var selected, unknown []string
	for _, m := range o.Middleware {
		if m = strings.TrimSpace(m); m == "" {
			continue
		}
		selected = append(selected, m)
		if !known[m] {
			unknown = append(unknown, m)
		}
	}
	if len(unknown) > 0 {
		var opts []string
		for code := range known {
			opts = append(opts, code)
		}
		sort.Strings(unknown)
		sort.Strings(opts)
		return nil, nil, fmt.Errorf("未知中间件 %s（可选: %s）", strings.Join(unknown, ","), strings.Join(opts, "/"))
	}
	sort.Strings(selected) // 分组按勾选顺序稳定追加（pushImages.middleware 是 map，遍历序不保）
	for _, code := range selected {
		list, ok := pi.Middleware[code]
		if !ok {
			noImage = append(noImage, code)
			continue
		}
		for _, ref := range list {
			if ref.target() == "" || seen[ref.target()] {
				continue
			}
			seen[ref.target()] = true
			refs = append(refs, ref)
		}
	}
	for _, t := range o.ExtraImages {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			refs = append(refs, ImageRef{Ref: t})
		}
	}
	return refs, noImage, nil
}

// RunUploadPreset 仅推送预设（不构建）
func RunUploadPreset(r *Recipe, o *Options, logf func(string, ...any)) error {
	if o.StudioURL == "" {
		return fmt.Errorf("需要工作台地址（--studio <url>）")
	}
	if r.Preset == "" {
		return fmt.Errorf("recipe %s 未随附预设（preset）", r.Name)
	}
	client := NewStudioClient(o.StudioURL, orDefault(o.StudioUser, "admin"), o.StudioPass)
	if err := client.Login(); err != nil {
		return err
	}
	presetPath := filepath.Join(r.Dir(), filepath.FromSlash(r.Preset))
	if err := client.ImportPreset(presetPath); err != nil {
		return err
	}
	logf("预设推送完成 ✔（%s → %s）", presetPath, o.StudioURL)
	return nil
}

// ---- hook 执行 ----

func runHook(r *Recipe, repoRoot string, scan *ScanResult, o *Options, work string, repush bool, logf func(string, ...any)) error {
	interp, script, err := r.HookScript(runtime.GOOS)
	if err != nil {
		return err
	}
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("hook 脚本不存在: %s", script)
	}
	args := []string{script,
		"--repo", repoRoot,
		"--work", work,
	}
	for _, p := range r.Params {
		switch p.Kind {
		case "modules":
			if len(o.Modules) > 0 {
				args = append(args, "--modules", strings.Join(o.Modules, ","))
			}
		case "middleware":
			if len(o.Middleware) > 0 {
				args = append(args, "--middleware", strings.Join(o.Middleware, ","))
			}
		default:
			v := paramValue(r, scan, o.Params, p.Name)
			if v == "" {
				continue
			}
			if v == p.Default && p.Kind != "text" {
				continue // 等于声明缺省：省略（hook 侧缺省一致）
			}
			args = append(args, kebabFlag(p.Name), v)
		}
	}
	if repush {
		args = append(args, "--repush")
	}
	if filepath.Ext(script) == ".ps1" {
		// PS 具名参数绑定不了 GNU kebab（--x 不是参数记号，被当位置参数逐个塞满 param() 后报
		// 「找不到与形参匹配的位置形式参数」）——ps1 钩子合同为 PascalCase 具名参数（-Repo/-Work/-BaseVersion）
		for i := 1; i < len(args); i++ {
			if strings.HasPrefix(args[i], "--") {
				args[i] = psParamFlag(args[i])
			}
		}
	}
	cmd := exec.Command(interp[0], append(append([]string{}, interp[1:]...), args...)...)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if exe, err := os.Executable(); err == nil {
		// 供 hook 调用通用工具（collect-kit zip-dir 等）
		cmd.Env = append(os.Environ(), "COLLECT_KIT_BIN="+exe)
	}
	logf("    $ %s", hookDisplay(interp, script, args))
	return cmd.Run()
}

// hookDisplay 展示命令行（密码等敏感值由调用方保证不进 hook 参数——凭据走 core 的 studio 客户端）
func hookDisplay(interp []string, script string, args []string) string {
	parts := []string{filepath.Base(script)}
	parts = append(parts, args[3:]...) // 跳过脚本名/repo/work（已由上下文给出）
	return strings.Join(append([]string{interp[0]}, parts...), " ")
}

func readManifest(path string) (*BuildManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hook 未产出 %s: %w", path, err)
	}
	var m BuildManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ---- 参数校验 ----

func validateModules(r *Recipe, scan *ScanResult, o *Options) error {
	if r.Scan == nil || len(o.Modules) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, m := range scan.Modules {
		known[m.Short] = true
	}
	for _, m := range o.Modules {
		m = strings.TrimSpace(m)
		if m != "" && !known[m] {
			return fmt.Errorf("未知业务模块: %s（不在 %s 扫描清单内）", m, r.Scan.ModulesDir)
		}
	}
	return nil
}

func checkRepushSupport(r *Recipe, o *Options, repush bool) error {
	if !repush {
		return nil
	}
	ca := r.Actions.Collect
	if ca == nil {
		return fmt.Errorf("recipe %s 未声明 collect 动作，无制品可重推", r.Name)
	}
	if len(ca.RepushProfiles) == 0 {
		return nil
	}
	form := o.Params["form"]
	for _, p := range ca.RepushProfiles {
		if p == form {
			return nil
		}
	}
	return fmt.Errorf("形态 %s 暂不支持重推（recipe 声明仅 %v；多镜像无统一制品指纹——请直接重跑构建）",
		orDefault(form, "(缺省)"), ca.RepushProfiles)
}

func resolveRepoRoot(r *Recipe, o *Options) (string, error) {
	if o.Repo != "" {
		return filepath.Abs(o.Repo)
	}
	return r.RepoRootAbs()
}
