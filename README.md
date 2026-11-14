# 实施看板 · ImplBoard

> 简道云 / 轻流 两个平台的表单与数据「当日看板」——本地运行、只读查询、可打包成桌面应用。

## 它能做什么

- **当日有更新的表单**：逐张列出最后更新时间、以及「更新后新增数据量」（从这次更新到当日 23:59 新收到的记录数）。
- **当日有数据添加的表单**：按新增条数倒序，带总条数与占比条。
- **5 个指标卡**：应用内表单 / 当日更新（发布）/ 当日有数据添加 / 当日新增记录 / 更新后新增数据量。
- **3 张图**：近 7 日新增记录、当日更新时段（24 小时面积折线）、更新后新增占当日新增（环形）。
- **缓存与新鲜度**：报告按「应用包 + 日期」缓存在本地，切日期不再重复请求；1 小时内标「实时中」，超时标「已过期 · N 小时前」。
- **全部请求只读**：只调用查询接口，没有任何发布、保存、删除。

## 目录结构

```
ImplBoard/
├── board/                     # 桌面外壳（一个主页 + 两个看板）
│   ├── app.py                 # 起三个本地 HTTP 服务，用 pywebview 开窗口
│   ├── home.html              # 主页：选平台进入
│   ├── icons/                 # 两个平台图标
│   ├── ImplBoard.spec         # PyInstaller 打包配置（macOS）
│   ├── build-windows.bat      # Windows 一键构建
│   └── workflow-build-windows.yml  # GitHub Actions 构建 Windows 包
├── jdy-today/                 # 简道云看板（index.html + 采集服务）
└── qflow-today/               # 轻流看板（index.html + 采集服务）
```

## 快速开始（源码运行）

两个看板各自是一个自带 HTTP 服务的单文件页面，前端只认 `/api/report`、`/api/apps` 这类接口。

```bash
# 轻流（默认 8899）
QF_AUTH=./qflow-today/qf_auth.json python3 qflow-today/qf_server.py --port 8899

# 简道云（默认 8898）
JDY_AUTH=./jdy-today/jdy_auth.json python3 jdy-today/jdy_today_server.py --port 8898
```

浏览器打开 `http://127.0.0.1:8898/`（简道云）或 `http://127.0.0.1:8899/`（轻流）。

### 凭据

- 浏览器里登录对应平台，复制一份请求的 Cookie / token，存成 JSON：
  - 简道云：`{"cookie": "...", "csrf": "..."}`
  - 轻流：`{"token": "...", "wsid": "..."}`
- 也可以用环境变量 `JDY_AUTH` / `QF_AUTH` 指到任意路径的 JSON。
- 默认读取 `~/Library/Application Support/ImplBoard/`（旧版目录 `DataBoard` 仍兼容）。

> 凭据只存在本机，`.gitignore` 已排除 `*_auth.json`，不要提交。

## 打包成桌面应用

**macOS**

```bash
cd board
pyinstaller ImplBoard.spec --noconfirm --distpath dist --workpath build
mv dist/ImplBoard.app "dist/实施看板.app"
/usr/libexec/PlistBuddy -c "Set :CFBundleDisplayName 实施看板" "dist/实施看板.app/Contents/Info.plist"
codesign --force --deep --sign - "dist/实施看板.app"
```

**Windows**：在 `board/` 下双击 `build-windows.bat`，产物是 `dist\ImplBoard\ImplBoard.exe`（整个文件夹一起拷贝）。
也可以直接用仓库里的 GitHub Actions 工作流产出 Windows 包。

## 实现要点

- 采集端只用 Python 标准库（`urllib` + `ThreadPoolExecutor`），无第三方依赖；桌面壳用 `pywebview`。
- 「更新后新增数据量」= 表单 `updateTime` 之后到当日 23:59 的记录数，用一次 `count` 接口算差值。
- 前端零框架（原生 JS + 内联样式），两页共用同一套 CSS，方便保持一致。

## 截图

截图含真实表单名与记录条数，未放入公开仓库，需要时单独提供。

## 说明

- 两个平台的内部接口并非公开 API，字段与口径可能变化；本项目只做只读聚合，不保证接口长期稳定。
- 仅供内部使用，请遵守所在平台的使用条款。
