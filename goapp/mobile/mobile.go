// Package mobile 是 Android 侧通过 gomobile 调用的入口：起同一套本地服务，
// 把主页地址交给 WebView。桌面端不走这里。
package mobile

import "implboard/internal/boot"

// Start 在 dir（Android 的 getFilesDir()）下启动服务并返回主页 URL。
// 出错时返回 error，gomobile 会把它变成 Java 的 Exception。
func Start(dir string) (string, error) {
	return boot.Start(dir)
}
