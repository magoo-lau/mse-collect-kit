// 本地向导（浏览器表单 → 动态渲染 recipe 参数 → 子进程执行本程序子命令）。
// 表单与 CLI 同源：recipe params 声明即表单字段；动作三分类 collect/push-images/repush；
// 推送类动作启动前登录预检（密码错当场拦截）。页面动态化——新增 recipe 零改前端。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	wizardAddr  = "127.0.0.1:7861" // deliver-studio 默认占 7860，向导错开一位
	wizardLogCap = 512 * 1024      // 构建日志内存上限（超限截头保尾）
)

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	urlRe     = regexp.MustCompile(`^https?://[A-Za-z0-9./:_#?&=%~-]+$`)
	imageRe   = regexp.MustCompile(`^[A-Za-z0-9./:@_-]+$`)
)

// wizardForm 页面表单 JSON（params 按 recipe 声明的 name 透传）
type wizardForm struct {
	Action      string            `json:"action"` // collect（缺省）| push-images | repush
	Params      map[string]string `json:"params"`
	Modules     []string          `json:"modules"`
	Middleware  []string          `json:"middleware"`
	ExtraImages string            `json:"extraImages"`
	UploadMode  string            `json:"uploadMode"` // none/default/custom
	UploadURL   string            `json:"uploadUrl"`
	Package     bool              `json:"package"`
	StudioUser  string            `json:"studioUser"`
	StudioPass  string            `json:"studioPass"`
}

// RecipeCard 可切换 recipe 清单项（向导顶部下拉；多内置 recipe 时出现）
type RecipeCard struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

type wizardServer struct {
	recipe *Recipe
	scan   *ScanResult
	cards  []RecipeCard // 可切换清单（len>1 才展示；-recipe 指定时为空 = 锁定单一）
	repoOv string       // -repo 覆盖（对全部可切换 recipe 生效）
	dryRun bool
	run    wizardRunState
	selMu  sync.Mutex // recipe/scan 切换与读取互斥（select 换装与 start/preview/meta 并发）
}

// selected 取当前 recipe 与扫描快照（切换与执行互斥，避免半换装状态）
func (s *wizardServer) selected() (*Recipe, *ScanResult) {
	s.selMu.Lock()
	defer s.selMu.Unlock()
	return s.recipe, s.scan
}

// selectedScan 仅取扫描快照（子进程 cwd 用）
func (s *wizardServer) selectedScan() *ScanResult {
	_, scan := s.selected()
	return scan
}

// wizardRunState 单任务串行（running 拒绝再次 start）；锁纪律——appendLog/finish 各自独立持锁、不嵌套互调
type wizardRunState struct {
	mu       sync.Mutex
	phase    string // idle | running | done | failed
	exitCode int
	log      []byte
	zip      string
	cmdLine  string
}

func (r *wizardRunState) appendLog(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, p...)
	if over := len(r.log) - wizardLogCap; over > 0 {
		r.log = append([]byte("……（前面日志已截断）\n"), r.log[over:]...)
	}
}

func (r *wizardRunState) finish(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if code == 0 {
		r.phase = "done"
	} else {
		r.phase = "failed"
	}
	r.exitCode = code
}

