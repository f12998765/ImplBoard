# 实施看板 · ImplBoard

> 简道云 / 轻流 两个平台的表单与数据「当日看板」——本地运行、只读查询，打包成 macOS / Windows / Linux 桌面应用和 Android APK。

## 它能做什么

- **当日有更新（发布）的表单**：逐张列出最后更新时间、以及「更新后新增数据量」（从这次更新到当日 23:59 新收到的记录数）。
- **当日有数据添加的表单**：按新增条数倒序，带总条数与占比条。
- **5 个指标卡**：应用内表单 / 当日更新（发布）/ 当日有数据添加 / 当日新增记录 / 更新后新增数据量。
- **3 张图**：近 7 日新增记录、当日更新时段（24 小时面积折线）、更新后新增占当日新增（环形）。
- **缓存与新鲜度**：报告按「应用包 + 日期」缓存在本机，切日期不再重复请求；1 小时内标「实时中」，超时标「已过期 · N 小时前」。
- **手机可用**：窄屏下工具条只留「日期 + 应用包 + ⋯」，其余操作收进菜单；表格自动精简列。
- **全部请求只读**：只调用查询接口，没有任何发布、保存、删除。

## 下载

到 [Releases](https://github.com/f12998765/ImplBoard/releases) 取最新版本（当前 `0.0.2`）。

| 平台 | 文件 |
| --- | --- |
| macOS（Apple 芯片） | `implboard-<版本>-arm64.dmg` |
| macOS（Intel） | `implboard-<版本>-amd64.dmg` |
| Windows x64 / ARM64 | `implboard-setup-<版本>-<arch>.exe`（安装包）、`implboard-<版本>-windows-<arch>.exe`（便携版） |
| Linux x64 / ARM64 | `implboard-<版本>-linux-<arch>.tar.gz`、`.deb`、`.rpm` |
| Android（arm64 / x86_64） | `implboard-<版本>-android.apk` |

安装注意事项：

- **macOS**：ad-hoc 签名、未公证。首次打开右键 →「打开」，或 `xattr -dr com.apple.quarantine "/Applications/实施看板.app"`。
- **Windows**：安装包未签名，SmartScreen 提示时选「更多信息 → 仍要运行」。
- **Linux**：`tar.gz` 解压后运行 `./install.sh` 装到 `~/.local`（可自更新），也可以直接装 `.deb` / `.rpm`。
- **Android**：APK 未上架应用商店，需要允许「安装未知来源应用」。

## 首次使用

1. 浏览器登录对应平台，复制一份请求里的凭证：
   - 简道云：Cookie 与 `x-csrf-token`
   - 轻流：`token` 与 `wsid`
2. 打开应用 →「账号」粘贴保存。凭证只存在本机，`~/Library/Application Support/ImplBoard/`（Windows 为 `%APPDATA%\ImplBoard`）。
3. 选日期与应用包（简道云）后点「刷新」；数据按「应用包 + 日期」缓存在 localStorage，再进同一个日期不会重复请求。

## 本地开发

页面是三个单文件 HTML（主页 / 简道云 / 轻流），Go 侧起三个本地端口把它们和查询接口一起serve出去，桌面端用 [mygo](https://github.com/egoist/mygo) 开原生窗口，Android 端用 WebView + gomobile 绑定同一份 Go 服务。

```bash
cd goapp
go run .                                  # 直接开窗口
go tool mygo build -skip-notarize         # 打本机平台的包，产物在 goapp/build/
```

版本号写在 `goapp/mygo.json` 的 `version`，产物文件名都带它。

Android 包需要 Android SDK / NDK 与 Gradle，只在 CI 里构建（见下）。

## 目录结构

```
ImplBoard/
├── goapp/                     # Go 实现（桌面 + 移动共用）
│   ├── assets/                # home.html / jdy.html / qf.html 与图标
│   ├── internal/boot/         # 起三个本地端口、内嵌页面、汇总主页
│   ├── internal/jdy/          # 简道云只读查询（应用列表、表单结构、count）
│   ├── internal/qf/           # 轻流只读查询（应用树、记录数、最后发布时间）
│   ├── mobile/                # gomobile 绑定入口（供 Android 调用）
│   └── mygo.json              # 应用名 / slug / 版本号 / 打包配置
├── android/                   # Android 壳工程（WebView + 内嵌 Go 服务）
└── board/ jdy-today/ qflow-today/   # 早期 Python 实现，仅作参考，不再更新
```

## 打包与发布

`.github/workflows/release.yml` 在 `goapp/`、`android/` 有改动推到 `main` 时（或手动触发）自动执行：

1. 读 `goapp/mygo.json` 的 `version`；
2. 四个 runner 并行打包：macOS（dmg）、Windows（NSIS 安装包 + 便携版）、Linux（tar.gz / deb / rpm）、Android（Gradle 出 APK）；
3. 按版本号打 tag，创建或更新同名 Release 并上传全部产物。

版本号不变时重复推送只更新资产，改了 `version` 就自动出新 tag 与新 release。发布说明见 `.github/release-notes.md`（`__VERSION__` 会替换成实际版本号）。

## 实现要点

- 采集端只用 Go 标准库（`net/http` + `encoding/json`），无 cgo、无第三方运行时依赖。
- 「更新后新增数据量」= 表单 `updateTime` 之后到当日 23:59 的记录数，用一次 `count` 接口算差值。
- 并发 16 查询，页面轮询进度接口显示「已查询 N/M」。
- 前端零框架（原生 JS + 内联样式），三页共用同一套 CSS 变量与组件样式。

## 说明

- 两个平台的内部接口并非公开 API，字段与口径可能变化；本项目只做只读聚合，不保证接口长期稳定。
- 截图含真实表单名与记录条数，未放入公开仓库，需要时单独提供。
- 仅供内部使用，请遵守所在平台的使用条款。
