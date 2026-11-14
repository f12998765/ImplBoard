package com.xizero.implboard;

import android.app.Activity;
import android.os.Bundle;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;

/**
 * 唯一的 Activity：开一个 WebView，指向本进程内 Go 服务起的主页。
 * 页面、接口、缓存都在 Go 侧，这里只负责把它显示出来。
 */
public class MainActivity extends Activity {

    private WebView web;

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);

        web = new WebView(this);
        WebSettings s = web.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true); // 看板用 localStorage 缓存报告
        s.setAllowFileAccess(false);
        s.setAllowContentAccess(false);
        web.setWebViewClient(new WebViewClient());
        setContentView(web);

        String url = null;
        try {
            url = Mobile.start(getFilesDir().getAbsolutePath());
        } catch (Exception e) {
            url = null;
        }
        if (url == null || url.isEmpty()) {
            web.loadData(
                    "<meta name=viewport content=\"width=device-width,initial-scale=1\">"
                            + "<h3 style=\"font-family:sans-serif\">启动失败</h3>"
                            + "<p style=\"font-family:sans-serif;color:#666\">本地服务没能启动，重启应用再试。</p>",
                    "text/html", "utf-8");
        } else {
            web.loadUrl(url);
        }
    }

    @Override
    public void onBackPressed() {
        if (web != null && web.canGoBack()) {
            web.goBack();
        } else {
            super.onBackPressed();
        }
    }

    @Override
    protected void onDestroy() {
        if (web != null) {
            web.destroy();
            web = null;
        }
        super.onDestroy();
    }
}
