# -*- mode: python ; coding: utf-8 -*-
from PyInstaller.utils.hooks import collect_data_files
from PyInstaller.utils.hooks import collect_submodules

datas = [('../jdy-today/index.html', 'jdy'), ('../qflow-today/index.html', 'qf'), ('home.html', '.'), ('icons', 'icons')]
hiddenimports = ['webview.platforms.cocoa', 'certifi']
datas += collect_data_files('certifi')
hiddenimports += collect_submodules('webview')


a = Analysis(
    ['app.py'],
    pathex=['../jdy-today', '../qflow-today'],
    binaries=[],
    datas=datas,
    hiddenimports=hiddenimports,
    hookspath=[],
    hooksconfig={},
    runtime_hooks=[],
    excludes=[],
    noarchive=False,
    optimize=0,
)
pyz = PYZ(a.pure)

exe = EXE(
    pyz,
    a.scripts,
    [],
    exclude_binaries=True,
    name='ImplBoard',
    debug=False,
    bootloader_ignore_signals=False,
    strip=False,
    upx=True,
    console=False,
    disable_windowed_traceback=False,
    argv_emulation=False,
    target_arch=None,
    codesign_identity=None,
    entitlements_file=None,
)
coll = COLLECT(
    exe,
    a.binaries,
    a.datas,
    strip=False,
    upx=True,
    upx_exclude=[],
    name='ImplBoard',
)
app = BUNDLE(
    coll,
    name='ImplBoard.app',
    icon=None,
    bundle_identifier=None,
)
