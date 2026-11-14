# 轻流看板（只读）

全部应用包一次查完，按应用包分组列表单：**最后发布时间 · 总条数 · 当日新增 · 当日是否发布**。
应用包等同于简道云的分组，不再做切换筛选。

只调用读取接口，没有任何发布 / 保存 / 删除：

| 用途 | 接口 |
| --- | --- |
| 应用包 / 应用树 | `GET /api/tag/apps` |
| 记录数（可按天） | `POST /api/app/{appKey}/apply/filter/amount` |
| 最后发布时间 | `GET /api/app/{appKey}/publish` |

## 启动

```bash
cd qflow-today
QF_TOKEN=<prodtoken> QF_WSID=690763 QF_VERSION=mongo1 python3 qf_server.py --port 8788
```

打开 http://127.0.0.1:8788 。

也可以在页面右上角「账号与令牌」里**粘贴 cURL**（轻流页面 F12 → Network → 任意请求 → Copy as cURL），
后端会解析出 `-H 'token:'`、`-H 'wsid:'`，以及 cookie 里的 `prodtoken` / `prodwsId` / `qfVersion`，
账号存 `qf_auth.json`（600），支持添加 / 切换 / 删除。

## 页面结构（与简道云版一致）

- 顶栏：品牌 +「全部应用包 · N 个」/ 日期 / 生成时间 / 实时；右侧 `账号`、日期、`回到今天`、`导出`、`刷新`
- 四个指标：应用内表单 · 当日有数据添加的表单 · 当日发布的表单 · 当日新增记录
- 两张图：近 7 日新增记录、当日发布时段（张/小时）
- 两个列表：**当日发布的表单**（按发布时间倒序）、**当日有数据添加的表单**（按新增条数倒序）
- 全部表单 · 按应用包：分组行可折叠，列 = 表单 / 最后发布 / 总条数 / 当日新增

## 指标口径

| 指标 | 口径 |
| --- | --- |
| 表单数 | 去重后的应用数（同一表单在多个包里只算一次） |
| 总条数 | `filter/amount` + `type: 8` 的 `resultAmount` |
| 当日新增 | 同接口加 `queries: [{queId: 2, scope: 1, minValue: D, maxValue: D}]` |
| 当日有数据添加的表单 | `当日新增 > 0` 的表单个数 |
| 当日发布的表单 | `lastPublishTime` 落在所选日期的表单个数 |
| 最后发布 | `/app/{key}/publish` 的 `lastPublishTime`（设计发布，不是数据变更） |
| 时段分布 | 当日发布的那些表单，按 `lastPublishTime` 的小时分桶（0–23） |

`queId: 2` = 申请时间（系统字段，类型 `4`）；`scope: 1` = 属于。日期格式 `YYYY-MM-DD`，闭区间。

## 实测（2026-11-13）

- 11 个应用包 / 116 个表单 / 56,181 条记录
- 当日新增 1,363 条，分布在 40 个表单
- 当日发布 21 个表单：9 点 1 个、10 点 6 个、11 点 7 个、14 点 6 个、17 点 1 个
- 首次冷查约 20–45s；同进程内换日期 1.8s（总数 / 发布 / 按天计数分别缓存 5 分钟）

## 请求量

每个表单：1 次总数 + 1 次当日 + 1 次发布 = 3 次；7 日趋势再 +7 次。
116 个表单冷启动约 1,160 次请求，并发 16（`QF_WORKERS` 可调），超时 8s，超时自动重试 1 次。

## 接口备忘（实测）

```
POST /api/app/{key}/apply/filter/amount
     headers: token, wsid, NewInterfaceSkipHeader: "", Cookie: qfVersion + prodtoken
     body: {"pageNum":1,"pageSize":1,"type":8,
            "queries":[{"queId":2,"scope":1,"minValue":"2026-11-13","maxValue":"2026-11-13"}]}
     -> {"code":0,"data":{"resultAmount":1363,"todoAmount":null}}

GET /api/app/{key}/publish
     -> {"data":{"lastPublishTime":1794540571000,"lastPublishUser":{...}}}
```

`type`: `5` 待处理，`8` 全部（含已结束）。不带 `type` 会返回 40027。

## 打包成桌面应用

已打包好，`dist/` 下：

| 文件 | 说明 |
| --- | --- |
| `轻流看板.app` | 直接双击运行（Apple 芯片） |
| `轻流看板-macOS-arm64.zip` | 发给别人的压缩包，含使用说明 |
| `使用说明.txt` | 给使用者的操作步骤 |

外壳是 `qf_app.py`：找一个空闲端口起 `qf_server.start(port)`，再用 pywebview
开一个系统 WebView 窗口指向它，窗口关掉进程就退出。

重新打包（macOS）：

```bash
python3 -m venv .venv-build
.venv-build/bin/pip install pyinstaller pywebview
cd qflow-today
PYINSTALLER_CONFIG_DIR=$PWD/../.picache ../.venv-build/bin/pyinstaller \
  --noconfirm --clean --windowed --name QingFlowBoard --onedir \
  --distpath ./dist --workpath ./build \
  --add-data "index.html:." \
  --hidden-import webview.platforms.cocoa --collect-submodules webview \
  qf_app.py
```

打包后配置写在 `~/Library/Application Support/QingFlowBoard/qf_auth.json`
（`.app` 内部是只读的，不能写在程序旁边），可用 `QF_AUTH` 环境变量覆盖。
