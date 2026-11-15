// collect-kit v2 单测：recipe 合同加载校验、通用仓库扫描、CLI 参数名往返、
// 端到端（假 hook → 产物合同 → 假 deliver-studio：登录/去重上传/打包请求回填/套件下载）。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---- recipe 合同 ----

func writeRecipe(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "recipe.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const goodRecipe = `{
  "format": 1, "name": "demo-repo", "title": "演示", "repoRoot": "../../x",
  "scan": {"modulesDir": "biz", "modulePrefix": "app-", "requireSuffix": "-service",
    "probeDirs": [{"suffix":"-ui","label":"ui"}], "versionFile": "framework/pom.xml", "versionDefault": "9.9"},
  "middleware": [{"code":"rocketmq","label":"MQ"}],
  "params": [
    {"name":"name","kind":"text","label":"套件名","pattern":"^[a-z0-9][a-z0-9-]*$"},
    {"name":"version","kind":"text","label":"版本","default":"1.0.0"},
    {"name":"form","kind":"choice","label":"形态","default":"mono","choices":[{"value":"mono","label":"单体"},{"value":"micro","label":"微服务"}]},
    {"name":"ui","kind":"bool","label":"前端","default":"true"},
    {"name":"modules","kind":"modules","label":"模块"},
    {"name":"out","kind":"out","label":"输出","default":"./out"}
  ],
  "actions": {
    "collect": {"hook": {"sh": "hooks/collect.sh"}, "repushProfiles": ["mono"]},
    "pushImages": {"always": ["mysql:8.4"],
      "whenParam": [{"param":"form","equals":"micro","images":["nacos/nacos-server:v3.0.0"]}],
      "middleware": {"rocketmq": [{"ref":"apache/rocketmq:5.3.1"}]}
    }
  }
}`

func TestRecipeLoadAndValidate(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadRecipe(dir, filepath.Join(dir, "no-such")); err == nil {
		t.Fatal("不存在的 recipe 应报错")
	}
	writeRecipe(t, dir, goodRecipe)
	r, err := LoadRecipe(dir, dir)
	if err != nil {
		t.Fatalf("合法 recipe 应通过: %v", err)
	}
	if r.Name != "demo-repo" || len(r.Params) != 6 || r.Actions.Collect == nil {
		t.Fatalf("recipe 解析不符: %+v", r)
	}
	interp, script, err := r.HookScript("windows") // 无 ps1 → 回退 sh
	if err != nil || interp[0] != "sh" || !strings.HasSuffix(filepath.ToSlash(script), "hooks/collect.sh") {
		t.Fatalf("hook 选择不符: %v %s %v", interp, script, err)
	}
	// 校验失败族
	for name, mutate := range map[string]func(s string) string{
		"版本错":  func(s string) string { return strings.Replace(s, `"format": 1`, `"format": 2`, 1) },
		"名字错":  func(s string) string { return strings.Replace(s, `"demo-repo"`, `"Demo Repo"`, 1) },
		"kind非法": func(s string) string { return strings.Replace(s, `"kind":"bool"`, `"kind":"int"`, 1) },
		"choice空": func(s string) string { return strings.Replace(s, `"choices":[{"value":"mono","label":"单体"},{"value":"micro","label":"微服务"}]`, ``, 1) },
		"pattern非法": func(s string) string { return strings.Replace(s, `^[a-z0-9][a-z0-9-]*$`, `[unclosed`, 1) },
	} {
		d := t.TempDir()
		writeRecipe(t, d, mutate(goodRecipe))
		if _, err := LoadRecipe(d, d); err == nil {
			t.Errorf("%s: 应校验失败", name)
		}
	}
}

// 仅声明 collect（无 pushImages）的极简 recipe 必须可加载——曾对 nil PushImages 取 Middleware 直接 panic
func TestRecipeCollectOnlyNoPanic(t *testing.T) {
	minimal := `{"format":1,"name":"mini","params":[{"name":"name","kind":"text","label":"名"}],
	  "actions":{"collect":{"hook":{"sh":"hooks/collect.sh"}}}}`
	d := t.TempDir()
	writeRecipe(t, d, minimal)
	r, err := LoadRecipe(d, d)
	if err != nil {
		t.Fatalf("collect-only recipe 应通过（曾空指针 panic）: %v", err)
	}
	if r.Actions.PushImages != nil {
		t.Fatalf("pushImages 应为 nil: %+v", r.Actions.PushImages)
	}
}

// 内置根逐级向上定位：cwd 链与 exe 链共用的 findRecipesRoot——向导构建子进程 cwd=仓库根（链上无 recipes/），
// 须靠 exe 链（bin/ → collect-kit/）命中，否则兜底 exe 同级必失败（曾致 bin\recipes\ai-mse 读取失败）
func TestFindRecipesRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "app", "recipes")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// 深层子目录逐级向上命中 + start 自身一级直接命中
	deep := filepath.Join(base, "app", "sub", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findRecipesRoot(deep); got != root {
		t.Fatalf("深层向上应命中 app/recipes: got %s want %s", got, root)
	}
	if got := findRecipesRoot(filepath.Join(base, "app")); got != root {
		t.Fatalf("start 自身一级应命中: got %s want %s", got, root)
	}
	// 全链无 recipes/ 返回空串（临时目录祖先链按约定无同名目录）
	if got := findRecipesRoot(t.TempDir()); got != "" {
		t.Fatalf("无 recipes/ 链应返回空串: %s", got)
	}
}

// 内置 ai-mse recipe 必须可加载且 preset/hook 就位（与 deliver-studio 内嵌副本字节一致由构建流程保障）
func TestBuiltinAiMseRecipe(t *testing.T) {
	if _, err := os.Stat(filepath.Join("recipes", "ai-mse", "recipe.json")); err != nil {
		t.Skip("非源码目录运行（recipes 未随测试工作目录）")
	}
	r, err := LoadRecipe(builtinRoot(), "ai-mse")
	if err != nil {
		t.Fatalf("ai-mse recipe 加载失败: %v", err)
	}
	if r.Preset == "" {
		t.Fatal("ai-mse recipe 缺 preset")
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), r.Preset)); err != nil {
		t.Fatalf("preset 文件缺失: %v", err)
	}
	if _, _, err := r.HookScript(runtime.GOOS); err != nil {
		t.Fatalf("当前平台缺 hook: %v", err)
	}
	if root, err := r.RepoRootAbs(); err != nil || !strings.HasSuffix(filepath.ToSlash(root), "/ai-mse") {
		t.Fatalf("repoRoot 解析不符: %s %v", root, err)
	}
	// 模块/中间件选择流向钩子的前提：params 按名+kind 声明（wizard 按名、runner 按类把关）——
	// 缺声明时向导勾选被静默丢弃、钩子永远收不到 --modules/--middleware
	for _, want := range []struct{ name, kind string }{
		{"modules", "modules"}, {"middleware", "middleware"},
	} {
		p := r.Param(want.name)
		if p == nil || p.Kind != want.kind {
			t.Fatalf("ai-mse recipe 缺参数 %s（kind=%s）——勾选将无法传给钩子", want.name, want.kind)
		}
	}
	// 组件勾选制守门：mysql/redis 在中间件目录且备料分组就位、无 always 兜底
	// （勾什么备什么/打包什么；退回 always 硬编码会重新出现「没勾也推/也打包」）
	mwCodes := map[string]bool{}
	for _, mw := range r.Middle {
		mwCodes[mw.Code] = true
	}
	if !mwCodes["mysql"] || !mwCodes["redis"] {
		t.Fatal("ai-mse 中间件目录缺 mysql/redis（备料与打包组件勾选制的前提）")
	}
	pi := r.Actions.PushImages
	if pi == nil || len(pi.Always) > 0 {
		t.Fatal("ai-mse pushImages 应无 always 兜底（mysql/redis 走中间件勾选组）")
	}
	if len(pi.Middleware["mysql"]) == 0 || len(pi.Middleware["redis"]) == 0 {
		t.Fatal("ai-mse pushImages 缺 mysql/redis 备料分组")
	}
	// 附件链要求：三个应用均声明附件四件套（附件 vars.env 预填命中；缺声明重打包日志出告警）
	presetData, err := os.ReadFile(filepath.Join(r.Dir(), r.Preset))
	if err != nil {
		t.Fatal(err)
	}
	var preset struct {
		Apps []struct {
			Code      string `json:"code"`
			Variables []struct {
				Name string `json:"name"`
				When string `json:"when"`
			} `json:"variables"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(presetData, &preset); err != nil {
		t.Fatalf("preset 解析失败: %v", err)
	}
	if len(preset.Apps) != 3 {
		t.Fatalf("preset 应含 3 个应用: %d", len(preset.Apps))
	}
	for _, app := range preset.Apps {
		have := map[string]bool{}
		for _, v := range app.Variables {
			if v.When == "install" {
				have[v.Name] = true
			}
		}
		for _, want := range []string{"TENANT_ID", "TENANT_CODE", "TENANT_NAME", "LICENSE_PUBLIC_KEY"} {
			if !have[want] {
				t.Errorf("preset 应用 %s 缺附件安装变量 %s（附件 vars.env 预填不命中）", app.Code, want)
			}
		}
		// 组件勾选制配套：DB/Redis 端点占位符提升为应用级安装变量——未勾 mysql/redis 组件时的配置渲染兜底
		// （勾选路径组件端点后写覆盖同名变量，双源一致）
		for _, want := range []string{"DB_HOST", "DB_PORT", "DB_NAME", "PLATFORM_DB_NAME", "DB_USER", "DB_PASSWORD", "REDIS_HOST", "REDIS_PORT"} {
			if !have[want] {
				t.Errorf("preset 应用 %s 缺安装变量 %s（未勾组件时配置渲染无源）", app.Code, want)
			}
		}
	}
}

