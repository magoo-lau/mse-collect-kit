// deliver-studio REST 客户端：登录换 token、预设导入、镜像入仓（{ref} 优先 / 本地 save 兜底）、
// 制品去重上传（同名同大小同 appCode 同版本复用 materialId）、打包提交/轮询/下载。
// 与旧脚本行为同源（collect-kit 是这套交互的单一真源），Go 实现免 node/curl 依赖。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// StudioClient 工作台客户端（token 自动携带）
type StudioClient struct {
	Base  string
	User  string
	Pass  string
	token string
	http  *http.Client
}

func NewStudioClient(base, user, pass string) *StudioClient {
	return &StudioClient{Base: strings.TrimRight(base, "/"), User: user, Pass: pass,
		http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *StudioClient) client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// Login 登录换 token（「连不上」与「密码错」分开报，指向性同旧脚本）
func (c *StudioClient) Login() error {
	body, _ := json.Marshal(map[string]string{"user": c.User, "password": c.Pass})
	resp, err := c.http.Post(c.Base+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("工作台无法连接（%s 未启动/地址不对？）: %w", c.Base, err)
	}
	defer resp.Body.Close()
	var r struct {
		Token string `json:"token"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r)
	if resp.StatusCode != http.StatusOK || r.Token == "" {
		msg := orDefault(r.Error, "账号/密码不正确（密码见工作台启动控制台，或 -reset-pass 重置）")
		return fmt.Errorf("工作台登录失败: %s", msg)
	}
	c.token = r.Token
	return nil
}

// PrecheckLogin 启动前预检（密码错/未启动当场拦截，免整轮构建后才发现；token 即弃）
func PrecheckLogin(base, user, pass string) error {
	if pass == "" {
		return nil
	}
	return NewStudioClient(base, user, pass).Login()
}

func (c *StudioClient) do(method, path string, contentType string, body io.Reader, out any, timeout time.Duration) error {
	req, err := http.NewRequest(method, c.Base+path, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.token != "" {
		req.Header.Set("X-Studio-Token", c.token)
	}
	resp, err := c.client(timeout).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, orDefault(e.Error, string(data)))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("响应解析失败: %w（%s）", err, truncate(string(data), 200))
		}
	}
	return nil
}

// ImportPreset 预设导入（幂等 upsert）
func (c *StudioClient) ImportPreset(presetPath string) error {
	data, err := os.ReadFile(presetPath)
	if err != nil {
		return err
	}
	return c.do(http.MethodPost, "/api/components/import", "application/json",
		bytes.NewReader(data), nil, time.Minute)
}

// PushImage 镜像入仓：POST {ref}（工作台侧 docker save）优先；失败回退本地 save + multipart 上传
func (c *StudioClient) PushImage(ref string, logf func(string, ...any)) error {
	body, _ := json.Marshal(map[string]string{"ref": ref})
	if err := c.do(http.MethodPost, "/api/images", "application/json",
		bytes.NewReader(body), nil, 30*time.Minute); err == nil {
		return nil
	}
	// 异机/工作台侧无 docker：本地 save 后上传
	tmp, err := os.MkdirTemp("", "collect-kit-img")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	tar := filepath.Join(tmp, strings.NewReplacer("/", "_", ":", "_").Replace(ref)+".tar")
	if out, err := exec.Command("docker", "save", ref, "-o", tar).CombinedOutput(); err != nil {
		return fmt.Errorf("本地 docker save %s 失败: %v: %s", ref, err, truncate(string(out), 200))
	}
	f, err := os.Open(tar)
	if err != nil {
		return err
	}
	defer f.Close()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentType := mw.FormDataContentType()
	go func() {
		defer f.Close()
		_ = pw.CloseWithError(func() error {
			mw.WriteField("ref", ref)
			w, err := mw.CreateFormFile("file", filepath.Base(tar))
			if err != nil {
				return err
			}
			if _, err := io.Copy(w, f); err != nil {
				return err
			}
			return pw.CloseWithError(mw.Close())
		}())
	}()
	logf("    本地 save 回退上传: %s", ref)
	return c.do(http.MethodPost, "/api/images", contentType, pr, nil, time.Hour)
}

// Material 工作台制品记录（去重查询用）
type Material struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	FileName string `json:"fileName"`
	Size     int64  `json:"size"`
	AppCode  string `json:"appCode"`
	Version  string `json:"version"`
}

// FindMaterialID 同身份去重（文件名+大小+appCode+version 命中复用 materialId，跳过上传免流量）
func (c *StudioClient) FindMaterialID(fileName string, size int64, appCode, version string) (int64, error) {
	var list []Material
	if err := c.do(http.MethodGet, "/api/materials?kind=artifact", "", nil, &list, time.Minute); err != nil {
		return 0, err
	}
	for _, m := range list {
		if m.FileName == fileName && m.Size == size && m.AppCode == appCode && m.Version == version {
			return m.ID, nil
		}
	}
	return 0, nil
}

// UploadMaterial multipart 上传制品 → materialId。缺省（dedup=false）总是上传：deliver-studio 服务端
// 同身份（类型+文件名+应用+版本）保留最新一条——不同版本共存、同版本重推覆盖为最新并刷新上传时间；
// dedup=true 省流量模式：四元组（文件名+大小+appCode+version）全等命中复用跳过上传
func (c *StudioClient) UploadMaterial(path, kind, appCode, version, remark string, dedup bool, logf func(string, ...any)) (int64, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false, err
	}
	if dedup {
		if id, err := c.FindMaterialID(filepath.Base(path), info.Size(), appCode, version); err == nil && id > 0 {
			logf("    复用已有制品 #%d（同名同大小，跳过上传）: %s", id, filepath.Base(path))
			return id, true, nil
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentType := mw.FormDataContentType()
	go func() {
		defer f.Close()
		_ = pw.CloseWithError(func() error {
			mw.WriteField("kind", kind)
			mw.WriteField("appCode", appCode)
			mw.WriteField("version", version)
			mw.WriteField("remark", remark)
			w, err := mw.CreateFormFile("file", filepath.Base(path))
			if err != nil {
				return err
			}
			if _, err := io.Copy(w, f); err != nil {
				return err
			}
			return pw.CloseWithError(mw.Close())
		}())
	}()
	logf("    上传 %s: %s", kind, filepath.Base(path))
	var resp struct {
		ID int64 `json:"id"`
	}
	if err := c.do(http.MethodPost, "/api/materials", contentType, pr, &resp, 30*time.Minute); err != nil {
		return 0, false, err
	}
	return resp.ID, false, nil
}

// PackageRec 工作台打包记录（轮询用）
type PackageRec struct {
	ID      int64  `json:"id"`
	Status  string `json:"status"`
	Stage   string `json:"stage"`
	Log     string `json:"log"`
	ZipPath string `json:"zipPath"`
}

// StartPackage 提交打包任务（请求体=AssembleRequest；units 里 materialRole 已换 materialId）
func (c *StudioClient) StartPackage(plan *PackagePlan) (int64, error) {
	body, err := json.Marshal(plan)
	if err != nil {
		return 0, err
	}
	var resp PackageRec
	if err := c.do(http.MethodPost, "/api/packages", "application/json",
		bytes.NewReader(body), &resp, time.Minute); err != nil {
		return 0, err
	}
	return resp.ID, nil
}

// WaitPackage 轮询打包任务（3s 间隔，默认上限 30 分钟）；logf 输出阶段变化
func (c *StudioClient) WaitPackage(id int64, logf func(string, ...any)) error {
	lastStage := ""
	for i := 0; i < 600; i++ {
		var rec PackageRec
		if err := c.do(http.MethodGet, fmt.Sprintf("/api/packages/%d", id), "", nil, &rec, 30*time.Second); err != nil {
			logf("    轮询失败（重试）: %v", err)
		} else {
			if rec.Stage != lastStage {
				logf("    打包 #%d %s（%s）", id, rec.Status, rec.Stage)
				lastStage = rec.Stage
			}
			if rec.Status != "running" {
				if rec.Status != "ok" {
					return fmt.Errorf("工作台打包未完成（status=%s）——末尾日志:\n%s",
						rec.Status, tailLines(rec.Log, 8))
				}
				return nil
			}
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("打包 #%d 轮询超时（30 分钟）", id)
}

// DownloadPackage 下载打包产物 zip
func (c *StudioClient) DownloadPackage(id int64, dest string) error {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/packages/%d/download", c.Base, id), nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("X-Studio-Token", c.token)
	}
	resp, err := c.client(time.Hour).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("下载失败 HTTP %d", resp.StatusCode)
	}
	_ = os.Remove(dest)
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
