package platform

import (
	"os"
	"strings"
)

// IsAndroid reports whether ThothDock runs on Android.
func IsAndroid() bool {
	if os.Getenv("ANDROID_ROOT") != "" {
		return true
	}
	_, err := os.Stat("/system/build.prop")
	return err == nil
}

// androidCADirs are Android's system trust stores: the updatable Conscrypt
// APEX store (Android 14+) first, then the classic system directory.
var androidCADirs = []string{"/apex/com.android.conscrypt/cacerts", "/system/etc/security/cacerts"}

// UseAndroidTrustStore makes Go's TLS verification use Android's system CA
// certificates. A linux/arm64 Go binary otherwise looks only in desktop
// Linux locations and trusts nothing on Android. It never overrides an
// explicit SSL_CERT_FILE or SSL_CERT_DIR, and must run before any TLS use.
func UseAndroidTrustStore() {
	if !IsAndroid() || os.Getenv("SSL_CERT_FILE") != "" || os.Getenv("SSL_CERT_DIR") != "" {
		return
	}
	var dirs []string
	for _, d := range androidCADirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) > 0 {
		os.Setenv("SSL_CERT_DIR", strings.Join(dirs, ":"))
	}
}