// ---- 通用仓库扫描 ----

func TestScanRepo(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root,
		"biz/app-oa/oa-service", "biz/app-oa/oa-ui", "biz/app-fs/fs-service",
		"biz/app-broken", // 无 -service 子目录 → 不入列
		"biz/not-prefixed/x-service",
		"framework")
	os.WriteFile(filepath.Join(root, "framework", "pom.xml"),
		[]byte("<project><version> 2.5.0-SNAPSHOT </version></project>"), 0o644)
	spec := &ScanSpec{ModulesDir: "biz", ModulePrefix: "app-", RequireSuffix: "-service",
		ProbeDirs:    []ProbeDir{{Suffix: "-ui", Label: "ui"}},
		VersionFile:  "framework/pom.xml",
		VersionDefault: "1.0.0"}
	scan, err := ScanRepo(root, spec)
	if err != nil {
		t.Fatal(err)
	}
	if scan.BaseVersion != "2.5.0-SNAPSHOT" {
		t.Fatalf("基座版本提取不符: %s", scan.BaseVersion)
	}
	if len(scan.Modules) != 2 {
		t.Fatalf("模块扫描应为 2 个（fs/oa）: %+v", scan.Modules)
	}
	var oa, fs *ModuleInfo
	for i := range scan.Modules {
		switch scan.Modules[i].Short {
		case "oa":
			oa = &scan.Modules[i]
		case "fs":
			fs = &scan.Modules[i]
		}
	}
	if oa == nil || fs == nil {
		t.Fatalf("模块缺失: %+v", scan.Modules)
	}
	if oa.Label != "service · ui" {
		t.Fatalf("oa 模块标注不符（应含 ui 探测）: %+v", oa)
	}
	if fs.Label != "service" {
		t.Fatalf("fs 模块标注不符（无 ui 探测）: %+v", fs)
	}
}

