// Package workspace 把 workspace 目录按“项目文档 / 设计 / 验收 / 证据 / 回执 /
// 工具 / 工件 / 运行 / Python / 脚本 / 知识”分类加载成索引。
//
// 为什么单独一层：TUI 的冻结上下文原来只读少量固定文件，os/ 下的子文件夹
// （acceptance/evidence/kernel_receipts/tools）和 docs/ 设计文档对模型是“看不见的”。
// 这里只读、分类、限量，绝不修改 workspace。
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// File 是 workspace 里的一个文件（按分类归属）。
type File struct {
	Path     string    `json:"path"`
	Category string    `json:"category"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// Category 是一类文件夹/文档集合。
type Category struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Files []File `json:"files"`
}

// Index 是分类索引。
type Index struct {
	Root       string     `json:"root"`
	Categories []Category `json:"categories"`
	Errors     []string   `json:"errors,omitempty"`
}

const (
	maxPerCategory = 200
	maxTotal       = 2000
)

// Scan 扫描 root，返回分类索引（容忍权限错误，不崩）。
func Scan(root string) *Index {
	idx := &Index{Root: root}
	if strings.TrimSpace(root) == "" {
		idx.Errors = append(idx.Errors, "workspace root 为空")
		return idx
	}
	cats := map[string]*Category{}
	add := func(name, rel string, info os.FileInfo) {
		if idx.total() >= maxTotal {
			return
		}
		c := cats[name]
		if c == nil {
			c = &Category{Name: name, Path: categoryPath(name)}
			cats[name] = c
		}
		if len(c.Files) >= maxPerCategory {
			return
		}
		c.Files = append(c.Files, File{
			Path:     filepath.ToSlash(rel),
			Category: name,
			Size:     info.Size(),
			Modified: info.ModTime(),
		})
	}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			idx.Errors = append(idx.Errors, fmt.Sprintf("%s: %v", p, err))
			return nil
		}
		if d.IsDir() {
			if skipDir(p, root) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		add(classify(rel), rel, info)
		return nil
	})
	for _, c := range cats {
		sort.Slice(c.Files, func(i, j int) bool { return c.Files[i].Path < c.Files[j].Path })
	}
	names := make([]string, 0, len(cats))
	for n := range cats {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		idx.Categories = append(idx.Categories, *cats[n])
	}
	return idx
}

func (i *Index) total() int {
	n := 0
	for _, c := range i.Categories {
		n += len(c.Files)
	}
	return n
}

func skipDir(p, root string) bool {
	base := filepath.Base(p)
	if p == root {
		return false
	}
	switch base {
	case ".git", "node_modules", ".pytest_cache", "__pycache__", ".crush", ".kilo", ".claude":
		return true
	}
	return false
}

// classify 按相对路径把文件归到稳定类别（顺序即优先级）。
func classify(rel string) string {
	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")
	base := parts[0]
	switch {
	case strings.HasPrefix(rel, "os/acceptance/"):
		return "acceptance"
	case strings.HasPrefix(rel, "os/evidence/"):
		return "evidence"
	case strings.HasPrefix(rel, "os/kernel_receipts/"):
		return "receipts"
	case strings.HasPrefix(rel, "os/tools/"):
		return "tools"
	case base == "docs":
		return "design"
	case base == "artifacts":
		return "artifacts"
	case base == "runs":
		return "runs"
	case base == "python":
		return "python"
	case base == "manifests":
		return "manifests"
	case base == "scripts":
		return "scripts"
	case base == "knowledge":
		return "knowledge"
	case len(parts) == 1 && strings.HasSuffix(rel, ".md"):
		return "project"
	case base == "os" && strings.HasSuffix(rel, ".md"):
		return "project"
	default:
		return "other"
	}
}

func categoryPath(name string) string {
	switch name {
	case "acceptance":
		return "os/acceptance"
	case "evidence":
		return "os/evidence"
	case "receipts":
		return "os/kernel_receipts"
	case "tools":
		return "os/tools"
	case "design":
		return "docs"
	case "artifacts":
		return "artifacts"
	case "runs":
		return "runs"
	case "python":
		return "python"
	case "manifests":
		return "manifests"
	case "scripts":
		return "scripts"
	case "knowledge":
		return "knowledge"
	case "project":
		return "os + 顶层 *.md"
	default:
		return "."
	}
}

// Render 输出适合注入 system 的简短分类索引（默认每类最多 8 个文件）。
func Render(idx *Index, perCategory int) string {
	if idx == nil {
		return ""
	}
	if perCategory <= 0 {
		perCategory = 8
	}
	var b strings.Builder
	b.WriteString("## Workspace 分类索引（只读；需要全文用 read/ls）\n")
	for _, c := range idx.Categories {
		b.WriteString(fmt.Sprintf("- %s (%s)：%d 个文件\n", c.Name, c.Path, len(c.Files)))
		for i, f := range c.Files {
			if i >= perCategory {
				b.WriteString(fmt.Sprintf("  …还有 %d 个\n", len(c.Files)-perCategory))
				break
			}
			b.WriteString("  - " + f.Path + "\n")
		}
	}
	if len(idx.Errors) > 0 {
		b.WriteString("- 加载错误：\n")
		for _, e := range idx.Errors {
			if len(e) > 160 {
				e = e[:160] + "…"
			}
			b.WriteString("  - " + e + "\n")
		}
	}
	return b.String()
}
