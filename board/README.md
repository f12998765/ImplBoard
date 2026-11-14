# 实施看板 ImplBoard（桌面壳）

一个主页，进去是简道云 / 轻流两个看板，看板左上角「‹ 主页」返回。

```
board/
  app.py          外壳：起三个本地服务 + 开窗口
  home.html       主页（两个卡片）
  icons/          从两家官网 favicon 转的 128px PNG
  dist/           打好的 .app 和 zip
```

三个本地只读服务（端口都是启动时随机挑）：

| 服务 | 来源 | 作用 |
| --- | --- | --- |
| 简道云 | `jdy-today/jdy_today_server.py` | 表单更新 / 今日数据 / 7 日趋势 |
| 轻流 | `qflow-today/qf_server.py` | 应用包 / 当日新增 / 发布时段 |
| 主页 | `board/app.py` | 静态页 + 图标，把两个地址塞进卡片 |

主页卡片链到 `http://127.0.0.1:<port>/?home=<主页地址>`；
两个看板的 `index.html` 末尾各有一小段脚本，检测到 `?home=` 就在标题左边
插一个「‹ 主页」按钮，点了跳回去。没有参数时不插，单独打开看板不受影响。

## 重新打包

```bash
cd board
PYINSTALLER_CONFIG_DIR=$PWD/../.picache ../.venv-build/bin/pyinstaller \
  --noconfirm --clean --windowed --name ImplBoard --onedir \
  --distpath ./dist --workpath ./build \
  --paths ../jdy-today --paths ../qflow-today \
  --add-data "../jdy-today/index.html:jdy" \
  --add-data "../qflow-today/index.html:qf" \
  --add-data "home.html:." \
  --add-data "icons:icons" \
  --hidden-import webview.platforms.cocoa --collect-submodules webview \
  app.py

# 改显示名、重签名、改中文名
/usr/libexec/PlistBuddy -c "Set :CFBundleDisplayName 实施看板" dist/ImplBoard.app/Contents/Info.plist
codesign --force --deep --sign - dist/ImplBoard.app
mv dist/ImplBoard.app "dist/实施看板.app"
```

## 配置

两个平台的凭据都在 `~/Library/Application Support/ImplBoard/`：

- `jdy_auth.json` 简道云（Cookie + csrf）
- `qf_auth.json` 轻流（prodtoken + wsid）

调试时可以用 `JDY_AUTH` / `QF_AUTH` 环境变量指向别的文件。
