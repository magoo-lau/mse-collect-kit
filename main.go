// collect-kit 入口：无参 = 本地向导（浏览器动态表单）；子命令：
//
//	collect      构建（hook → 产物合同 → 推送 → 可选一键打包）
//	repush       重推既有制品（hook --repush 重写 manifest，免重建）
//	push-images  第三方镜像备料（纯 recipe 声明，无 hook）
//	upload-preset 仅推送预设
//	zip-dir      通用工具：目录 → zip（hook 脚本可用，如 UI dist 打包）
//	list-recipes 列内置 recipe
//
// 参数与 recipe 声明同源：recipe.json params 的 name（camelCase）即 CLI --kebab-case flag，
// kind=modules/middleware 走 --modules/--middleware 逗号清单。
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	args := os.Args[1:]
	// 无参或 flag 形参（-addr/-recipe/-repo/-open/-dry-run）= 向导；子命令 = 动作/工具
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		runWizard()
		return
	}
	switch args[0] {
	case "collect", "repush", "push-images", "upload-preset":
		if err := runAction(args[0], args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "构建失败 ✘:", err)
			os.Exit(1)
		}
	case "zip-dir":
		if len(args) != 3 {
			fmt.Fprintln(os.Stderr, "用法: collect-kit zip-dir <src-dir> <dest.zip>")
			os.Exit(2)
		}
		if err := zipDirCmd(args[1], args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "zip-dir 失败:", err)
			os.Exit(1)
		}
	case "list-recipes":
		for _, r := range builtinRecipes() {
			fmt.Println(r)
		}
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s（collect/repush/push-images/upload-preset/zip-dir/list-recipes；无参=向导）\n", args[0])
		os.Exit(2)
	}
}

// builtinRoot 内置 recipe 根定位：cwd 起逐级向上找 recipes/（仓库内任意子目录可运行），
// 再从 exe 所在目录逐级向上找（cwd 跳出工具树时兜底——向导构建子进程 cwd=仓库根、
// 从任意目录直接调 bin/ 下二进制等场景），最后 exe 同级（分发形态 = 二进制 + recipes/ 目录）
var builtinRoot = func() string {
	if dir, err := os.Getwd(); err == nil {
		if root := findRecipesRoot(dir); root != "" {
			return root
		}
	}
	if exe, err := os.Executable(); err == nil {
		if root := findRecipesRoot(filepath.Dir(exe)); root != "" {
			return root
		}
	}
	return "recipes"
}

// findRecipesRoot 从 start 起逐级向上找 recipes/ 目录；全链无则返回空串（start 自身也算一级——分发形态 exe 与 recipes/ 同级）
func findRecipesRoot(start string) string {
	for d := start; ; d = filepath.Dir(d) {
		if dirExists(filepath.Join(d, "recipes")) {
			return filepath.Join(d, "recipes")
		}
		if parent := filepath.Dir(d); parent == d {
			return ""
		}
	}
}

func builtinRecipes() []string {
	entries, err := os.ReadDir(builtinRoot())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && fileExists(filepath.Join(builtinRoot(), e.Name(), "recipe.json")) {
			out = append(out, e.Name())
		}
	}
	return out
}

