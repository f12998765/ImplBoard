// Package boot 启动三个本地端口（主页 / 简道云 / 轻流），把内嵌页面和只读接口
// 一起 serve 出去。桌面端的 mygo 窗口和 Android 的 WebView 都从这里拿主页地址。
package boot

import (
	"embed"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"implboard/internal/jdy"
	"implboard/internal/qf"
)

//go:embed all:assets
var assets embed.FS

const (
	appName       = "ImplBoard"
	legacyAppName = "DataBoard"
)

// DataDir 返回可写的数据目录。dir 非空时直接用它（Android 传应用私有目录），
// 否则看 $IMPLBOARD_HOME，再退回平台默认位置；旧版 DataBoard 目录仍兼容。
func DataDir(dir string) string {
	if dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		return dir
	}
	if v := os.Getenv("IMPLBOARD_HOME"); v != "" {
		_ = os.MkdirAll(v, 0o755)
		return v
	}
	var base string
	if v := os.Getenv("APPDATA"); v != "" { // windows
		base = v
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return "."
		}
		base = filepath.Join(home, "Library", "Application Support")
	}
	dir = filepath.Join(base, appName)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		old := filepath.Join(base, legacyAppName)
		if _, err := os.Stat(old); err == nil {
			return old
		}
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// Logf 往 <dir>/app.log 追加一行。写不进去就静默，日志不能影响主流程。
func Logf(dir, format string, args ...any) {
	f, err := os.OpenFile(filepath.Join(dir, "app.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

func listen(dir string, handler http.Handler) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go func() {
		if err := http.Serve(ln, handler); err != nil {
			Logf(dir, "serve %s: %v", ln.Addr(), err)
		}
	}()
	return "http://" + ln.Addr().String(), nil
}

// page 返回看板的静态入口：/ 与 /index.html 给页面，其余交给 API。
func page(name string, api http.Handler) (http.Handler, error) {
	body, err := assets.ReadFile("assets/" + name)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"), r.URL.Path == "/favicon.ico":
			api.ServeHTTP(w, r)
		case r.URL.Path == "/" || r.URL.Path == "/index.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}), nil
}

// Start 起好三个端口并返回主页地址；dir 为空时用平台默认数据目录。
func Start(dir string) (string, error) {
	dir = DataDir(dir)
	Logf(dir, "booting data=%s", dir)

	jdyAuth := os.Getenv("JDY_AUTH")
	if jdyAuth == "" {
		jdyAuth = filepath.Join(dir, "jdy_auth.json")
	}
	qfAuth := os.Getenv("QF_AUTH")
	if qfAuth == "" {
		qfAuth = filepath.Join(dir, "qf_auth.json")
	}

	jdySrv, err := jdy.New(jdy.Options{AuthPath: jdyAuth, DataDir: dir})
	if err != nil {
		Logf(dir, "jdy init: %v", err)
	}
	qfSrv, err := qf.New(qf.Options{AuthPath: qfAuth, DataDir: dir})
	if err != nil {
		Logf(dir, "qf init: %v", err)
	}
	if jdySrv == nil || qfSrv == nil {
		return "", fmt.Errorf("看板服务初始化失败")
	}

	jdyPage, err := page("jdy.html", jdySrv.Handler())
	if err != nil {
		return "", err
	}
	qfPage, err := page("qf.html", qfSrv.Handler())
	if err != nil {
		return "", err
	}

	jdyURL, err := listen(dir, jdyPage)
	if err != nil {
		return "", err
	}
	qfURL, err := listen(dir, qfPage)
	if err != nil {
		return "", err
	}

	home, err := assets.ReadFile("assets/home.html")
	if err != nil {
		return "", err
	}
	var homeURL string
	homeHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" || path == "/index.html" {
			back := url.QueryEscape(homeURL + "/")
			html := strings.ReplaceAll(string(home), "{{JDY}}", jdyURL+"/?home="+back)
			html = strings.ReplaceAll(html, "{{QF}}", qfURL+"/?home="+back)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(html))
			return
		}
		if strings.HasPrefix(path, "/icon/") {
			if body, err := assets.ReadFile("assets/icons/" + filepath.Base(path)); err == nil {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(body)
				return
			}
		}
		http.NotFound(w, r)
	})
	homeURL, err = listen(dir, homeHandler)
	if err != nil {
		return "", err
	}

	Logf(dir, "started home=%s jdy=%s qf=%s", homeURL, jdyURL, qfURL)
	return homeURL, nil
}
