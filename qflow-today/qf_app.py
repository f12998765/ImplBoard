#!/usr/bin/env python3
"""轻流看板 · 桌面外壳

起一个本地只读代理（qf_server），再用系统 WebView 开一个窗口指向它。
窗口关掉，进程就退出。
"""
import os
import socket
import sys
import threading
import traceback

import qf_server


def free_port():
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def log(msg):
    try:
        path = os.path.join(qf_server.data_dir(), "qf_app.log")
        with open(path, "a", encoding="utf-8") as f:
            f.write(msg.rstrip() + "\n")
    except Exception:
        pass


def main():
    port = free_port()
    httpd = qf_server.start(port)
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    url = "http://127.0.0.1:%d/" % port
    log("started %s" % url)

    try:
        import webview
        webview.create_window(
            "轻流看板",
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

    # 没有可用的 WebView 时退回浏览器，进程继续挂着直到被关掉
    import webbrowser
    webbrowser.open(url)
    threading.Event().wait()


if __name__ == "__main__":
    try:
        main()
    except Exception:
        log("fatal:\n" + traceback.format_exc())
        raise