// runWizard 入口（main 无参调用；flags 经 os.Args 透传）
func runWizard() {
	fs := flag.NewFlagSet("wizard", flag.ExitOnError)
	addr := fs.String("addr", wizardAddr, "监听地址")
	recipeFlag := fs.String("recipe", "", "recipe（内置名或目录路径；缺省=唯一内置 recipe）")
	repo := fs.String("repo", "", "仓库根目录（缺省按 recipe.repoRoot 推导）")
	open := fs.Bool("open", true, "自动打开浏览器")
	dryRun := fs.Bool("dry-run", false, "仅校验并打印组装命令，不执行")
	_ = fs.Parse(os.Args[1:])

	recipeName := *recipeFlag
	var cards []RecipeCard
	if recipeName == "" {
		builtins := builtinRecipes()
		switch {
		case len(builtins) == 0:
			log.Fatalf("内置 recipe 有 0 个——请用 -recipe <目录路径> 指定（或将 recipe 放入 exe 同级 recipes/）")
		case len(builtins) > 1: // 多内置：全部装车，页面下拉直接切换（-recipe 可预选并锁定单一）
			for _, b := range builtins {
				br, err := LoadRecipe(builtinRoot(), b)
				if err != nil {
					log.Printf("内置 recipe %s 加载失败（页面切换清单跳过）: %v", b, err)
					continue
				}
				cards = append(cards, RecipeCard{Name: br.Name, Title: br.Title})
			}
			if len(cards) == 0 {
				log.Fatalf("内置 recipe 全部加载失败")
			}
			recipeName = cards[0].Name
		default:
			recipeName = builtins[0]
		}
	}
	r, scan, err := wizardLoadAndScan(*repo, recipeName)
	if err != nil {
		log.Fatalf("加载 recipe 失败: %v", err)
	}

	srv := &wizardServer{recipe: r, scan: scan, cards: cards, repoOv: *repo, dryRun: *dryRun}
	http.HandleFunc("/", srv.handlePage)
	http.HandleFunc("/api/meta", srv.handleMeta)
	http.HandleFunc("/api/select", srv.handleSelect)
	http.HandleFunc("/api/preview", srv.handlePreview)
	http.HandleFunc("/api/start", srv.handleStart)
	http.HandleFunc("/api/status", srv.handleStatus)
	http.HandleFunc("/api/download", srv.handleDownload)

	url := "http://" + *addr
	note := ""
	if len(cards) > 1 {
		note = fmt.Sprintf("，共 %d 个可切换（页面顶部下拉）", len(cards))
	}
	log.Printf("collect-kit 向导已启动：%s（recipe=%s%s）", url, r.Name, note)
	log.Printf("  仓库 %s（%d 个业务模块，基座版本 %s）", scan.RepoRoot, len(scan.Modules), scan.BaseVersion)
	if *open {
		go openWizardBrowser(url)
	}
	log.Fatal(http.ListenAndServe(*addr, nil))
}

// wizardLoadAndScan 加载 recipe 并扫描（扫描失败降级空清单——表单仍可继续）
func wizardLoadAndScan(repoOverride, name string) (*Recipe, *ScanResult, error) {
	r, err := LoadRecipe(builtinRoot(), name)
	if err != nil {
		return nil, nil, err
	}
	repoRoot := repoOverride
	if repoRoot == "" {
		if repoRoot, err = r.RepoRootAbs(); err != nil {
			return nil, nil, err
		}
	}
	scan, serr := ScanRepo(repoRoot, r.Scan)
	if serr != nil {
		log.Printf("仓库扫描警告: %v（表单模块清单为空，可继续）", serr)
		scan = &ScanResult{RepoRoot: repoRoot}
	}
	return r, scan, nil
}

// handleMeta 页面动态渲染数据：recipe 声明 + 扫描结果 + 可切换清单
func (s *wizardServer) handleMeta(w http.ResponseWriter, _ *http.Request) {
	s.writeMeta(w)
}

func (s *wizardServer) writeMeta(w http.ResponseWriter) {
	recipe, scan := s.selected()
	writeWizardJSON(w, http.StatusOK, map[string]any{
		"recipes": s.cards, // 多内置时装车清单（页面据此渲染下拉；空/单项即隐藏）
		"recipe": map[string]any{
			"name": recipe.Name, "title": recipe.Title, "description": recipe.Desc,
			"params": recipe.Params, "middleware": recipe.Middle,
			"actions": map[string]bool{
				"collect":    recipe.Actions.Collect != nil,
				"pushImages": recipe.Actions.PushImages != nil,
				"repush":     recipe.Actions.Collect != nil,
			},
			"repushProfiles": recipe.Actions.Collect.RepushProfiles,
		},
		"scan": scan,
	})
}

