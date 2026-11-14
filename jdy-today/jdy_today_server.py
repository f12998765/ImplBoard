#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
简道云 · 表单概览看板（本地只读服务）

提供三个只读接口给页面用：
  GET /api/apps            应用包列表（右上角下拉）
  GET /api/report?appId=   今日概览 + 全部表单
  GET /api/trend?appId=    近 7 日新增记录趋势

认证
----
浏览器 Cookie 是 HttpOnly，页面 JS 读不到，所以由本服务转发请求：

    python3 jdy_today_server.py --curl curl.txt

curl.txt 是浏览器里「Copy as cURL (bash)」的任意一个请求，脚本自动提取
Cookie / x-csrf-token / host / appId。也可以 --cookie + --csrf 直接给。

所有请求均为读取（menu / form / data count / dashboard apps），不会写任何数据。
"""

import argparse
import gzip
import json
import os
import re
import sys
import threading
import time
import uuid
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib import error as urlerror
from urllib import request as urlrequest
from urllib.parse import parse_qs, urlparse

try:
    from zoneinfo import ZoneInfo

    TZ = ZoneInfo("Asia/Shanghai")
except Exception:  # pragma: no cover
    TZ = timezone(timedelta(hours=8))

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


BASE_DIR = Path(__file__).resolve().parent
APP_NAME = "ImplBoard"
LEGACY_APP_NAME = "DataBoard"


def _res_dir():
    """打包后静态资源在 PyInstaller 的解包目录里。"""
    return Path(getattr(sys, "_MEIPASS", str(BASE_DIR)))


def _data_dir():
    """可写目录：macOS 的 .app 内部只读，配置放用户目录。"""
    if sys.platform == "darwin":
        base = Path.home() / "Library" / "Application Support"
    elif os.name == "nt":
        base = Path(os.environ.get("APPDATA") or Path.home())
    else:
        base = Path(os.environ.get("XDG_CONFIG_HOME") or (Path.home() / ".config"))
    d = base / APP_NAME
    if not d.exists():
        old = base / LEGACY_APP_NAME
        if old.exists():
            return old
    d.mkdir(parents=True, exist_ok=True)
    return d


def _page_file():
    if getattr(sys, "_MEIPASS", None):
        return _res_dir() / "jdy" / "index.html"
    return BASE_DIR / "index.html"

REPORT_TTL = 60
TREND_TTL = 900
APPS_TTL = 600
DEFAULT_WORKERS = 16
REQUEST_TIMEOUT = 10
MAX_WORKERS = DEFAULT_WORKERS

DEFAULT_APP_ID = "6ab0c3281a10a54713c6abf5"
DEFAULT_HOST = "www.jiandaoyun.com"


# --------------------------------------------------------------------------- #
# cURL 解析
# --------------------------------------------------------------------------- #
def _strip_continuations(text: str) -> str:
    return re.sub(r"\\\s*\r?\n", " ", text)


def _headers(curl: str):
    out = {}
    for m in re.finditer(r"""(?:-H|--header)\s+(['"])(.*?)\1""", curl, re.S):
        raw = m.group(2)
        if ":" in raw:
            k, v = raw.split(":", 1)
            out[k.strip().lower()] = v.strip()
    return out


def _flag(curl: str, name: str):
    m = re.search(name + r"""\s+(['"])(.*?)\1""", curl, re.S)
    if m:
        return m.group(2)
    m = re.search(name + r"\s+(\S+)", curl)
    return m.group(1) if m else None


def parse_curl(text: str) -> dict:
    curl = _strip_continuations(text)
    headers = _headers(curl)

    cookie = headers.get("cookie") or _flag(curl, r"(?:-b|--cookie)") or ""
    csrf = headers.get("x-csrf-token", "")

    url = _flag(curl, "--url")
    if not url:
        m = re.search(r"""curl\s+(['"])(https?://[^'"]+)\1""", curl)
        url = m.group(2) if m else ""

    host = urlparse(url).netloc or DEFAULT_HOST
    scheme = urlparse(url).scheme or "https"

    app_id = None
    for pat in (
        r"/_/(?:admin/)?app/([a-f0-9]{16,32})/",
        r"/dashboard/app/([a-f0-9]{16,32})/",
        r"/dashboard#/app/([a-f0-9]{16,32})/",
    ):
        m = re.search(pat, url)
        if m:
            app_id = m.group(1)
            break
    if not app_id:
        m = re.search(r'"appId"\s*:\s*"([a-f0-9]{16,32})"', curl)
        if m:
            app_id = m.group(1)

    return {
        "host": host,
        "scheme": scheme,
        "cookie": cookie,
        "csrf": csrf,
        "app_id": app_id,
        "user_agent": headers.get("user-agent", ""),
        "jdy_ver": headers.get("x-jdy-ver", ""),
        "sessionshift_bridge": headers.get("x-sessionshift-bridge", ""),
    }


# --------------------------------------------------------------------------- #
# 只读 API 客户端
# --------------------------------------------------------------------------- #
class JdyClient:
    def __init__(self, host, scheme, cookie, csrf, extra=None):
        self.base = f"{scheme}://{host}"
        self.cookie = cookie
        self.csrf = csrf
        self.extra = extra or {}

    def _request(self, method: str, path: str, body=None, retries: int = 1):
        data = None if body is None else json.dumps(body).encode("utf-8")
        last = None
        for attempt in range(retries + 1):
            req = urlrequest.Request(self.base + path, data=data, method=method)
            req.add_header("accept", "application/json, text/plain, */*")
            req.add_header("x-requested-with", "XMLHttpRequest")
            req.add_header("origin", self.base)
            req.add_header("referer", f"{self.base}/dashboard")
            if data is not None:
                req.add_header("content-type", "application/json")
            if self.cookie:
                req.add_header("cookie", self.cookie)
            if self.csrf:
                req.add_header("x-csrf-token", self.csrf)
            for k, v in self.extra.items():
                if v:
                    req.add_header(k, v)
            try:
                with urlrequest.urlopen(req, timeout=REQUEST_TIMEOUT) as resp:
                    raw = resp.read()
                    if resp.headers.get("content-encoding") == "gzip":
                        raw = gzip.decompress(raw)
                    ctype = resp.headers.get("content-type", "")
                    if "json" not in ctype:
                        raise RuntimeError(f"非 JSON 响应（{ctype}），Cookie 或 csrf 可能已过期")
                    return json.loads(raw)
            except urlerror.HTTPError as e:
                try:
                    body_txt = e.read()[:100].decode("utf-8", "ignore")
                except Exception:
                    body_txt = ""
                last = RuntimeError(f"HTTP {e.code} {body_txt}".strip())
                if e.code in (401, 403):
                    break
            except Exception as e:  # noqa: BLE001
                last = RuntimeError(str(e))
            if attempt < retries:
                time.sleep(0.3)
        raise last or RuntimeError("请求失败")

    def post(self, path, body=None):
        return self._request("POST", path, body if body is not None else {})

    def get(self, path):
        return self._request("GET", path)

    # -- 业务 ------------------------------------------------------------- #
    def apps_raw(self):
        """应用列表：POST /dashboard/apps {"has_app_group": true}"""
        return self.post("/dashboard/apps", {"has_app_group": True})

    def tree(self):
        """应用树：POST /dashboard/api_doc/list_entry {"appId": ...}
        返回 {menu, entryList, entryMap}；失败则退回 /_/app/{appId}/menu。"""
        try:
            data = self.post("/dashboard/api_doc/list_entry", {"appId": self.app_id})
            if isinstance(data, dict) and data.get("entryMap"):
                return data
        except Exception:  # noqa: BLE001
            pass
        return {"entryMap": self.entry_map(), "menu": None, "entryList": None}

    def entry_map(self):
        return (self.post(f"/_/app/{self.app_id}/menu") or {}).get("entryMap") or {}

    def form_detail(self, entry_id):
        return self.post(f"/_/admin/app/{self.app_id}/form/{entry_id}")

    def count(self, entry_id, since=None, until=None) -> int:
        filters = []
        if since:
            filters.append({"field": "createTime", "method": "gte", "value": since})
        if until:
            filters.append({"field": "createTime", "method": "lt", "value": until})
        body = {"appId": self.app_id, "entryId": entry_id}
        if filters:
            body["filter"] = {"rel": "and", "cond": filters}
        try:
            resp = self.post("/_/data_process/admin/data/count", body) or {}
        except Exception:  # noqa: BLE001
            if filters:
                raise
            body["filter"] = {"rel": "and", "cond": []}
            resp = self.post("/_/data_process/admin/data/count", body) or {}
        return int(resp.get("count") or 0)


# --------------------------------------------------------------------------- #
# 汇总
# --------------------------------------------------------------------------- #
def localize(iso_utc):
    if not iso_utc:
        return None
    try:
        return datetime.fromisoformat(iso_utc.replace("Z", "+00:00")).astimezone(TZ)
    except Exception:  # noqa: BLE001
        return None


def humanize(delta: timedelta) -> str:
    secs = int(delta.total_seconds())
    if secs < 60:
        return "刚刚"
    if secs < 3600:
        return f"{secs // 60} 分钟前"
    if secs < 86400:
        return f"{secs // 3600} 小时前"
    return f"{secs // 86400} 天前"


PROGRESS = {}
PROGRESS_LOCK = threading.Lock()


def progress_set(app_id, **kw):
    with PROGRESS_LOCK:
        cur = PROGRESS.setdefault(app_id, {})
        cur.update(kw)
        cur["at"] = time.time()


def progress_log(app_id, line):
    with PROGRESS_LOCK:
        p = PROGRESS.setdefault(app_id, {})
        logs = p.setdefault("logs", [])
        logs.append({"t": datetime.now(TZ).strftime("%H:%M:%S"), "m": line})
        del logs[:-40]
        p["at"] = time.time()


def progress_get(app_id):
    with PROGRESS_LOCK:
        return dict(PROGRESS.get(app_id) or {"phase": "idle", "done": 0, "total": 0})


def flatten_tree(tree):
    """把 /dashboard/api_doc/list_entry 的 menu + entryMap 拍平成表单列表。"""
    em = tree.get("entryMap") or {}
    menu = tree.get("menu") or []
    forms = []

    def walk(nodes, ancestors):
        for node in nodes or []:
            meta = em.get(node.get("id")) or {}
            name = meta.get("name") or ""
            if meta.get("type") == "form":
                forms.append({
                    "entryId": node.get("id"),
                    "name": name,
                    "group": ancestors[-1] if ancestors else "",
                    "hasFlow": bool(meta.get("hasFlow")),
                })
            else:
                walk(node.get("child") or [], ancestors + [name] if name else ancestors)

    if menu:
        walk(menu, [])

    if not forms:  # 没有 menu 就用 parent 关系猜分组
        groups = {e.get("entryId"): e.get("name") for e in em.values() if e.get("type") == "group"}
        for e in em.values():
            if e.get("type") == "form":
                forms.append({
                    "entryId": e.get("entryId"),
                    "name": e.get("name") or "",
                    "group": groups.get(e.get("parent") or "", ""),
                    "hasFlow": bool(e.get("hasFlow")),
                })
    return forms, len([e for e in em.values() if e.get("type") == "group"])


def build_report(client: JdyClient, app_id: str, app_name: str = "", workers: int = 12,
                 day: str = "") -> dict:
    if not app_name:
        try:
            app_name = resolve_app_name(client, app_id)
        except Exception:  # noqa: BLE001
            app_name = ""
    now = datetime.now(TZ)
    today = now.strftime("%Y-%m-%d")
    day = day or today
    is_today = day == today
    since = f"{day} 00:00:00"
    next_since = (datetime.strptime(day, "%Y-%m-%d") + timedelta(days=1)).strftime("%Y-%m-%d") + " 00:00:00"
    pkey = f"{app_id}|{day}"

    t0 = time.time()
    tree = client.tree()
    entries, group_count = flatten_tree(tree)

    rows = []
    errors = []
    lock = threading.Lock()
    done = {"n": 0, "ok": 0}
    progress_set(pkey, phase="forms", done=0, total=len(entries), ok=0, logs=[],
                 started=t0, finished=0, error="", appName=app_name, date=day)
    progress_log(pkey, f"应用「{app_name or app_id}」{day}：{len(entries)} 张表单，并发 {workers}")

    def day_added(entry_id):
        """某日新增 = count(gte 当日) - count(gte 次日)；lt 过滤该接口不认，只能用差值。"""
        first = client.count(entry_id, since=since)
        if first <= 0 or is_today:
            return first
        return max(0, first - client.count(entry_id, since=next_since))

    active = {}

    def publish():
        progress_set(pkey, current=[f"{n} · {st}" for n, st in list(active.items())[:8]],
                     running=len(active))

    def one(entry):
        name = entry.get("name") or entry.get("entryId")
        with lock:
            active[name] = "结构"
            publish()
        try:
            ent = (client.form_detail(entry["entryId"]) or {}).get("entry") or {}
            with lock:
                active[name] = "总数"
                publish()
            total = client.count(entry["entryId"])
            with lock:
                active[name] = "新增"
                publish()
            added = day_added(entry["entryId"])
            row = {
                "entryId": entry["entryId"],
                "name": ent.get("name") or entry.get("name"),
                "group": entry.get("group") or "",
                "hasFlow": bool(entry.get("hasFlow")),
                "updateTime": ent.get("updateTime"),
                "items": len(((ent.get("content") or {}).get("items") or [])),
                "total": total,
                "count": added,
            }
            # 更新后新增：从表单最后更新那一刻到今天结束，又收了多少条
            dt0 = localize(row.get("updateTime"))
            if dt0 and dt0.strftime("%Y-%m-%d") == day:
                try:
                    v = client.count(entry["entryId"],
                                     since=dt0.strftime("%Y-%m-%d %H:%M:%S"))
                    if not is_today:
                        v = max(0, v - client.count(entry["entryId"], since=next_since))
                    row["afterUpdate"] = v
                except Exception:  # noqa: BLE001
                    row["afterUpdate"] = None
            with lock:
                rows.append(row)
                done["ok"] += 1
        except Exception as e:  # noqa: BLE001
            with lock:
                errors.append({"name": entry.get("name"), "entryId": entry.get("entryId"), "error": str(e)[:160]})
        finally:
            with lock:
                done["n"] += 1
                active.pop(name, None)
                progress_set(pkey, done=done["n"], ok=done["ok"], total=len(entries),
                             current=[f"{n} · {st}" for n, st in list(active.items())[:8]],
                             running=len(active))
                if done["n"] % 10 == 0 or done["n"] == len(entries):
                    progress_log(pkey, f"表单 {done['n']}/{len(entries)}")

    with ThreadPoolExecutor(max_workers=max(2, workers)) as pool:
        list(pool.map(one, entries))

    progress_set(pkey, phase="done", done=done["n"], ok=done["ok"], total=len(entries),
                 finished=time.time(), current=[], running=0)
    progress_log(pkey, f"表单读取完成：{done['n']} 张，成功 {done['ok']}，失败 {len(errors)}")

    for r in rows:
        dt = localize(r.get("updateTime"))
        r["updateLocal"] = dt.strftime("%H:%M:%S") if dt else ""
        r["updateAgo"] = humanize(now - dt) if dt else ""
        r["updatedOn"] = bool(dt and dt.strftime("%Y-%m-%d") == day)

    updated = sorted([r for r in rows if r["updatedOn"]], key=lambda x: x.get("updateTime") or "", reverse=True)
    with_data = sorted([r for r in rows if r["count"] > 0], key=lambda x: x["count"], reverse=True)

    group_rows = {}
    for r in rows:
        g = r["group"] or "未分组"
        item = group_rows.setdefault(g, {"name": g, "forms": 0, "updated": 0, "added": 0, "records": 0})
        item["forms"] += 1
        item["updated"] += 1 if r["updatedOn"] else 0
        item["added"] += 1 if r["count"] else 0
        item["records"] += r["count"]

    rows_sorted = sorted(rows, key=lambda r: (r["group"] or "未分组", -_ts(r.get("updateTime"))))

    return {
        "generatedAt": now.isoformat(),
        "date": day,
        "isToday": is_today,
        "appId": app_id,
        "appName": app_name,
        "host": urlparse(client.base).netloc,
        "totalForms": len(entries),
        "updatedCount": len(updated),
        "dataFormCount": len(with_data),
        "recordCount": sum(x["count"] for x in with_data),
        "totalRecords": sum(x["total"] for x in rows),
        "groupTotal": group_count,
        "durationMs": int((time.time() - t0) * 1000),
        "updated": updated,
        "withData": with_data,
        "forms": rows_sorted,
        "groups": sorted(group_rows.values(), key=lambda g: g["name"]),
        "errors": errors,
    }


def _ts(iso):
    dt = localize(iso)
    return dt.timestamp() if dt else 0


def build_trend(client: JdyClient, days: int = 7, only=None, end_day: str = "", pkey: str = "") -> dict:
    today = datetime.now(TZ).replace(hour=0, minute=0, second=0, microsecond=0)
    if end_day:
        today = datetime.strptime(end_day, "%Y-%m-%d").replace(tzinfo=TZ)
    starts = [(today - timedelta(days=days - 1 - i)) for i in range(days)]
    bounds = [d.strftime("%Y-%m-%d 00:00:00") for d in starts] + [
        (starts[-1] + timedelta(days=1)).strftime("%Y-%m-%d 00:00:00")
    ]

    entries = [e for e in client.entry_map().values() if e.get("type") == "form"]
    if only:
        wanted = set(only)
        entries = [e for e in entries if e.get("entryId") in wanted]
    totals = [0] * len(bounds)
    lock = threading.Lock()
    done = {"n": 0}
    active = {}

    if pkey:
        progress_set(pkey, phase="trend", done=0, total=len(entries), current=[])
        progress_log(pkey, f"开始统计近 {days} 日新增（{len(entries)} 张有数据的表单，每张 {len(bounds)} 次计数）")

    def one(entry):
        name = entry.get("name") or entry.get("entryId")
        with lock:
            active[name] = "趋势"
            if pkey:
                progress_set(pkey, current=[f"{n} · 趋势" for n in list(active)[:8]], running=len(active))
        try:
            vals = [client.count(entry["entryId"], since=b) for b in bounds]
        except Exception:  # noqa: BLE001
            vals = []
        with lock:
            for i, v in enumerate(vals):
                totals[i] += v
            active.pop(name, None)
            done["n"] += 1
            if pkey:
                progress_set(pkey, done=done["n"], current=[f"{n} · 趋势" for n in list(active)[:8]],
                             running=len(active))
                if done["n"] % 10 == 0 or done["n"] == len(entries):
                    progress_log(pkey, f"趋势 {done['n']}/{len(entries)} 张表单")

    with ThreadPoolExecutor(max_workers=MAX_WORKERS) as pool:
        list(pool.map(one, entries))

    series = []
    for i, d in enumerate(starts):
        series.append({"date": d.strftime("%Y-%m-%d"), "count": max(0, totals[i] - totals[i + 1])})
    return {"days": series, "end": starts[-1].strftime("%Y-%m-%d"), "generatedAt": datetime.now(TZ).isoformat()}


# --------------------------------------------------------------------------- #
# 应用列表
# --------------------------------------------------------------------------- #
NAME_KEYS = ("name", "appName", "showName", "displayName", "title", "label", "text")
NEST_KEYS = ("app", "appInfo", "info", "meta", "base", "data")


def _pick_name(node):
    """从任意对象里尽力取出可读名称。"""
    if not isinstance(node, dict):
        return ""
    for k in NAME_KEYS:
        v = node.get(k)
        if isinstance(v, str) and v.strip():
            return v.strip()
    for k in NEST_KEYS:
        v = node.get(k)
        if isinstance(v, dict):
            got = _pick_name(v)
            if got:
                return got
    return ""


def _walk_apps(node, out, depth=0):
    """把响应里所有 {appId, name} 收集起来；同一 appId 后出现的名字会补全前面的空名。"""
    if depth > 5 or not isinstance(node, (list, dict)):
        return
    if isinstance(node, list):
        for x in node:
            _walk_apps(x, out, depth + 1)
        return

    app_id = node.get("appId") or node.get("app_id") or node.get("_id")
    if isinstance(app_id, str) and re.fullmatch(r"[a-f0-9]{16,32}", app_id):
        name = _pick_name(node)
        hit = next((a for a in out if a["appId"] == app_id), None)
        if hit is None:
            out.append({"appId": app_id, "name": name})
        elif name and not hit["name"]:
            hit["name"] = name

    for v in node.values():
        if isinstance(v, (list, dict)):
            _walk_apps(v, out, depth + 1)


def resolve_app_name(client: JdyClient, app_id: str) -> str:
    """应用列表里没名字时，单独问一次应用本身。全部失败就返回空串。"""
    probes = (
        ("POST", f"/_/app/{app_id}/menu", {}),
        ("GET", f"/_/admin/app/{app_id}/info_get", None),
        ("POST", f"/_/admin/app/{app_id}/info_get", {}),
        ("GET", f"/_/app/{app_id}/info_get", None),
        ("POST", f"/_/app/{app_id}/get", {}),
    )
    for method, path, body in probes:
        try:
            data = client.post(path, body) if method == "POST" else client.get(path)
        except Exception:  # noqa: BLE001
            continue
        name = _pick_name(data)
        if name and "entryMap" not in str(data)[:200]:
            return name
    return ""


def fetch_apps(client: JdyClient, current_app_id: str):
    """应用列表：优先 POST /dashboard/apps；拿不到再退回一组老路径。"""
    apps = []

    def add(app_id, name):
        if not isinstance(app_id, str) or not re.fullmatch(r"[a-f0-9]{16,32}", app_id):
            return
        hit = next((a for a in apps if a["appId"] == app_id), None)
        if hit is None:
            apps.append({"appId": app_id, "name": name or ""})
        elif name and not hit["name"]:
            hit["name"] = name

    try:
        data = client.apps_raw()
        if isinstance(data, dict):
            for a in data.get("apps") or []:
                if isinstance(a, dict):
                    add(a.get("appId") or a.get("_id"), a.get("name") or a.get("appName") or "")
            for g in data.get("app_group_list") or []:
                if isinstance(g, dict):
                    for a in g.get("apps") or []:
                        if isinstance(a, dict):
                            add(a.get("appId") or a.get("_id"), a.get("name") or "")
    except Exception:  # noqa: BLE001
        pass

    if not apps:
        for method, path in (
            ("GET", "/_/dashboard/apps?withGroup=1"),
            ("GET", "/_/dashboard/apps"),
            ("GET", "/dashboard/apps"),
            ("GET", "/_/dashboard/spaces"),
            ("POST", "/_/dashboard/apps"),
            ("POST", "/_/dashboard/app_group"),
        ):
            try:
                data = client.get(path) if method == "GET" else client.post(path, {})
            except Exception:  # noqa: BLE001
                continue
            _walk_apps(data, apps)
            if any(a["name"] for a in apps):
                break

    if not apps:
        apps = [{"appId": current_app_id, "name": ""}]
    return apps


# --------------------------------------------------------------------------- #
# HTTP 服务
# --------------------------------------------------------------------------- #
class App:
    def __init__(self, client=None, default_app_id=DEFAULT_APP_ID, workers=DEFAULT_WORKERS):
        self.client = client
        self.workers = workers
        self.default_app_id = default_app_id
        self.lock = threading.Lock()
        self.reports = {}
        self.trends = {}
        self.keylocks = {}
        self.apps_cache = None
        self.apps_at = 0.0

    # -- 认证 ------------------------------------------------------------- #
    def _activate(self, client, app_id):
        with self.lock:
            self.client = client
            self.default_app_id = app_id
            self.reports.clear()
            self.trends.clear()
            self.apps_cache = None

    def set_auth(self, cookie, csrf, host, scheme, app_id, extra=None):
        """验证并保存为一个凭据 profile（同 host+appId 视为同一个，覆盖）。"""
        client = JdyClient(host, scheme, cookie, csrf, extra or {})
        client.app_id = app_id
        client.entry_map()  # 立刻验证一次，失败会抛异常

        store = load_store()
        now = int(time.time())
        prof = next((p for p in store["profiles"]
                     if p.get("host") == host and p.get("app_id") == app_id), None)
        if prof is None:
            prof = {"id": "p" + uuid.uuid4().hex[:8], "createdAt": now}
            store["profiles"].append(prof)
        prof.update({
            "name": prof.get("name") or ("飞书 " if host.startswith("lark.") else "") + host,
            "host": host, "scheme": scheme, "app_id": app_id,
            "cookie": cookie, "csrf": csrf,
            "user_agent": (extra or {}).get("user-agent", ""),
            "jdy_ver": (extra or {}).get("x-jdy-ver", ""),
            "updatedAt": now,
        })
        store["active"] = prof["id"]
        try:
            save_store(store)
        except Exception:  # noqa: BLE001
            pass
        self._activate(client, app_id)
        return client

    def profiles(self):
        store = load_store()
        return [public_profile(p, store.get("active") or "") for p in store["profiles"]]

    def profile_by_id(self, pid):
        return next((p for p in load_store()["profiles"] if p["id"] == pid), None)

    def switch(self, pid):
        store = load_store()
        prof = next((p for p in store["profiles"] if p["id"] == pid), None)
        if not prof:
            raise RuntimeError("没有这个凭据")
        client = JdyClient(prof["host"], prof.get("scheme", "https"), prof["cookie"], prof.get("csrf", ""),
                           {"user-agent": prof.get("user_agent", ""), "x-jdy-ver": prof.get("jdy_ver", "")})
        client.app_id = prof.get("app_id") or DEFAULT_APP_ID
        client.entry_map()  # 验证
        store["active"] = pid
        try:
            save_store(store)
        except Exception:  # noqa: BLE001
            pass
        self._activate(client, client.app_id)
        return public_profile(prof, pid)

    def delete_profile(self, pid):
        store = load_store()
        n = len(store["profiles"])
        store["profiles"] = [p for p in store["profiles"] if p["id"] != pid]
        if len(store["profiles"]) == n:
            raise RuntimeError("没有这个凭据")
        if store.get("active") == pid:
            store["active"] = ""
            with self.lock:
                self.client = None
                self.reports.clear()
                self.trends.clear()
                self.apps_cache = None
        try:
            save_store(store)
        except Exception:  # noqa: BLE001
            pass
        return True

    def ensure(self):
        if not self.client:
            raise PermissionError("未认证")
        return self.client

    def client_for(self, app_id: str) -> JdyClient:
        base = self.ensure()
        if app_id == base.app_id:
            return base
        c = JdyClient(
            urlparse(base.base).netloc,
            urlparse(base.base).scheme,
            base.cookie,
            base.csrf,
            base.extra,
        )
        c.app_id = app_id
        return c

    def apps(self):
        self.ensure()
        with self.lock:
            if self.apps_cache and time.time() - self.apps_at < APPS_TTL:
                return self.apps_cache
        data = fetch_apps(self.client, self.default_app_id)
        todo = [a for a in data if not a.get("name")]
        if todo and len(todo) <= 12:
            with ThreadPoolExecutor(max_workers=6) as pool:
                names = list(pool.map(lambda x: resolve_app_name(self.client, x["appId"]), todo))
            for a, name in zip(todo, names):
                a["name"] = name
        with self.lock:
            self.apps_cache, self.apps_at = data, time.time()
        return data

    def keylock(self, key):
        with self.lock:
            lk = self.keylocks.get(key)
            if lk is None:
                lk = threading.Lock()
                self.keylocks[key] = lk
            return lk

    def report(self, app_id: str, fresh=False, day=""):
        self.ensure()
        day = day or datetime.now(TZ).strftime("%Y-%m-%d")
        key = (app_id, day)
        with self.lock:
            hit = self.reports.get(key)
            if hit and not fresh and time.time() - hit[1] < REPORT_TTL:
                return hit[0]
        # 同一 app+日期 只允许一个在跑，避免重复请求把进度搅乱
        with self.keylock(("report", app_id, day)):
            with self.lock:
                hit = self.reports.get(key)
                if hit and not fresh and time.time() - hit[1] < REPORT_TTL:
                    return hit[0]
            name = ""
            try:
                for a in self.apps():
                    if a["appId"] == app_id:
                        name = a.get("name") or ""
                        break
            except Exception:  # noqa: BLE001
                pass
            data = build_report(self.client_for(app_id), app_id, name, self.workers, day)
            with self.lock:
                self.reports[key] = (data, time.time())
            return data

    def trend(self, app_id: str, fresh=False, only=None, end_day=""):
        self.ensure()
        end_day = end_day or datetime.now(TZ).strftime("%Y-%m-%d")
        key = (app_id, end_day)
        with self.lock:
            hit = self.trends.get(key)
            if hit and not fresh and time.time() - hit[1] < TREND_TTL:
                return hit[0]
        with self.keylock(("trend", app_id, end_day)):
            with self.lock:
                hit = self.trends.get(key)
                if hit and not fresh and time.time() - hit[1] < TREND_TTL:
                    return hit[0]
            data = build_trend(self.client_for(app_id), only=only, end_day=end_day,
                               pkey=f"{app_id}|{end_day}")
            with self.lock:
                self.trends[key] = (data, time.time())
            return data


APP = None


class Handler(BaseHTTPRequestHandler):
    server_version = "JDYBoard/1.1"
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass

    def _send(self, payload: bytes, ctype: str, code: int = 200):
        self.send_response(code)
        self.send_header("content-type", ctype)
        self.send_header("content-length", str(len(payload)))
        self.send_header("cache-control", "no-store")
        self.end_headers()
        self.wfile.write(payload)

    def _json(self, obj, code=200):
        self._send(json.dumps(obj, ensure_ascii=False).encode("utf-8"), "application/json; charset=utf-8", code)

    def _read_body(self):
        try:
            n = int(self.headers.get("content-length") or 0)
        except ValueError:
            n = 0
        return self.rfile.read(n) if n else b""

    def do_POST(self):  # noqa: N802
        path = urlparse(self.path).path
        raw = self._read_body()

        if path == "/api/auth":
            return self._auth(raw)
        if path == "/api/auth/switch":
            return self._switch(raw)
        if path == "/api/auth/delete":
            return self._delete(raw)

        self._json({"error": "not found"}, 404)

    def _switch(self, raw):
        try:
            pid = (json.loads(raw or b"{}") or {}).get("id") or ""
        except Exception:  # noqa: BLE001
            pid = ""
        if not pid:
            return self._json({"error": "缺少 id"}, 400)
        try:
            prof = APP.switch(pid)
        except Exception as e:  # noqa: BLE001
            return self._json({"error": f"切换失败：{e}"}, 400)
        return self._json({"ok": True, "profile": prof})

    def _delete(self, raw):
        try:
            pid = (json.loads(raw or b"{}") or {}).get("id") or ""
        except Exception:  # noqa: BLE001
            pid = ""
        if not pid:
            return self._json({"error": "缺少 id"}, 400)
        try:
            APP.delete_profile(pid)
        except Exception as e:  # noqa: BLE001
            return self._json({"error": str(e)}, 400)
        return self._json({"ok": True, "auth": APP.client is not None, "profiles": APP.profiles()})

    def _auth(self, raw: bytes):
        text = raw.decode("utf-8", "ignore").strip()
        payload = {}
        if text.startswith("{"):
            try:
                payload = json.loads(text)
            except Exception:  # noqa: BLE001
                payload = {}
        curl = payload.get("curl") or ("" if payload else text)

        cookie = payload.get("cookie") or ""
        csrf = payload.get("csrf") or ""
        app_id = payload.get("appId") or ""
        host = payload.get("host") or DEFAULT_HOST
        scheme = payload.get("scheme") or "https"
        extra = {}

        if curl:
            try:
                info = parse_curl(curl)
            except Exception as e:  # noqa: BLE001
                return self._json({"error": f"cURL 解析失败：{e}"}, 400)
            cookie = cookie or info["cookie"]
            csrf = csrf or info["csrf"]
            app_id = app_id or info["app_id"] or ""
            host = info["host"] or host
            scheme = info["scheme"] or scheme
            extra = {"user-agent": info.get("user_agent"), "x-jdy-ver": info.get("jdy_ver")}

        if not cookie:
            return self._json({"error": "没找到 Cookie：请粘贴完整的 cURL（含 -H 'cookie: ...'）"}, 400)

        app_id = app_id or APP.default_app_id
        if not re.fullmatch(r"[a-f0-9]{16,32}", app_id or ""):
            return self._json({"error": "appId 不合法，请在 cURL 里带上 /_/admin/app/{appId}/ 路径或手动指定"}, 400)

        try:
            APP.set_auth(cookie, csrf, host, scheme, app_id, extra)
        except Exception as e:  # noqa: BLE001
            return self._json({"error": f"验证失败：{e}"}, 400)

        name = (payload.get("name") or "").strip()
        if name:
            try:
                store = load_store()
                for prof in store["profiles"]:
                    if prof.get("host") == host and prof.get("app_id") == app_id:
                        prof["name"] = name
                save_store(store)
            except Exception:  # noqa: BLE001
                pass

        return self._json({"ok": True, "appId": app_id})

    def do_GET(self):  # noqa: N802
        parsed = urlparse(self.path)
        path = parsed.path
        qs = parse_qs(parsed.query)

        if path in ("/", "/index.html"):
            return self._send(_page_file().read_bytes(), "text/html; charset=utf-8")
        if path == "/api/status":
            profs = APP.profiles()
            active = next((x for x in profs if x["active"]), None)
            return self._json({"auth": APP.client is not None, "appId": APP.default_app_id,
                               "active": active, "profiles": len(profs)})
        if path == "/api/auth":
            profs = APP.profiles()
            return self._json({"auth": APP.client is not None, "profiles": profs,
                               "active": next((x for x in profs if x["active"]), None)})
        if path == "/favicon.ico":
            return self._send(b"", "image/x-icon", 204)

        app_id = (qs.get("appId") or [APP.default_app_id])[0]
        fresh = (qs.get("fresh") or ["0"])[0] == "1"

        if APP.client is None:
            return self._json({"error": "未认证", "needAuth": True}, 401)
        if not re.fullmatch(r"[a-f0-9]{16,32}", app_id or ""):
            return self._json({"error": "appId 不合法"}, 400)

        day = (qs.get("date") or [""])[0]
        if day and not re.fullmatch(r"\d{4}-\d{2}-\d{2}", day):
            return self._json({"error": "date 需为 YYYY-MM-DD"}, 400)

        if path == "/api/progress":
            return self._json(progress_get(app_id + "|" + (day or datetime.now(TZ).strftime("%Y-%m-%d"))))

        try:
            if path == "/api/apps":
                return self._json({"apps": APP.apps(), "current": app_id})
            if path == "/api/report":
                rep = APP.report(app_id, fresh, day)
                if (qs.get("trend") or ["1"])[0] != "0":
                    ids = [r["entryId"] for r in rep["forms"] if (r.get("total") or 0) > 0]
                    if ids:
                        pkey = f"{app_id}|{rep['date']}"
                        try:
                            rep = dict(rep)
                            rep["trend"] = APP.trend(app_id, fresh, ids, rep["date"])
                        except Exception as e:  # noqa: BLE001
                            rep["trend"] = None
                            progress_log(pkey, f"趋势统计失败：{e}")
                    else:
                        rep = dict(rep)
                        rep["trend"] = {"days": [], "end": rep["date"], "generatedAt": ""}
                progress_set(f"{app_id}|{rep['date']}", phase="done", current=[], running=0,
                             finished=time.time())
                progress_log(f"{app_id}|{rep['date']}", f"完成：用时 {rep.get('durationMs', 0) / 1000:.1f}s")
                return self._json(rep)
            if path == "/api/trend":
                only = [x for x in (qs.get("only") or [""])[0].split(",") if x]
                return self._json(APP.trend(app_id, fresh, only or None, day))
        except PermissionError:
            return self._json({"error": "未认证", "needAuth": True}, 401)
        except Exception as e:  # noqa: BLE001
            return self._json({"error": str(e)}, 503)

        return self._send(b"not found", "text/plain; charset=utf-8", 404)


# --------------------------------------------------------------------------- #
# 入口
# --------------------------------------------------------------------------- #
AUTH_FILE = Path(os.environ.get("JDY_AUTH") or (_data_dir() / "jdy_auth.json"))


def load_store():
    """{"active": id, "profiles": [{id,name,host,scheme,app_id,cookie,csrf,...}]}"""
    if not AUTH_FILE.exists():
        return {"active": "", "profiles": []}
    try:
        raw = json.loads(AUTH_FILE.read_text(encoding="utf-8"))
    except Exception:  # noqa: BLE001
        return {"active": "", "profiles": []}
    if isinstance(raw.get("profiles"), list):
        return {"active": raw.get("active") or "", "profiles": raw["profiles"]}
    if raw.get("cookie"):  # 旧版单条格式，迁移成一个 profile
        return {"active": "p1", "profiles": [{
            "id": "p1", "name": raw.get("host") or "默认",
            "host": raw.get("host") or DEFAULT_HOST, "scheme": raw.get("scheme") or "https",
            "app_id": raw.get("app_id") or DEFAULT_APP_ID, "cookie": raw["cookie"],
            "csrf": raw.get("csrf", ""), "user_agent": raw.get("user_agent", ""),
            "jdy_ver": raw.get("jdy_ver", ""), "createdAt": int(time.time()),
        }]}
    return {"active": "", "profiles": []}


def save_store(store):
    tmp = AUTH_FILE.with_suffix(".tmp")
    tmp.write_text(json.dumps(store, ensure_ascii=False, indent=2), encoding="utf-8")
    tmp.chmod(0o600)
    tmp.replace(AUTH_FILE)


def public_profile(p, active_id=""):
    return {
        "id": p["id"],
        "name": p.get("name") or p.get("host") or "未命名",
        "host": p.get("host", ""),
        "appId": p.get("app_id", ""),
        "env": "飞书" if (p.get("host") or "").startswith("lark.") else "简道云",
        "updatedAt": int(p.get("updatedAt") or p.get("createdAt") or 0),
        "active": p["id"] == active_id,
    }


def start(port=0, workers=DEFAULT_WORKERS):
    """起服务并返回 server，供桌面外壳调用（端口 0 = 自动挑一个空闲的）。"""
    global APP
    APP = App(None, DEFAULT_APP_ID, workers)
    store = load_store()
    active = next((x for x in store["profiles"] if x["id"] == store.get("active")), None)
    if active:
        try:
            APP.switch(active["id"])
        except Exception:  # noqa: BLE001
            pass
    httpd = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    httpd.daemon_threads = True
    return httpd


def main():
    ap = argparse.ArgumentParser(description="简道云 · 表单看板（只读）")
    ap.add_argument("--curl", help="浏览器复制的 cURL 文件路径")
    ap.add_argument("--cookie", help="Cookie 字符串")
    ap.add_argument("--csrf", help="x-csrf-token")
    ap.add_argument("--app-id", default=None, help="默认应用 ID")
    ap.add_argument("--host", default=None, help="域名，可带 http(s):// 前缀")
    ap.add_argument("--scheme", default=None, choices=["http", "https"])
    ap.add_argument("--port", type=int, default=8787)
    ap.add_argument("--workers", type=int, default=DEFAULT_WORKERS, help="并发数（默认 12）")
    args = ap.parse_args()

    store = load_store()
    cookie = args.cookie or ""
    csrf = args.csrf or ""
    app_id = args.app_id or ""
    host = args.host or ""
    scheme = args.scheme or "https"
    extra = {}

    if args.curl:
        info = parse_curl(Path(args.curl).expanduser().read_text(encoding="utf-8", errors="ignore"))
        cookie = cookie or info["cookie"]
        csrf = csrf or info["csrf"]
        app_id = app_id or info["app_id"] or ""
        host = host or info["host"]
        scheme = args.scheme or info["scheme"]
        extra = {"user-agent": info.get("user_agent", ""), "x-jdy-ver": info.get("jdy_ver", "")}

    if host.startswith(("http://", "https://")):
        scheme = "https" if host.startswith("https://") else "http"
        host = urlparse(host).netloc

    global APP
    APP = App(None, app_id or DEFAULT_APP_ID, args.workers)

    if cookie:
        try:
            APP.set_auth(cookie, csrf, host or DEFAULT_HOST, scheme, app_id or DEFAULT_APP_ID, extra)
            print(f"[i] 已用命令行凭据验证通过（{host or DEFAULT_HOST}）")
        except Exception as e:  # noqa: BLE001
            print(f"[!] 命令行凭据不可用（{e}），请在页面上重新粘贴 cURL")
    else:
        active = next((x for x in store["profiles"] if x["id"] == store.get("active")), None)
        if active:
            try:
                APP.switch(active["id"])
                print(f"[i] 已载入凭据「{active.get('name') or active.get('host')}」（{active.get('host')}）")
            except Exception as e:  # noqa: BLE001
                print(f"[!] 已保存的凭据不可用（{e}），请在页面上重新粘贴 cURL")
        elif store["profiles"]:
            print("[i] 有保存的凭据但未选中，请在页面上切换")

    httpd = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    print(f"[√] 打开 http://127.0.0.1:{args.port}")
    if APP.client is None:
        print("[i] 还没有登录态：打开页面后把浏览器复制的 cURL 粘进输入框即可")
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\n[√] 已退出")


if __name__ == "__main__":
    main()
