package main

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

// 前端页面和节点端安装包在构建时内嵌进面板程序，所以部署只需要一个文件。
// scripts/build.sh 会先把它们放进这两个目录再编译。
//
//go:embed all:webdist
var embeddedWeb embed.FS

//go:embed all:nodedist
var embeddedDist embed.FS

// pickFS 优先用命令行指定的磁盘目录（开发时用），否则用内嵌的文件。
func pickFS(dir string, embedded embed.FS, sub string) fs.FS {
	if dir != "" {
		return os.DirFS(dir)
	}
	f, _ := fs.Sub(embedded, sub)
	return f
}

// serveWeb 托管前端单页应用：存在的文件直接返回，其余路径回退到 index.html。
func serveWeb(r *gin.Engine, fsys fs.FS) {
	index, _ := fs.ReadFile(fsys, "index.html")
	files := http.FileServer(http.FS(fsys))
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+c.Request.URL.Path), "/")
		if p != "" && p != "index.html" {
			if st, err := fs.Stat(fsys, p); err == nil && !st.IsDir() {
				files.ServeHTTP(c.Writer, c.Request)
				return
			}
		}
		if index == nil {
			c.String(http.StatusNotFound, "这个面板程序没有内嵌前端页面，请用 scripts/build.sh 构建，或用 -web 指定前端目录。")
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	})
}

var (
	distFS       fs.FS
	downloadName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// handleDownload 提供节点端的安装脚本和各架构的安装包。不需要登录，
// 因为节点机器要直接用 curl 下载；这里只有可以公开的安装文件。
func handleDownload(c *gin.Context) {
	name := c.Param("file")
	if !downloadName.MatchString(name) || name == "README.txt" {
		c.Status(http.StatusNotFound)
		return
	}
	if st, err := fs.Stat(distFS, name); err != nil || st.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}
	c.FileFromFS(name, http.FS(distFS))
}
