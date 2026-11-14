#!/usr/bin/env python3
"""实施看板 · 桌面外壳（ImplBoard）

一个主页，进去是简道云 / 轻流两个看板；看板左上角可以回主页。
三个本地只读服务：简道云代理、轻流代理、主页静态服务。
"""
import os
import sys
import threading
import traceback
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
from urllib.parse import quote, urlparse

import jdy_today_server as jdy
import qf_server as qf

HERE = os.path.dirname(os.path.abspath(__file__))
APP_NAME = "ImplBoard"
LEGACY_APP_NAME = "DataBoard"

PORTS = {"home": 0, "jdy": 0, "qf": 0}


def res_dir():
    return getattr(sys, "_MEIPASS", HERE)


def data_dir():
    if sys.platform == "darwin":
        base = os.path.expanduser("~/Library/Application Support")
    elif os.name == "nt":
        base = os.environ.get("APPDATA") or os.path.expanduser("~")
    else:
        base = os.environ.get("XDG_CONFIG_HOME") or os.path.expanduser("~/.config")
    d = os.path.join(base, APP_NAME)
    if not os.path.exists(d):
        old = os.path.join(base, LEGACY_APP_NAME)
        if os.path.exists(old):
            return old
    os.makedirs(d, exist_ok=True)
    return d


def log(msg):
    try:
        with open(os.path.join(data_dir(), "app.log"), "a", encoding="utf-8") as f:
            f.write(msg.rstrip() + "\n")
    except Exception:
        pass


class HomeHandler(BaseHTTPRequestHandler):
    server_version = "BoardHome/1.0"

    def log_message(self, *a):
        pass

    def _send(self, body, ctype, code=200):
        if isinstance(body, str):
            body = body.encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):  # noqa: N802
        path = urlparse(self.path).path

        if path in ("/", "/index.html"):
            try:
                with open(os.path.join(res_dir(), "home.html"), encoding="utf-8") as f:
                    html = f.read()
            except OSError:
                return self._send("home.html missing", "text/plain; charset=utf-8", 500)
            home = "http://127.0.0.1:%d/" % PORTS["home"]
            back = quote(home, safe="")
            html = html.replace("{{JDY}}", "http://127.0.0.1:%d/?home=%s" % (PORTS["jdy"], back))
            html = html.replace("{{QF}}", "http://127.0.0.1:%d/?home=%s" % (PORTS["qf"], back))
            return self._send(html, "text/html; charset=utf-8")

        if path.startswith("/icon/"):
            fp = os.path.join(res_dir(), "icons", os.path.basename(path))
            if os.path.isfile(fp):
                with open(fp, "rb") as f:
                    return self._send(f.read(), "image/png")

        return self._send("not found", "text/plain; charset=utf-8", 404)


def serve_in_background(srv):
    threading.Thread(target=srv.serve_forever, daemon=True).start()


def main():
    jdy_srv = jdy.start(0)
    PORTS["jdy"] = jdy_srv.server_address[1]
    serve_in_background(jdy_srv)

    qf_srv = qf.start(0)
    PORTS["qf"] = qf_srv.server_address[1]
    serve_in_background(qf_srv)

    home_srv = ThreadingHTTPServer(("127.0.0.1", 0), HomeHandler)
    home_srv.daemon_threads = True
    PORTS["home"] = home_srv.server_address[1]
    serve_in_background(home_srv)

    url = "http://127.0.0.1:%d/" % PORTS["home"]
    try:
        if sys.stdout:  # Windows 的 --windowed 下 stdout 是 None
            print("[ca] qf=%s jdy=%s" % (qf.CA_FILE or "(none)", jdy.CA_FILE or "(none)"), flush=True)
    except Exception:  # noqa: BLE001
        pass
    log("ca=%s" % (qf.CA_FILE or "(none)"))
    log("started home=%s jdy=%d qf=%d" % (url, PORTS["jdy"], PORTS["qf"]))

    try:
        import webview

        webview.create_window(
            "实施看板",
            url,
            width=1360,
            height=900,
            min_size=(980, 640),
        )
        webview.start()
        log("window closed")
        return
    except Exception:
        log("webview failed:\n" + traceback.format_exc())

    import webbrowser

    webbrowser.open(url)
    threading.Event().wait()


if __name__ == "__main__":
    try:
        main()
    except Exception:
        log("fatal:\n" + traceback.format_exc())
        raise
