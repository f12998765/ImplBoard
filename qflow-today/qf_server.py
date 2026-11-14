#!/usr/bin/env python3
"""轻流看板后端：只读代理。

只调用读取类接口：
  GET  /api/tag/apps                      应用包 / 应用树
  POST /api/app/{key}/apply/filter/amount  记录数（可按日期区间）
  GET  /api/app/{key}/publish              最后发布时间
不调用任何写接口（发布、保存、删除都没有）。
"""
import json
import os
import re
import sys
import time
import threading
import urllib.request
import urllib.error
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
from concurrent.futures import ThreadPoolExecutor, as_completed

HERE = os.path.dirname(os.path.abspath(__file__))
APP_NAME = "ImplBoard"
LEGACY_APP_NAME = "DataBoard"


def res_dir():
    """打包后静态资源在 PyInstaller 的临时解包目录里。"""
    return getattr(sys, "_MEIPASS", HERE)


def data_dir():
    """可写目录：macOS 的 .app 内部是只读的，配置必须放用户目录。"""
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


AUTH_PATH = os.environ.get("QF_AUTH") or os.path.join(data_dir(), "qf_auth.json")
def _page_file():
    if getattr(sys, "_MEIPASS", None):
        return os.path.join(res_dir(), "qf", "index.html")
    return os.path.join(HERE, "index.html")


PAGE_PATH = _page_file()

def _install_ca():
    """打包后机器上可能没有 CA 证书，优先用随包带的 certifi。"""
    if os.environ.get("SSL_CERT_FILE"):
        return os.environ["SSL_CERT_FILE"]
    cands = []
    try:
        import certifi
        cands.append(certifi.where())
    except Exception:  # noqa: BLE001
        pass
    cands += [
        "/etc/ssl/cert.pem",
        "/opt/homebrew/etc/openssl@4/cert.pem",
        "/opt/homebrew/etc/openssl@3/cert.pem",
        "/usr/local/etc/openssl@3/cert.pem",
    ]
    for p in cands:
        try:
            if p and os.path.isfile(p):
                os.environ["SSL_CERT_FILE"] = p
                os.environ["SSL_CERT_DIR"] = os.path.dirname(p)
                return p
        except Exception:  # noqa: BLE001
            continue
    return ""


CA_FILE = _install_ca()


BASE = "https://qingflow.com/api"
WORKERS = int(os.environ.get("QF_WORKERS", "16"))
TIMEOUT = 8
APPS_TTL = 600
REPORT_TTL = 120

PROGRESS = {"phase": "idle", "done": 0, "total": 0, "current": "", "logs": [], "at": 0}
PROGRESS_LOCK = threading.Lock()


def now_ms():
    return int(time.time() * 1000)


def progress_set(**kw):
    with PROGRESS_LOCK:
        PROGRESS.update(kw)
        PROGRESS["at"] = time.time()


def progress_log(msg):
    with PROGRESS_LOCK:
        PROGRESS["logs"].append({"t": time.strftime("%H:%M:%S"), "m": msg})
        if len(PROGRESS["logs"]) > 40:
            del PROGRESS["logs"][0]
        PROGRESS["at"] = time.time()