func TestScanRepoVersionFallback(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "framework")
	scan, err := ScanRepo(root, &ScanSpec{VersionFile: "framework/pom.xml", VersionDefault: "3.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if scan.BaseVersion != "3.0.0" {
		t.Fatalf("版本提取失败应用 VersionDefault: %s", scan.BaseVersion)
	}
}

func mkdirs(t *testing.T, base string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(base, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// ---- 参数名往返 ----

func TestFlagNameRoundTrip(t *testing.T) {
	cases := map[string]string{"name": "name", "version": "version", "baseVersion": "base-version", "ui": "ui"}
	for camel, flag := range cases {
		if got := kebabFlag(camel); got != "--"+flag {
			t.Errorf("kebabFlag(%s) = %s，期望 --%s", camel, got, flag)
		}
		if got := camelName(flag); got != camel {
			t.Errorf("camelName(%s) = %s，期望 %s", flag, got, camel)
		}
	}
}

// ps1 钩子具名参数翻译：GNU kebab 绑不进 PowerShell param()（双横线非参数记号，
// 全部落成位置参数塞爆声明后报「找不到与形参匹配的位置形式参数」），核心须转 -PascalCase 传递
func TestPsParamFlag(t *testing.T) {
	cases := map[string]string{
		"--repo":          "-Repo",
		"--work":          "-Work",
		"--name":          "-Name",
		"--base-version":  "-BaseVersion",
		"--ui":            "-Ui",
		"--modules":       "-Modules",
		"--middleware":    "-Middleware",
		"--repush":        "-Repush",
		"--tenant-code-x": "-TenantCodeX",
	}
	for kebab, want := range cases {
		if got := psParamFlag(kebab); got != want {
			t.Errorf("psParamFlag(%s) = %s，期望 %s", kebab, got, want)
		}
	}
}

// ---- 端到端：假 hook + 假 deliver-studio ----

// fakeStudio 模拟 deliver-studio 交互面：登录/预设导入/镜像入仓/制品列表与上传/打包/下载
type fakeStudio struct {
	t          *testing.T
	presetSeen bool
	images     []string
	materials  []map[string]string
	existing   []Material // 预置既有制品（去重命中用）
	pkgBody    map[string]any
	pkgCalls   int
}

func (f *fakeStudio) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": "t-1"})
	})
	mux.HandleFunc("/api/components/import", func(w http.ResponseWriter, _ *http.Request) {
		f.presetSeen = true
		json.NewEncoder(w).Encode(map[string]int{"apps": 3, "components": 8})
	})
	mux.HandleFunc("/api/images", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Studio-Token") != "t-1" {
			w.WriteHeader(401)
			return
		}
		var req struct{ Ref string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.images = append(f.images, req.Ref)
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/materials", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(f.existing)
			return
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			w.WriteHeader(400)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(400)
			return
		}
		file.Close()
		m := map[string]string{
			"fileName": filepath.Base(r.FormValue("file")),
			"appCode":  r.FormValue("appCode"),
		}
		f.materials = append(f.materials, m)
		json.NewEncoder(w).Encode(map[string]any{"id": len(f.materials) + 100, "fileName": m["fileName"]})
	})
	mux.HandleFunc("/api/packages", func(w http.ResponseWriter, r *http.Request) {
		f.pkgCalls++
		if err := json.NewDecoder(r.Body).Decode(&f.pkgBody); err != nil {
			f.t.Fatalf("打包请求解析失败: %v", err)
		}
		json.NewEncoder(w).Encode(PackageRec{ID: 9, Status: "ok"})
	})
	mux.HandleFunc("/api/packages/9", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(PackageRec{ID: 9, Status: "ok", Stage: "完成"})
	})
	mux.HandleFunc("/api/packages/9/download", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("fake-kit-zip"))
	})
	return mux
}

