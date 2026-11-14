@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0"

echo ============================================
echo  构建 实施看板 ImplBoard (Windows)
echo ============================================
echo.

where py >nul 2>nul
if %errorlevel%==0 (set "PY=py -3") else (set "PY=python")
%PY% --version >nul 2>nul
if errorlevel 1 (
  echo [!] 没找到 Python。请先装 https://www.python.org/downloads/ 并勾选 Add to PATH。
  pause & exit /b 1
)

echo [1/3] 建虚拟环境...
if not exist .venv-build (
  %PY% -m venv .venv-build || (echo [!] 建 venv 失败 & pause & exit /b 1)
)
call .venv-build\Scripts\activate.bat

echo [2/3] 装依赖...
python -m pip install -q --upgrade pip
pip install -q pyinstaller pywebview certifi || (echo [!] 装依赖失败 & pause & exit /b 1)

echo [3/3] 打包...
pyinstaller --noconfirm --clean --windowed --name ImplBoard --onedir ^
  --paths jdy-today --paths qflow-today ^
  --add-data "jdy-today\index.html;jdy" ^
  --add-data "qflow-today\index.html;qf" ^
  --add-data "board\home.html;." ^
  --add-data "board\icons;icons" ^
  --hidden-import certifi --collect-data certifi ^
  --collect-all webview --hidden-import clr ^
  board\app.py
if errorlevel 1 (echo [!] 打包失败 & pause & exit /b 1)

echo.
echo 完成：dist\ImplBoard\ImplBoard.exe
echo 整个 dist\ImplBoard 文件夹一起拷贝给别人，双击 ImplBoard.exe 运行。
pause