class QFApi:
    def __init__(self, token, wsid, qfversion, cookie_extra=""):
        self.token = token
        self.wsid = wsid
        self.qfversion = qfversion
        self.cookie_extra = cookie_extra

    def headers(self, json_body=False):
        h = {
            "token": self.token,
            "wsid": str(self.wsid),
            "accept": "application/json, text/plain, */*",
            "referer": "https://qingflow.com/",
            "user-agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "
                          "(KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36",
            "x-q-request-id": "%032x" % (int(time.time() * 1000) % (1 << 128)),
        }
        cookie = "qfVersion=%s; prodtoken=%s; prodwsId=%s" % (
            self.qfversion, self.token, self.wsid)
        if self.cookie_extra:
            cookie += "; " + self.cookie_extra
        h["Cookie"] = cookie
        if json_body:
            h["content-type"] = "application/json"
            h["NewInterfaceSkipHeader"] = ""
        return h

    def _req(self, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(BASE + path, data=data,
                                     headers=self.headers(body is not None),
                                     method="POST" if body is not None else "GET")
        try:
            with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
                return json.loads(r.read().decode("utf-8", "replace"))
        except urllib.error.HTTPError as e:
            return {"code": e.code, "message": "HTTP %s" % e.code}
        except Exception as e:
            return {"code": -1, "message": str(e)}

    def tree(self):
        r = self._req("/tag/apps")
        if r.get("code") != 0:
            raise RuntimeError(r.get("message") or str(r.get("code")))
        return (r.get("data") or {}).get("tags") or []

    def amount(self, app_key, day=None, since=None):
        body = {"pageNum": 1, "pageSize": 1, "type": 8}
        if since:
            end = (day or time.strftime("%Y-%m-%d")) + " 23:59:59"
            body["queries"] = [{"queId": 2, "scope": 1, "minValue": since, "maxValue": end}]
        elif day:
            body["queries"] = [{"queId": 2, "scope": 1, "minValue": day, "maxValue": day}]
        for attempt in range(2):
            r = self._req("/app/%s/apply/filter/amount" % app_key, body)
            if r.get("code") == 0:
                return ((r.get("data") or {}).get("resultAmount"))
            if r.get("code") not in (-1, 500, 502, 503, 504):
                return None
        return None

    def publish(self, app_key):
        for attempt in range(2):
            r = self._req("/app/%s/publish" % app_key)
            if r.get("code") == 0:
                d = r.get("data") or {}
                return d.get("lastPublishTime"), (d.get("lastPublishUser") or {}).get("userName")
            if r.get("code") not in (-1, 500, 502, 503, 504):
                return None, None
        return None, None


def flatten(tags):
    """tag -> tagItems(+subItems) 摊平成 [{tagId,tagName,apps:[{key,name}]}]"""
    out = []

    def walk(items, bucket, seen):
        for it in items or []:
            if it.get("itemType") == 1 and it.get("appKey"):
                key = it["appKey"]
                if key not in seen:
                    seen.add(key)
                    bucket.append({"key": key,
                                   "name": it.get("title") or it.get("relationKey") or key,
                                   "trial": bool(it.get("beingTrial"))})
            walk(it.get("subItems"), bucket, seen)

    for t in tags:
        bucket = []
        walk(t.get("tagItems"), bucket, set())
        out.append({"tagId": t.get("tagId"), "tagName": t.get("tagName") or "未分组",
                    "apps": bucket})
    return out


COUNT_TTL = 300


def cached_count(api, prof, key, day, since=None):
    ck = (prof, key, day or "-", since or "-")
    with CACHE.lock:
        hit = CACHE.counts.get(ck)
    if hit and time.time() - hit[0] < COUNT_TTL:
        return hit[1]
    v = api.amount(key, day, since)
    with CACHE.lock:
        CACHE.counts[ck] = (time.time(), v)
    return v


def cached_publish(api, prof, key):
    with CACHE.lock:
        hit = CACHE.pubs.get((prof, key))
    if hit and time.time() - hit[0] < COUNT_TTL:
        return hit[1]
    v = api.publish(key)
    with CACHE.lock:
        CACHE.pubs[(prof, key)] = (time.time(), v)
    return v


class Cache:
    def __init__(self):
        self.lock = threading.Lock()
        self.apps = {}
        self.reports = {}
        self.counts = {}
        self.pubs = {}
        self.keylocks = {}

    def keylock(self, key):
        with self.lock:
            lk = self.keylocks.get(key)
            if lk is None:
                lk = threading.Lock()
                self.keylocks[key] = lk
            return lk


CACHE = Cache()


def load_auth():
    if not os.path.exists(AUTH_PATH):
        return {"profiles": [], "active": None}
    try:
        with open(AUTH_PATH, "r", encoding="utf-8") as f:
            return json.load(f)
    except Exception:
        return {"profiles": [], "active": None}


def save_auth(cfg):
    with open(AUTH_PATH, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)
    try:
        os.chmod(AUTH_PATH, 0o600)
    except Exception:
        pass


def active_profile(cfg=None):
    cfg = cfg or load_auth()
    for p in cfg.get("profiles") or []:
        if p.get("id") == cfg.get("active"):
            return p
    return None


CURL_TOKEN = re.compile(r"-H\s+[\"']token:\s*([^\"']+)[\"']", re.I)
CURL_WSID = re.compile(r"-H\s+[\"']wsid:\s*([^\"']+)[\"']", re.I)
CURL_COOKIE = re.compile(r"(?:-b|--cookie)\s+[\"']([^\"']+)[\"']", re.I)


def parse_curl(text):
    """从浏览器复制出来的 cURL 里取 token / wsid / qfVersion。"""
    text = (text or "").replace("\\\n", " ").replace("\r", "").replace("\n", " ")
    out = {"token": "", "wsid": "", "qfVersion": "mongo1", "cookieExtra": ""}

    m = CURL_TOKEN.search(text)
    if m:
        out["token"] = m.group(1).strip()
    m = CURL_WSID.search(text)
    if m:
        out["wsid"] = m.group(1).strip()

    cookie = ""
    m = CURL_COOKIE.search(text)
    if m:
        cookie = m.group(1)
    if cookie:
        jar = {}
        for part in cookie.split(";"):
            if "=" in part:
                k, v = part.split("=", 1)
                jar[k.strip()] = v.strip()
        if jar.get("prodtoken"):
            out["token"] = out["token"] or jar["prodtoken"]
        if jar.get("prodwsId"):
            out["wsid"] = out["wsid"] or jar["prodwsId"]
        if jar.get("qfVersion"):
            out["qfVersion"] = jar["qfVersion"]
        keep = ["prodUid", "JSESSIONID", "prodwsId"]
        extra = "; ".join("%s=%s" % (k, jar[k]) for k in keep if jar.get(k))
        out["cookieExtra"] = extra
    return out


def build_report(api, day, fresh=False):
    key = (api.token[-6:], day)
    if not fresh:
        with CACHE.lock:
            hit = CACHE.reports.get(key)
        if hit and time.time() - hit[0] < REPORT_TTL:
            return hit[1]
    with CACHE.keylock(key):
        if not fresh:
            with CACHE.lock:
                hit = CACHE.reports.get(key)
            if hit and time.time() - hit[0] < REPORT_TTL:
                return hit[1]
        return _build_report_locked(api, day, key)


def _build_report_locked(api, day, key):
    progress_set(phase="count", done=0, total=0, current="", logs=[])
    progress_log("读取应用树")
    prof_key = api.token[-6:]
    with CACHE.lock:
        hit = CACHE.apps.get(prof_key)
    if not hit or time.time() - hit[0] > APPS_TTL:
        tree = api.tree()
        with CACHE.lock:
            CACHE.apps[prof_key] = (time.time(), tree)
        hit = (time.time(), tree)
    groups = flatten(hit[1])

    apps = []
    seen_app = set()
    for g in groups:
        for a in g["apps"]:
            if a["key"] in seen_app:
                continue
            seen_app.add(a["key"])
            apps.append(dict(a, tag=g["tagName"]))

    days = []
    base = time.strptime(day, "%Y-%m-%d")
    for i in range(6, -1, -1):
        t = time.mktime(base) - i * 86400
        days.append(time.strftime("%Y-%m-%d", time.localtime(t)))

    progress_set(total=len(apps), current="")
    progress_log("共 %d 个应用（%d 个应用包），开始统计" % (len(apps), len(groups)))

    def work(a):
        total = cached_count(api, prof_key, a["key"], None)
        dc = cached_count(api, prof_key, a["key"], day)
        pub, user = cached_publish(api, prof_key, a["key"])
        after = None
        if pub:
            pd = time.strftime("%Y-%m-%d", time.localtime(pub / 1000.0))
            if pd == day:
                after = cached_count(api, prof_key, a["key"], day,
                                     since=time.strftime("%Y-%m-%d %H:%M:%S",
                                                         time.localtime(pub / 1000.0))) or 0
        return a["key"], {"total": total or 0, "day": dc or 0,
                          "afterPublish": after,
                          "publish": pub, "publishUser": user}

    rows = {}
    done = 0
    with ThreadPoolExecutor(max_workers=WORKERS) as ex:
        futs = [ex.submit(work, a) for a in apps]
        for f in as_completed(futs):
            try:
                k, v = f.result()
                rows[k] = v
            except Exception as e:
                progress_log("失败：%s" % e)
            done += 1
            if done % 5 == 0 or done == len(apps):
                progress_set(done=done)
            if done % 20 == 0 or done == len(apps):
                progress_log("已统计 %d/%d" % (done, len(apps)))

    progress_set(phase="trend", done=0, total=len(apps), current="7 日趋势")
    progress_log("统计 7 日趋势（%s ~ %s）" % (days[0], days[-1]))
    trend_total = {}
    for i, d in enumerate(days):
        cnt = 0

        def twork(a):
            return cached_count(api, prof_key, a["key"], d) or 0

        with ThreadPoolExecutor(max_workers=WORKERS) as ex:
            for c in ex.map(twork, apps):
                cnt += c
        trend_total[d] = cnt
        progress_set(done=i + 1)
        progress_log("%s 新增 %d" % (d, cnt))

    out_apps = []
    hours = {h: 0 for h in range(24)}
    published = 0
    for a in apps:
        r = rows.get(a["key"], {})
        pub = r.get("publish")
        pub_day = ""
        if pub:
            try:
                pub_day = time.strftime("%Y-%m-%d", time.localtime(int(pub) / 1000))
                if pub_day == day:
                    published += 1
                    hours[time.localtime(int(pub) / 1000).tm_hour] += 1
            except Exception:
                pub_day = ""
        out_apps.append({
            "key": a["key"], "name": a["name"], "tag": a.get("tag"),
            "total": r.get("total", 0), "day": r.get("day", 0),
            "afterPublish": r.get("afterPublish"),
            "publish": pub, "publishDay": pub_day,
            "publishUser": r.get("publishUser"),
            "publishedToday": pub_day == day,
        })

    report = {
        "day": day,
        "apps": out_apps,
        "trend": [{"d": d, "n": trend_total[d]} for d in days],
        "hours": [{"h": h, "n": hours[h]} for h in range(24)],
        "sum": {
            "apps": len(out_apps),
            "groups": len(groups),
            "total": sum(a["total"] for a in out_apps),
            "day": sum(a["day"] for a in out_apps),
            "withData": sum(1 for a in out_apps if a["day"] > 0),
            "published": published,
        },
        "generatedAt": now_ms(),
    }
    with CACHE.lock:
        CACHE.reports[key] = (time.time(), report)
    progress_set(phase="done", done=len(apps), current="")
    progress_log("完成：%d 个应用，当日发布 %d 个" % (len(apps), published))
    return report


class Handler(BaseHTTPRequestHandler):
    server_version = "qf-dashboard"

    def log_message(self, fmt, *args):
        sys.stderr.write("%s %s\n" % (time.strftime("%H:%M:%S"), fmt % args))

    def _send(self, code, body, ctype="application/json; charset=utf-8"):
        if isinstance(body, (dict, list)):
            body = json.dumps(body, ensure_ascii=False)
        raw = body.encode("utf-8") if isinstance(body, str) else body
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(raw)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(raw)

    def _json(self, obj, code=200):
        self._send(code, obj)

    def do_GET(self):
        from urllib.parse import urlparse, parse_qs
        u = urlparse(self.path)
        q = parse_qs(u.query)
        path = u.path

        if path in ("/", "/index.html"):
            try:
                with open(PAGE_PATH, "rb") as f:
                    self._send(200, f.read(), "text/html; charset=utf-8")
            except FileNotFoundError:
                self._send(404, "index.html missing", "text/plain")
            return

        if path == "/api/auth":
            cfg = load_auth()
            return self._json({"active": cfg.get("active"), "profiles": [
                {"id": p.get("id"), "name": p.get("name"), "wsid": p.get("wsid")}
                for p in cfg.get("profiles") or []]})

        if path == "/api/progress":
            with PROGRESS_LOCK:
                return self._json(dict(PROGRESS))

        prof = active_profile()
        if not prof:
            return self._json({"error": "未配置账号"}, 401)

        api = QFApi(prof["token"], prof["wsid"], prof.get("qfVersion") or "mongo1",
                    prof.get("cookieExtra") or "")

        if path == "/api/apps":
            try:
                tree = api.tree()
                with CACHE.lock:
                    CACHE.apps[prof["token"][-6:]] = (time.time(), tree)
                gs = flatten(tree)
                return self._json({"groups": gs,
                                   "apps": sum(len(g["apps"]) for g in gs)})
            except Exception as e:
                return self._json({"error": str(e)}, 500)

        if path == "/api/report":
            day = (q.get("day") or [time.strftime("%Y-%m-%d")])[0]
            fresh = (q.get("fresh") or [""])[0] == "1"
            try:
                return self._json(build_report(api, day, fresh=fresh))
            except Exception as e:
                return self._json({"error": str(e)}, 500)

        return self._json({"error": "not found"}, 404)

    def do_POST(self):
        from urllib.parse import urlparse
        path = urlparse(self.path).path
        n = int(self.headers.get("Content-Length") or 0)
        try:
            body = json.loads(self.rfile.read(n).decode() or "{}")
        except Exception:
            body = {}

        if path == "/api/auth":
            cfg = load_auth()
            op = body.get("op") or "add"
            if op == "add":
                info = parse_curl(body.get("curl") or "")
                if not info["token"]:
                    return self._json({"error": "没有从 cURL 里解析到 token，请确认复制了 -H 'token: ...' 或 prodtoken cookie"}, 400)
                if not info["wsid"]:
                    return self._json({"error": "没有解析到 wsid（-H 'wsid: ...' 或 prodwsId cookie）"}, 400)
                pid = "q%s" % int(time.time())
                name = body.get("name") or ("wsid " + str(info["wsid"]))
                cfg.setdefault("profiles", []).append({
                    "id": pid, "name": name, "token": info["token"],
                    "wsid": info["wsid"], "qfVersion": info["qfVersion"],
                    "cookieExtra": info["cookieExtra"],
                })
                cfg["active"] = pid
                CACHE.reports.clear()
            elif op == "switch":
                cfg["active"] = body.get("id")
            elif op == "delete":
                cfg["profiles"] = [p for p in cfg.get("profiles") or []
                                   if p.get("id") != body.get("id")]
                if cfg.get("active") == body.get("id"):
                    cfg["active"] = (cfg["profiles"][0]["id"] if cfg["profiles"] else None)
                CACHE.reports.clear()
            save_auth(cfg)
            return self._json({"ok": True, "active": cfg.get("active")})
        return self._json({"error": "not found"}, 404)


def seed_from_env():
    token = os.environ.get("QF_TOKEN")
    if not token:
        return
    cfg = load_auth()
    wsid = os.environ.get("QF_WSID", "690763")
    qfver = os.environ.get("QF_VERSION", "mongo1")
    for p in cfg.get("profiles") or []:
        if p.get("token") == token:
            p["wsid"] = wsid
            p["qfVersion"] = qfver
            cfg["active"] = p["id"]
            save_auth(cfg)
            return
    pid = "qenv"
    cfg.setdefault("profiles", [])
    cfg["profiles"] = [p for p in cfg["profiles"] if p.get("id") != pid]
    cfg["profiles"].append({"id": pid, "name": "锋芒（env）", "token": token,
                            "wsid": wsid, "qfVersion": qfver, "cookieExtra": ""})
    cfg["active"] = pid
    save_auth(cfg)


def start(port=8788):
    """起服务并返回 server 对象，供桌面外壳调用（端口传 0 表示自动挑）。"""
    seed_from_env()
    srv = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    srv.daemon_threads = True
    return srv


def main():
    port = 8788
    for i, a in enumerate(sys.argv):
        if a == "--port" and i + 1 < len(sys.argv):
            port = int(sys.argv[i + 1])
    srv = start(port)
    print("轻流看板 → http://127.0.0.1:%d" % srv.server_address[1], flush=True)
    srv.serve_forever()


if __name__ == "__main__":
    main()