// e2eFixture：临时 recipe（sh hook 写 manifest）+ 假 studio + RunCollect 全链
func e2eFixture(t *testing.T) (*Recipe, *Options, *fakeStudio) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("当前环境无 sh，跳过 hook 端到端")
	}
	root := t.TempDir()
	mkdirs(t, root, "biz/app-oa/oa-service", "biz/app-oa/oa-ui", "framework")
	if err := os.WriteFile(filepath.Join(root, "framework", "pom.xml"), []byte("<version>1.2.3</version>"), 0o644); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(root, "hook-collect.sh")
	hookBody := `#!/usr/bin/env bash
set -euo pipefail
while [[ $# -gt 0 ]]; do case "$1" in --repo) REPO="$2";; --work) WORK="$2";; --name) NAME="$2";; --version) VER="$2";; *) :;; esac; shift 2; done
mkdir -p "$WORK/build/target"
printf 'fake-jar' > "$WORK/build/target/demo.jar"
printf 'fake-ui' > "$WORK/mse-ui-1.0.0.zip"
cat > "$WORK/build-manifest.json" <<EOF
{"appImages":["mse/app:1.0.0"],
 "materials":[{"path":"build/target/demo.jar","kind":"artifact","appCode":"demo-app"},
              {"path":"mse-ui-1.0.0.zip","kind":"artifact","appCode":"demo-app","role":"ui"}],
 "package":{"appCode":"demo-app","units":[{"code":"app","source":"image_ref","imageRef":"mse/app:1.0.0"},{"code":"web","source":"artifact","materialRole":"ui"}],"componentCodes":["mysql","redis"]},
 "summary":["fat-jar 就绪","UI zip 就绪"]}
EOF
`
	if err := os.WriteFile(hook, []byte(hookBody), 0o755); err != nil {
		t.Fatal(err)
	}
	recipeDir := filepath.Join(root, "recipe")
	rel, err := filepath.Rel(recipeDir, root)
	if err != nil {
		t.Fatal(err)
	}
	recipeJSON := strings.Replace(goodRecipe,
		`"format": 1, "name": "demo-repo"`, `"format": 1, "name": "demo-repo", "preset": "preset.json"`, 1)
	recipeJSON = strings.Replace(recipeJSON,
		`"repoRoot": "../../x"`, fmt.Sprintf(`"repoRoot": %q`, filepath.ToSlash(rel)), 1)
	recipeJSON = strings.Replace(recipeJSON, `"hooks/collect.sh"`,
		fmt.Sprintf("%q", filepath.ToSlash(hook)), 1)
	writeRecipe(t, recipeDir, recipeJSON)
	if err := os.WriteFile(filepath.Join(recipeDir, "preset.json"), []byte(`{"components":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRecipe(recipeDir, recipeDir)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeStudio{t: t}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	o := &Options{Action: "collect", Recipe: recipeDir, Repo: root,
		Params:     map[string]string{"name": "e2e", "version": "1.0.0", "ui": "true", "form": "mono", "out": root},
		StudioURL:  srv.URL, StudioUser: "admin", StudioPass: "pw", Package: true}
	return r, o, fake
}

func TestRunCollectEndToEnd(t *testing.T) {
	r, o, fake := e2eFixture(t)
	res, err := RunCollect(r, o, func(string, ...any) {})
	if err != nil {
		t.Fatalf("端到端失败: %v", err)
	}
	if !fake.presetSeen {
		t.Error("预设未导入")
	}
	if len(fake.images) != 1 || fake.images[0] != "mse/app:1.0.0" {
		t.Errorf("应用镜像入仓不符: %v", fake.images)
	}
	if len(fake.materials) != 2 {
		t.Fatalf("制品上传数不符: %v", fake.materials)
	}
	if fake.pkgCalls != 1 {
		t.Fatalf("打包提交应为 1 次: %d", fake.pkgCalls)
	}
	// 真 studio 的 AssembleRequest 硬校验 name 非空——hook manifest 省略时 core 必须用参数补齐
	if name, _ := fake.pkgBody["name"].(string); name != "e2e" {
		t.Errorf("打包请求 name 不符（缺省应由 core 以 --name 补齐）: %v", fake.pkgBody["name"])
	}
	if ver, _ := fake.pkgBody["version"].(string); ver != "1.0.0" {
		t.Errorf("打包请求 version 不符: %v", fake.pkgBody["version"])
	}
	units, _ := fake.pkgBody["units"].([]any)
	if len(units) != 2 {
		t.Fatalf("打包单元数不符: %v", fake.pkgBody)
	}
	web, _ := units[1].(map[string]any)
	if role, _ := web["materialRole"].(string); role != "" {
		t.Errorf("materialRole 提交前应被回填清除: %v", web)
	}
	if mid, _ := web["materialId"].(float64); mid != 102 { // 第二个上传制品 id=102
		t.Errorf("web 单元 materialId 应回填 ui 角色制品（102）: %v", web)
	}
	if res.KitZip == "" || !strings.HasSuffix(filepath.ToSlash(res.KitZip), "e2e-1.0.0.zip") {
		t.Errorf("套件 zip 路径不符: %s", res.KitZip)
	}
	data, err := os.ReadFile(res.KitZip)
	if err != nil || string(data) != "fake-kit-zip" {
		t.Errorf("套件 zip 内容不符: %q %v", string(data), err)
	}

	// 默认同版本覆盖：既有同身份记录不拦截上传——总是全量重传（覆盖由服务端同身份保留最新完成）
	fake.existing = append(fake.existing, Material{ID: 55, Kind: "artifact",
		FileName: "demo.jar", Size: int64(len("fake-jar")), AppCode: "demo-app", Version: "1.0.0"})
	fake.materials = nil
	if _, err := RunCollect(r, o, func(string, ...any) {}); err != nil {
		t.Fatalf("第二次端到端失败: %v", err)
	}
	if len(fake.materials) != 2 { // 既有同身份记录存在仍全量上传（jar + UI zip）
		t.Errorf("默认覆盖语义不符（应总是上传 2 个）: %v", fake.materials)
	}

	// --dedup-materials 省流量模式：四元组全等命中复用跳过（jar + UI zip 全命中 → 零上传），汇总打「复用制品」
	fake.existing = append(fake.existing, Material{ID: 56, Kind: "artifact",
		FileName: "mse-ui-1.0.0.zip", Size: int64(len("fake-ui")), AppCode: "demo-app", Version: "1.0.0"})
	fake.materials = nil
	o.DedupMaterials = true
	res2, err := RunCollect(r, o, func(string, ...any) {})
	o.DedupMaterials = false
	if err != nil {
		t.Fatalf("第三次端到端失败: %v", err)
	}
	if len(fake.materials) != 0 {
		t.Errorf("dedup 模式应零上传（全命中复用）: %v", fake.materials)
	}
	if !strings.Contains(strings.Join(res2.SummaryOK, "\n"), "复用制品") {
		t.Errorf("dedup 命中应汇总为「复用制品」: %v", res2.SummaryOK)
	}
}

// repush：核心按 recipe repushProfiles 拦截不支持的形态
func TestRepushProfileGuard(t *testing.T) {
	r, o, _ := e2eFixture(t)
	o.Action = "repush"
	o.Params["form"] = "micro"
	if _, err := RunCollect(r, o, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "暂不支持重推") {
		t.Fatalf("micro repush 应拦截: %v", err)
	}
}

// checkParams：未知 flag 拒绝
func TestCheckParamsUnknownFlag(t *testing.T) {
	dir := t.TempDir()
	writeRecipe(t, dir, goodRecipe)
	r, err := LoadRecipe(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	o := &Options{Params: map[string]string{"ghostParam": "x"}}
	if err := checkParams(r, o); err == nil || !strings.Contains(err.Error(), "未知参数") {
		t.Fatalf("未知参数应拒绝: %v", err)
	}
}

// ---- push-images 备料清单组装 ----

// assemblePushRefs 勾什么推什么：middleware 段只认勾选（未勾一个不推）、跨组按 target 去重、
// 未知 code 拦截防打错字静默漏推、勾选了目录项但无镜像分组的进 noImage 提示
func TestAssemblePushRefs(t *testing.T) {
	r := &Recipe{
		Name:   "demo",
		Middle: []MwDecl{{Code: "rocketmq"}, {Code: "milvus"}, {Code: "openim"}, {Code: "bare"}},
		Params: []ParamDecl{{Name: "form", Kind: "choice", Default: "mono"}},
		Actions: Actions{PushImages: &PushImages{
			Always:    []string{"mysql:8.4", "redis:7-alpine"},
			WhenParam: []CondImages{{Param: "form", Equals: "micro", Images: []string{"nacos/nacos-server:v3.0.0"}}},
			Middleware: map[string][]ImageRef{
				"rocketmq": {{Ref: "apache/rocketmq:5.3.1"}},
				"milvus":   {{Ref: "quay.io/coreos/etcd:v3.5.14"}, {Ref: "minio/minio:RELEASE.2024-05-28T17-19-04Z"}},
				"openim":   {{Ref: "minio/minio:RELEASE.2024-05-28T17-19-04Z"}, {Build: "openim/custom:v1", Context: "docker/openim"}},
			},
		}},
	}
	targets := func(refs []ImageRef) []string {
		out := make([]string, len(refs))
		for i, x := range refs {
			out[i] = x.target()
		}
		return out
	}
	has := func(list []string, want string) bool {
		for _, x := range list {
			if x == want {
				return true
			}
		}
		return false
	}

	// 未勾选任何中间件 → 只推 always（whenParam 未命中不追加）
	refs, noImage, err := assemblePushRefs(r, nil, &Options{})
	if err != nil || len(noImage) != 0 {
		t.Fatalf("空勾选应成功: %v %v", refs, err)
	}
	if got := targets(refs); len(got) != 2 || got[0] != "mysql:8.4" || got[1] != "redis:7-alpine" {
		t.Fatalf("空勾选应只推 always: %v", got)
	}

	// whenParam 命中追加（form=micro → nacos）
	refs, _, err = assemblePushRefs(r, nil, &Options{Params: map[string]string{"form": "micro"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := targets(refs); len(got) != 3 || !has(got, "nacos/nacos-server:v3.0.0") {
		t.Fatalf("whenParam 命中应追加 nacos: %v", got)
	}

	// 勾选 milvus+openim：两组并集、minio 跨组去重、build 条目按 target 计
	refs, _, err = assemblePushRefs(r, nil, &Options{Middleware: []string{"openim", "milvus"}})
	if err != nil {
		t.Fatal(err)
	}
	got := targets(refs)
	if len(got) != 5 {
		t.Fatalf("milvus+openim 去重后应 5 个（2 always + etcd/minio/openim-custom）: %v", got)
	}
	for _, want := range []string{"mysql:8.4", "redis:7-alpine", "quay.io/coreos/etcd:v3.5.14",
		"minio/minio:RELEASE.2024-05-28T17-19-04Z", "openim/custom:v1"} {
		if !has(got, want) {
			t.Errorf("缺少 %s: %v", want, got)
		}
	}

	// 勾选了目录项但无镜像分组：合法，进 noImage；extra-images 追加且与 always 去重
	refs, noImage, err = assemblePushRefs(r, nil,
		&Options{Middleware: []string{"bare"}, ExtraImages: []string{"mysql:8.4", "xx/y:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(noImage) != 1 || noImage[0] != "bare" {
		t.Fatalf("无分组中间件应进 noImage: %v", noImage)
	}
	if got := targets(refs); len(got) != 3 || !has(got, "xx/y:1") {
		t.Fatalf("extra 应追加去重（mysql 不重复）: %v", got)
	}

	// 未知 code 拦截：报错并列出可选项
	_, _, err = assemblePushRefs(r, nil, &Options{Middleware: []string{"rocketmq", "es"}})
	if err == nil || !strings.Contains(err.Error(), "未知中间件 es") ||
		!strings.Contains(err.Error(), "可选: bare/milvus/openim/rocketmq") {
		t.Fatalf("未知中间件应报错并列可选项: %v", err)
	}
}

// ---- 向导多 recipe 页面切换 ----

const selRecipeFmt = `{"format":1,"name":"%s","title":"%s","params":[{"name":"name","kind":"text","label":"名"}],
  "actions":{"collect":{"hook":{"sh":"hooks/collect.sh"}}}}`

func TestWizardRecipeSwitch(t *testing.T) {
	root := t.TempDir()
	writeRecipe(t, filepath.Join(root, "repo-a"), fmt.Sprintf(selRecipeFmt, "repo-a", "甲仓库"))
	writeRecipe(t, filepath.Join(root, "repo-b"), fmt.Sprintf(selRecipeFmt, "repo-b", "乙仓库"))

	oldRoot := builtinRoot
	builtinRoot = func() string { return root }
	defer func() { builtinRoot = oldRoot }()

	ra, sa, err := wizardLoadAndScan("", "repo-a")
	if err != nil {
		t.Fatalf("初始加载失败: %v", err)
	}
	srv := &wizardServer{recipe: ra, scan: sa,
		cards: []RecipeCard{{Name: "repo-a", Title: "甲仓库"}, {Name: "repo-b", Title: "乙仓库"}}}

	// meta 下发可切换清单 + 当前 recipe
	mr := httptest.NewRecorder()
	srv.handleMeta(mr, httptest.NewRequest(http.MethodGet, "/api/meta", nil))
	var meta struct {
		Recipes []RecipeCard   `json:"recipes"`
		Recipe  map[string]any `json:"recipe"`
	}
	if err := json.Unmarshal(mr.Body.Bytes(), &meta); err != nil || len(meta.Recipes) != 2 || meta.Recipe["name"] != "repo-a" {
		t.Fatalf("meta 不符: %v %s", err, mr.Body.String())
	}

	selectOn := func(s *wizardServer, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/select", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.handleSelect(w, req)
		return w
	}

	// 切换成功：响应 meta 已切到 repo-b，服务器当前 recipe 同步变更
	w := selectOn(srv, `{"recipe":"repo-b"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("切换应 200: %d %s", w.Code, w.Body.String())
	}
	if r, _ := srv.selected(); r.Name != "repo-b" {
		t.Fatalf("服务器应已切到 repo-b: %s", r.Name)
	}
	var sel struct {
		Recipe map[string]any `json:"recipe"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sel); err != nil || sel.Recipe["name"] != "repo-b" {
		t.Fatalf("切换响应 meta 不符: %v %s", err, w.Body.String())
	}

	// 未知名 400；运行中 409；未开启切换（单一/-recipe 锁定）400
	if w := selectOn(srv, `{"recipe":"ghost"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("未知名应 400: %d %s", w.Code, w.Body.String())
	}
	srv.run.phase = "running"
	if w := selectOn(srv, `{"recipe":"repo-a"}`); w.Code != http.StatusConflict {
		t.Fatalf("运行中应 409: %d %s", w.Code, w.Body.String())
	}
	srv.run.phase = "idle"
	single := &wizardServer{recipe: ra, scan: sa}
	if w := selectOn(single, `{"recipe":"repo-b"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("单一 recipe 应 400: %d %s", w.Code, w.Body.String())
	}
}