func runAction(action string, args []string) error {
	o := &Options{Action: action, Params: map[string]string{}}
	var modules, middleware, extras []string

	// 拆固定 flag；其余按 --kebab=value / --kebab value 收集
	i := 0
	next := func() string {
		i++
		if i >= len(args) {
			return ""
		}
		return args[i]
	}
	for i = 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			return fmt.Errorf("无法识别的参数: %s", a)
		}
		key, val := a[2:], ""
		hasVal := false
		if idx := strings.Index(key, "="); idx >= 0 {
			key, val, hasVal = key[:idx], key[idx+1:], true
		}
		takeVal := func() string {
			if hasVal {
				return val
			}
			return next()
		}
		switch key {
		case "recipe":
			o.Recipe = takeVal()
		case "repo":
			o.Repo = takeVal()
		case "studio":
			o.StudioURL = takeVal()
		case "studio-user":
			o.StudioUser = takeVal()
		case "studio-pass":
			o.StudioPass = takeVal()
		case "package":
			o.Package = !hasVal || val == "true"
		case "dedup-materials":
			o.DedupMaterials = !hasVal || val == "true"
		case "modules":
			modules = splitCSV(takeVal())
		case "middleware":
			middleware = splitCSV(takeVal())
		case "extra-images":
			extras = splitCSV(takeVal())
		default:
			if hasVal {
				o.Params[camelName(key)] = val
			} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				o.Params[camelName(key)] = next()
			} else {
				o.Params[camelName(key)] = "true" // bool flag
			}
		}
	}
	o.Modules = modules
	o.Middleware = middleware
	o.ExtraImages = extras

	if o.Recipe == "" {
		builtins := builtinRecipes()
		if len(builtins) == 1 {
			o.Recipe = builtins[0]
		} else {
			return fmt.Errorf("缺少 --recipe（内置: %s；或 --recipe <recipe 目录路径>）", strings.Join(builtins, "/"))
		}
	}
	r, err := LoadRecipe(builtinRoot(), o.Recipe)
	if err != nil {
		return err
	}
	if err := checkParams(r, o); err != nil {
		return err
	}
	logf := func(format string, args ...any) { fmt.Printf(format+"\n", args...) }

	// 推送类动作：凭据/地址前置校验（密码错当场拦截，不白跑构建）
	if action == "collect" && o.Package && o.StudioURL == "" {
		o.StudioURL = "http://127.0.0.1:7860" // 与旧脚本一致：--package 缺省工作台地址
	}
	if action != "collect" && o.StudioURL == "" {
		return fmt.Errorf("本动作需要工作台地址（--studio <url>）")
	}
	if o.StudioURL != "" && strings.TrimSpace(o.StudioPass) == "" {
		return fmt.Errorf("推送工作台需要登录密码（--studio-pass；见工作台启动控制台）")
	}
	if o.StudioURL != "" {
		if err := PrecheckLogin(o.StudioURL, orDefault(o.StudioUser, "admin"), o.StudioPass); err != nil {
			return err
		}
	}

	var res *Result
	switch action {
	case "collect", "repush":
		res, err = RunCollect(r, o, logf)
	case "push-images":
		res, err = RunPushImages(r, o, logf)
	case "upload-preset":
		err = RunUploadPreset(r, o, logf)
	}
	if err != nil {
		return err
	}
	fmt.Println("============================================================")
	if res != nil {
		fmt.Printf("完成 ✔（recipe=%s action=%s）\n", r.Name, action)
		if res.KitZip != "" {
			fmt.Printf("运行时套件: %s —— deliver-studio「交付附件」叠加后即为最终交付包（出交付包）\n", res.KitZip)
		}
		fmt.Println("———— 结果汇总 ————")
		for _, s := range res.SummaryOK {
			fmt.Printf("  [OK]   %s\n", s)
		}
		if len(res.SummaryWarn) > 0 {
			for _, s := range res.SummaryWarn {
				fmt.Printf("  [WARN] %s\n", s)
			}
		} else {
			fmt.Println("  （无异常/跳过项）")
		}
	}
	fmt.Println("============================================================")
	return nil
}

// checkParams recipe 声明校验：未知 flag 报错（防拼错）、choice/pattern/required 校验
func checkParams(r *Recipe, o *Options) error {
	known := map[string]*ParamDecl{}
	for i := range r.Params {
		known[r.Params[i].Name] = &r.Params[i]
	}
	for name := range o.Params {
		if _, ok := known[name]; !ok {
			return fmt.Errorf("未知参数 --%s（recipe %s 未声明；已声明: %s）", kebabOf(name), r.Name, paramNames(r))
		}
	}
	for _, p := range r.Params {
		v, ok := o.Params[p.Name]
		if !ok {
			v = p.Default
		}
		if p.Name == "name" && strings.TrimSpace(v) == "" && (o.Action == "collect" || o.Action == "repush") {
			return fmt.Errorf("缺少 --name（%s）", p.Label)
		}
		if !ok || v == "" {
			continue
		}
		switch p.Kind {
		case "choice":
			found := false
			for _, c := range p.Choices {
				if c.Value == v {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("参数 %s 的取值 %q 不在可选项内", p.Name, v)
			}
		case "text":
			if p.Pattern != "" {
				re, err := regexp.Compile(p.Pattern)
				if err == nil && !re.MatchString(v) {
					return fmt.Errorf("参数 %s 的取值 %q 不合法（%s）", p.Name, v, p.Label)
				}
			}
		}
	}
	return nil
}

func paramNames(r *Recipe) string {
	var flags []string
	for _, p := range r.Params {
		flags = append(flags, kebabFlag(p.Name))
	}
	return strings.Join(flags, " ")
}

// ---- zip-dir 通用工具（hook 可用；deflate，目录全量递归） ----

func zipDirCmd(src, dest string) error {
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("源目录不存在: %s", src)
	}
	_ = os.Remove(dest)
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	defer zw.Close()
	count := 0
	err = filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(w, f); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("%s（%d 个条目）\n", dest, count)
	return nil
}

// ---- 小工具 ----

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// camelName kebab flag 段 → camelCase 参数名（base-version → baseVersion）
func camelName(flag string) string {
	parts := strings.Split(flag, "-")
	out := parts[0]
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		out += strings.ToUpper(p[:1]) + p[1:]
	}
	return out
}

// kebabOf camelCase → kebab（checkParams 错误信息用）
func kebabOf(name string) string {
	out := camelRe.ReplaceAllString(name, "${1}-${2}")
	return strings.ToLower(out)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
