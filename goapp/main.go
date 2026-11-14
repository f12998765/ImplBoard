package main

import (
	"embed"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"implboard/internal/jdy"
	"implboard/internal/qf"
)

//go:embed all:assets
var assets embed.FS

const (
	appName       = "ImplBoard"
	legacyAppName = "DataBoard"
)

// dataDir 与 Python 版保持一致：优先 ImplBoard，不存在时沿用旧的 DataBoard 目录。
func dataDir() string {
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
	dir := filepath.Join(base, appName)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		old := filepath.Join(base, legacyAppName)
		if _, err := os.Stat(old); err == nil {
			return old
		}
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func logf(format string, args ...any) {
	f, err := os.OpenFile(filepath.Join(dataDir(), "app.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

func listen(handler http.Handler) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go func() {
		if err := http.Serve(ln, handler); err != nil {
			logf("serve %s: %v", ln.Addr(), err)
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

func main() {
	dir := dataDir()
	logf("booting data=%s", dir)

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
		logf("jdy init: %v", err)
	}
	qfSrv, err := qf.New(qf.Options{AuthPath: qfAuth, DataDir: dir})
	if err != nil {
		logf("qf init: %v", err)
	}

	jdyPage, err := page("jdy.html", jdySrv.Handler())
	if err != nil {
		log.Fatal(err)
	}
	qfPage, err := page("qf.html", qfSrv.Handler())
	if err != nil {
		log.Fatal(err)
	}

	jdyURL, err := listen(jdyPage)
	if err != nil {
		log.Fatal(err)
	}
	qfURL, err := listen(qfPage)
	if err != nil {
		log.Fatal(err)
	}

	home, err := assets.ReadFile("assets/home.html")
	if err != nil {
		log.Fatal(err)
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
	homeURL, err = listen(homeHandler)
	if err != nil {
		log.Fatal(err)
	}
	logf("started home=%s jdy=%s qf=%s", homeURL, jdyURL, qfURL)

	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:     "实施看板",
			URL:       homeURL + "/",
			Width:     1360,
			Height:    900,
			MinWidth:  980,
			MinHeight: 640,
		})
	})
	if err := mygo.App.Run(); err != nil {
		logf("run: %v", err)
		log.Fatal(err)
	}
	logf("window closed")
}
