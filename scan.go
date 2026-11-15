// 通用仓库扫描：按 recipe.scan 规则发现可采集模块（目录前缀 + 必备子目录）与基座版本。
// 核心不预设任何目录约定——mse-modules/mse-<m>/<m>-service 等是 ai-mse recipe 的数据。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ModuleInfo 扫描出的模块（短名 + 探测子目录标注）
type ModuleInfo struct {
	Short  string   `json:"short"`
	Probes []string `json:"probes"` // 命中的探测子目录标注（如 service/ui/mobile-ui）
	Label  string   `json:"label"`  // 向导展示：service · ui · mobile-ui
}

// ScanResult 扫描结果（向导 meta 与 hook 参数共用）
type ScanResult struct {
	RepoRoot    string       `json:"repoRoot"`
	Modules     []ModuleInfo `json:"modules"`
	BaseVersion string       `json:"baseVersion"`
}

var versionTagRe = regexp.MustCompile(`<version>\s*([^<]+?)\s*</version>`)

// ScanRepo 按 ScanSpec 扫描仓库根
func ScanRepo(root string, spec *ScanSpec) (*ScanResult, error) {
	res := &ScanResult{RepoRoot: root, BaseVersion: "1.0.0-SNAPSHOT"}
	if spec == nil {
		return res, nil
	}
	// 基座版本：VersionFile 首个 <version> 标签；提取失败（文件缺失/无标签）用 VersionDefault 兜底
	if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(spec.VersionFile))); err == nil {
		if m := versionTagRe.FindSubmatch(data); m != nil {
			res.BaseVersion = string(m[1])
		}
	}
	if spec.VersionDefault != "" && res.BaseVersion == "1.0.0-SNAPSHOT" {
		res.BaseVersion = spec.VersionDefault
	}
	if spec.ModulesDir == "" {
		return res, nil
	}
	entries, err := os.ReadDir(filepath.Join(root, spec.ModulesDir))
	if err != nil {
		return res, fmt.Errorf("读取模块目录失败（仓库根不对？）: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), spec.ModulePrefix) {
			continue
		}
		short := strings.TrimPrefix(e.Name(), spec.ModulePrefix)
		if short == "" {
			continue
		}
		modDir := filepath.Join(root, spec.ModulesDir, e.Name())
		if spec.RequireSuffix != "" && !dirExists(filepath.Join(modDir, short+spec.RequireSuffix)) {
			continue
		}
		mi := ModuleInfo{Short: short}
		if spec.RequireSuffix != "" {
			mi.Probes = append(mi.Probes, strings.TrimPrefix(spec.RequireSuffix, "-"))
		}
		for _, p := range spec.ProbeDirs {
			if dirExists(filepath.Join(modDir, short+p.Suffix)) {
				mi.Probes = append(mi.Probes, p.Label)
			}
		}
		mi.Label = strings.Join(mi.Probes, " · ")
		res.Modules = append(res.Modules, mi)
	}
	sort.Slice(res.Modules, func(i, j int) bool { return res.Modules[i].Short < res.Modules[j].Short })
	return res, nil
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