// handleSelect 多内置 recipe 时页面切换（运行中拒绝；成功后重置任务面板状态并回传完整 meta）
func (s *wizardServer) handleSelect(w http.ResponseWriter, r *http.Request) {
	if len(s.cards) < 2 {
		writeWizardErr(w, http.StatusBadRequest, "当前向导未开启 recipe 切换（单一 recipe 或已用 -recipe 锁定）")
		return
	}
	defer r.Body.Close()
	var req struct {
		Recipe string `json:"recipe"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeWizardErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	req.Recipe = strings.TrimSpace(req.Recipe)
	known := false
	for _, c := range s.cards {
		if c.Name == req.Recipe {
			known = true
			break
		}
	}
	if !known {
		writeWizardErr(w, http.StatusBadRequest, "未知 recipe: "+req.Recipe)
		return
	}
	s.run.mu.Lock()
	running := s.run.phase == "running"
	s.run.mu.Unlock()
	if running {
		writeWizardErr(w, http.StatusConflict, "已有任务在进行中（等待完成后再切换）")
		return
	}
	nr, ns, err := wizardLoadAndScan(s.repoOv, req.Recipe)
	if err != nil {
		writeWizardErr(w, http.StatusBadRequest, "切换失败: "+err.Error())
		return
	}
	s.selMu.Lock()
	s.recipe, s.scan = nr, ns
	s.selMu.Unlock()
	// 切换即换上下文：旧任务的日志/下载位一并清空
	s.run.mu.Lock()
	s.run.phase, s.run.exitCode, s.run.log, s.run.zip, s.run.cmdLine = "idle", 0, nil, "", ""
	s.run.mu.Unlock()
	log.Printf("向导切换 recipe=%s（仓库 %s，%d 个业务模块，基座版本 %s）", nr.Name, ns.RepoRoot, len(ns.Modules), ns.BaseVersion)
	s.writeMeta(w)
}

// handlePage 前端静态资源（内嵌 ui/dist；未知路径回退 index.html；未构建时提示页兜底）。
func (s *wizardServer) handlePage(w http.ResponseWriter, r *http.Request) {
	dist, ok := wizardUIDist()
	if !ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(uiPlaceholder))
		return
	}
	fileServer := http.FileServer(http.FS(dist))
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path != "" {
		if _, err := fs.Stat(dist, filepath.ToSlash(path)); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(index)
}

func (s *wizardServer) handlePreview(w http.ResponseWriter, r *http.Request) {
	req, err := readWizardForm(r)
	if err != nil {
		writeWizardErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args, zip, err := s.assemble(req)
	if err != nil {
		writeWizardErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeWizardJSON(w, http.StatusOK, map[string]any{"ok": true, "command": s.displayCmd(args, req), "zipPath": zip})
}

func (s *wizardServer) handleStart(w http.ResponseWriter, r *http.Request) {
	req, err := readWizardForm(r)
	if err != nil {
		writeWizardErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args, zip, err := s.assemble(req)
	if err != nil {
		writeWizardErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 推送类动作启动前登录预检（密码错/工作台未启动当场拦截）
	if upload, uerr := wizardUploadURL(req); uerr == nil && upload != "" {
		if perr := PrecheckLogin(upload, orDefault(strings.TrimSpace(req.StudioUser), "admin"), strings.TrimSpace(req.StudioPass)); perr != nil {
			writeWizardErr(w, http.StatusBadRequest, perr.Error())
			return
		}
	}
	s.run.mu.Lock()
	if s.run.phase == "running" {
		s.run.mu.Unlock()
		writeWizardErr(w, http.StatusConflict, "已有任务在进行中（刷新页面可继续查看日志）")
		return
	}
	s.run.phase = "running"
	s.run.exitCode = 0
	s.run.log = nil
	s.run.zip = zip
	s.run.cmdLine = s.displayCmd(args, req)
	s.run.mu.Unlock()

	if s.dryRun {
		s.run.appendLog([]byte("[dry-run] 仅校验与组装，不执行：\n" + s.run.cmdLine + "\n"))
		s.run.finish(0)
		writeWizardJSON(w, http.StatusOK, map[string]any{"ok": true, "command": s.run.cmdLine})
		return
	}
	exe, err := os.Executable()
	if err != nil {
		writeWizardErr(w, http.StatusInternalServerError, "定位自身路径失败: "+err.Error())
		return
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = s.selectedScan().RepoRoot
	lw := wizardLogWriter{st: &s.run}
	cmd.Stdout, cmd.Stderr = lw, lw
	if err := cmd.Start(); err != nil {
		s.run.appendLog([]byte("启动进程失败：" + err.Error() + "\n"))
		s.run.finish(-1)
		writeWizardErr(w, http.StatusInternalServerError, "启动进程失败："+err.Error())
		return
	}
	writeWizardJSON(w, http.StatusOK, map[string]any{"ok": true, "command": s.run.cmdLine})
	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			code = -1
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
			s.run.appendLog([]byte("\n进程异常退出：" + err.Error() + "\n"))
		}
		s.run.finish(code)
	}()
}

func (s *wizardServer) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.run.mu.Lock()
	phase, exitCode, zip, cmdLine, lg := s.run.phase, s.run.exitCode, s.run.zip, s.run.cmdLine, string(s.run.log)
	s.run.mu.Unlock()
	writeWizardJSON(w, http.StatusOK, map[string]any{
		"state": phase, "exitCode": exitCode, "zipPath": zip, "command": cmdLine,
		"log": lg, "zipExists": zip != "" && wizardFileExists(zip),
	})
}

func (s *wizardServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	s.run.mu.Lock()
	phase, zip := s.run.phase, s.run.zip
	s.run.mu.Unlock()
	if zip == "" || !wizardFileExists(zip) {
		http.Error(w, "套件包不存在（未勾选一键打包、尚未完成或文件已被移动）", http.StatusNotFound)
		return
	}
	if phase != "done" {
		http.Error(w, "构建尚未完成", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(zip)+`"`)
	http.ServeFile(w, r, zip)
}

// assemble 表单校验 + 组装本程序子命令参数（与 CLI 完全同源的字面量）
func (s *wizardServer) assemble(req *wizardForm) ([]string, string, error) {
	recipe, scan := s.selected()
	action := orDefault(req.Action, "collect")
	if action != "collect" && action != "push-images" && action != "repush" {
		return nil, "", fmt.Errorf("未知动作: %s", action)
	}
	o := &Options{Action: action, Params: map[string]string{}, Modules: req.Modules, Middleware: req.Middleware,
		ExtraImages: splitCSV(req.ExtraImages)}
	for k, v := range req.Params {
		o.Params[k] = v
	}
	// 通用校验：文本型 name/version 形态、modules/middleware 成员、choice 取值
	if action != "push-images" {
		if p := recipe.Param("name"); p != nil {
			if !nameRe.MatchString(strings.TrimSpace(o.Params["name"])) {
				return nil, "", fmt.Errorf("套件名 %q 不合法（仅小写字母/数字/短横线，如 tenant-a）", o.Params["name"])
			}
		}
		if v := strings.TrimSpace(o.Params["version"]); v != "" && !versionRe.MatchString(v) {
			return nil, "", fmt.Errorf("版本号 %q 不合法（字母/数字/点/短横线）", v)
		}
	}
	if err := validateModules(recipe, scan, o); err != nil {
		return nil, "", err
	}
	mwSet := map[string]bool{}
	for _, m := range recipe.Middle {
		mwSet[m.Code] = true
	}
	for _, m := range req.Middleware {
		if !mwSet[strings.TrimSpace(m)] {
			return nil, "", fmt.Errorf("未知中间件: %s", m)
		}
	}
	for _, p := range recipe.Params {
		if p.Kind != "choice" {
			continue
		}
		v := o.Params[p.Name]
		if v == "" {
			v = p.Default
		}
		if v == "" {
			continue
		}
		found := false
		for _, c := range p.Choices {
			if c.Value == v {
				found = true
				break
			}
		}
		if !found {
			return nil, "", fmt.Errorf("参数 %s 取值 %q 不在可选项内", p.Name, v)
		}
	}
	for _, e := range o.ExtraImages {
		if !imageRe.MatchString(e) {
			return nil, "", fmt.Errorf("附加镜像引用不合法: %q", e)
		}
	}
	upload, uerr := wizardUploadURL(req)
	if uerr != nil {
		return nil, "", uerr
	}
	studioUser := orDefault(strings.TrimSpace(req.StudioUser), "admin")
	studioPass := strings.TrimSpace(req.StudioPass)
	if action != "collect" && upload == "" {
		return nil, "", fmt.Errorf("本动作需要在上方选择工作台地址（默认 127.0.0.1:7860 或自定义）")
	}
	if upload != "" && studioPass == "" {
		return nil, "", fmt.Errorf("推送工作台需要登录密码（见工作台启动控制台）")
	}

	args := []string{action, "--recipe", recipe.Ref()}
	if recipe.Param("modules") != nil && len(req.Modules) > 0 {
		args = append(args, "--modules", strings.Join(req.Modules, ","))
	}
	if recipe.Param("middleware") != nil && len(req.Middleware) > 0 {
		args = append(args, "--middleware", strings.Join(req.Middleware, ","))
	}
	if action == "push-images" && len(o.ExtraImages) > 0 {
		args = append(args, "--extra-images", strings.Join(o.ExtraImages, ","))
	}
	for _, p := range recipe.Params {
		v := strings.TrimSpace(o.Params[p.Name])
		if v == "" || v == p.Default {
			continue
		}
		if p.Kind == "bool" && v == "false" {
			continue
		}
		args = append(args, kebabFlag(p.Name), v)
	}
	if upload != "" {
		args = append(args, "--studio", upload, "--studio-user", studioUser, "--studio-pass", studioPass)
		if req.Package {
			args = append(args, "--package")
		}
	}
	zip := ""
	if req.Package && action != "push-images" {
		zip = filepath.Join(filepath.Dir(workDir(recipe, scan, o.Params)),
			o.Params["name"]+"-"+orDefault(o.Params["version"], "1.0.0")+".zip")
	}
	return args, zip, nil
}

// displayCmd 展示命令行（密码打码）
func (s *wizardServer) displayCmd(args []string, req *wizardForm) string {
	parts := []string{"collect-kit"}
	for i, a := range args {
		if a == "--studio-pass" && i+1 < len(args) {
			parts = append(parts, a, "******")
			continue
		}
		if i > 0 && args[i-1] == "--studio-pass" {
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// Ref recipe 引用（内置名或目录路径——按加载方式回传）
func (r *Recipe) Ref() string {
	if r.loadedDir == filepath.Join(builtinRoot(), r.Name) {
		return r.Name
	}
	return r.loadedDir
}

// wizardUploadURL 表单上传三选一 → 工作台地址
func wizardUploadURL(req *wizardForm) (string, error) {
	switch strings.TrimSpace(req.UploadMode) {
	case "", "none":
		return "", nil
	case "default":
		return "http://127.0.0.1:7860", nil
	case "custom":
		u := strings.TrimSpace(req.UploadURL)
		if !urlRe.MatchString(u) {
			return "", fmt.Errorf("自定义上传地址不合法: %q（需 http(s)://…）", u)
		}
		return u, nil
	default:
		return "", fmt.Errorf("未知上传选项: %s", req.UploadMode)
	}
}

type wizardLogWriter struct{ st *wizardRunState }

func (lw wizardLogWriter) Write(p []byte) (int, error) { lw.st.appendLog(p); return len(p), nil }

func readWizardForm(r *http.Request) (*wizardForm, error) {
	defer r.Body.Close()
	var req wizardForm
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, fmt.Errorf("请求体不是合法 JSON: %v", err)
	}
	return &req, nil
}

func writeWizardJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeWizardErr(w http.ResponseWriter, code int, msg string) {
	writeWizardJSON(w, code, map[string]any{"ok": false, "error": msg})
}

func openWizardBrowser(u string) {
	time.Sleep(300 * time.Millisecond)
	var c *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		c = exec.Command("cmd", "/c", "start", "", u)
	case "darwin":
		c = exec.Command("open", u)
	default:
		c = exec.Command("xdg-open", u)
	}
	_ = c.Start()
}

func wizardFileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
