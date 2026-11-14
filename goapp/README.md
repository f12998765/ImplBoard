# 实施看板 · Go 版（ImplBoard · Go）

Python 版（`../board`、`../jdy-today`、`../qflow-today`）的 Go 重写：同一个进程里跑三个本地只读服务，用 mygo 的 webview 开窗口，打包成单个 `.app`。

## 结构

```
goapp/
├── main.go                 # 三个 127.0.0.1 服务 + 窗口
├── mygo.json               # 应用名 / 标识符 / 版本
├── assets/                 # 前端（jdy.html / qf.html / home.html / icons）
├── internal/jdy/           # 简道云采集与 /api/*
├── internal/qf/            # 轻流采集与 /api/*
├── resources/icon.png      # 1024×1024 图标（tools/make_icon.py 生成）
└── tools/make_icon.py      # 画图标：superellipse 圆角 + 渐变 + 柱子
```

三个服务的分工与 Python 版一致：主页（`/`）、简道云（`/` + `/api/*`）、轻流（`/` + `/api/*`），端口都是随机空闲端口，互相之间用 `?home=` 参数回主页。

## 开发

```bash
go run .        # 直接跑，窗口会起来
```

凭据路径：`JDY_AUTH` / `QF_AUTH` 环境变量，或放在 `~/Library/Application Support/ImplBoard/`（旧的 `DataBoard/` 目录会自动沿用，所以从 Python 版升级不用重新登录）。

## 打包

```bash
go tool mygo build -skip-notarize -skip-dmg     # build/darwin-arm64/实施看板.app
go tool mygo build -skip-notarize               # 额外打一个 .dmg
go tool mygo build -platform windows/amd64,windows/arm64 -skip-notarize   # 交叉编译 Windows
```

不签名（ad hoc）时，别人第一次打开要右键 →「打开」，或者跑一下 `去隔离标记.command`。要免这一步得用 Developer ID 证书 + 公证。

## 与 Python 版的差异

- 启动与查询都由 Go 直接发请求，不再需要 Python 运行时；`/api/*` 的 JSON 字段与 Python 版一致，前端没有改动。
- 并发抓取在原逻辑上保留，实测简道云 41 张表单约 5 秒、轻流 116 张约 10 秒。
- 只读：只调用查询类接口，没有任何写操作。
