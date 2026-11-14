实施看板（Go 原生版）· 简道云 / 轻流 双平台当日看板。所有请求均为只读查询。

## 下载

| 平台 | 文件 |
| --- | --- |
| macOS（Apple 芯片 M1/M2/M3…） | `implboard-__VERSION__-arm64.dmg` |
| macOS（Intel） | `implboard-__VERSION__-amd64.dmg` |
| Windows x64 | `implboard-setup-__VERSION__-amd64.exe`（安装包）、`implboard-__VERSION__-windows-amd64.exe`（便携版） |
| Windows ARM64 | `implboard-setup-__VERSION__-arm64.exe`（安装包）、`implboard-__VERSION__-windows-arm64.exe`（便携版） |
| Linux x64 | `implboard-__VERSION__-linux-amd64.tar.gz`、`implboard___VERSION___amd64.deb`、`implboard-__VERSION__-1.x86_64.rpm` |
| Linux ARM64 | `implboard-__VERSION__-linux-arm64.tar.gz`、`implboard___VERSION___arm64.deb`、`implboard-__VERSION__-1.aarch64.rpm` |

## 安装

- **macOS**：应用为 ad-hoc 签名（未公证）。首次打开请右键 →「打开」，或执行
  `xattr -dr com.apple.quarantine "/Applications/实施看板.app"`。
- **Windows**：安装包未签名，SmartScreen 提示时选「更多信息 → 仍要运行」。
- **Linux**：`.tar.gz` 解压后运行 `./install.sh`，装到 `~/.local` 并可自更新；也可以直接装 `.deb` / `.rpm`。

## 首次使用

1. 在浏览器登录对应平台，复制一份请求里的凭证：
   - 简道云：Cookie 与 `x-csrf-token`
   - 轻流：`token` 与 `wsid`
2. 打开应用，点「账号」粘贴保存；凭证只存在本机。
3. 选日期与应用包（简道云）后点「刷新」，当天数据会按「应用包 + 日期」缓存在本地。

## 说明

- 两个平台的内部接口并非公开 API，字段与口径可能变化；本项目只做只读聚合。
- 手机上同样是这套页面：工具条只留「日期 + 应用包 + ⋯」，其余操作收进菜单。
