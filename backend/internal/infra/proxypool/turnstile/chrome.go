package turnstile

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// findChromePath 按优先级查找 Chrome/Chromium 可执行文件:
//  1. 环境变量 SOLVER_CHROME_PATH / CHROME_BIN / CHROME_PATH
//  2. ~/.cloakbrowser/ (跨平台,用户自带的反检测 Chrome)
//  3. playwright 缓存
//  4. 系统 Chrome
func findChromePath() string {
	for _, key := range []string{"SOLVER_CHROME_PATH", "CHROME_BIN", "CHROME_PATH"} {
		if p := os.Getenv(key); p != "" {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	home, _ := os.UserHomeDir()

	cloakDir := filepath.Join(home, ".cloakbrowser")
	if entries, err := os.ReadDir(cloakDir); err == nil {
		for _, e := range entries {
			if strings.Contains(strings.ToLower(e.Name()), "chrom") {
				for _, p := range []string{
					filepath.Join(cloakDir, e.Name(), "chrome"),
					filepath.Join(cloakDir, e.Name(), "Chromium.app", "Contents", "MacOS", "Chromium"),
					filepath.Join(cloakDir, e.Name(), "chrome.exe"),
				} {
					if _, err := os.Stat(p); err == nil {
						return p
					}
				}
			}
		}
	}

	var pwChrome string
	switch runtime.GOOS {
	case "darwin":
		pwChrome = filepath.Join(home, "Library", "Caches", "ms-playwright", "chromium-1234", "chrome-mac", "Chromium.app", "Contents", "MacOS", "Chromium")
	case "windows":
		pwChrome = filepath.Join(home, "AppData", "Local", "ms-playwright", "chromium-1234", "chrome-win", "chrome.exe")
	default:
		pwChrome = filepath.Join(home, ".cache", "ms-playwright", "chromium-1234", "chrome-linux", "chrome")
	}
	if _, err := os.Stat(pwChrome); err == nil {
		return pwChrome
	}

	headlessShell := filepath.Join(home, ".cache", "ms-playwright", "chromium_headless_shell-1234", "chrome-linux", "headless_shell")
	if _, err := os.Stat(headlessShell); err == nil {
		return headlessShell
	}

	switch runtime.GOOS {
	case "darwin":
		for _, p := range []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	case "windows":
		for _, p := range []string{
			filepath.Join(home, "AppData", "Local", "Google", "Chrome", "Application", "chrome.exe"),
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	default:
		for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome"} {
			if p, err := exec.LookPath(name); err == nil {
				return p
			}
		}
	}
	return ""
}

// ensureXvfb 在 Linux 无 DISPLAY 时启动 Xvfb 虚拟显示(Turnstile offscreen 模式需要)。
func ensureXvfb() {
	if runtime.GOOS != "linux" {
		return
	}
	if os.Getenv("DISPLAY") == "" {
		os.Setenv("DISPLAY", ":2")
	}
	cmd := exec.Command("xdpyinfo", "-display", ":2")
	if err := cmd.Run(); err == nil {
		return
	}
	tmpDir := os.TempDir()
	os.Remove(filepath.Join(tmpDir, ".X2-lock"))
	os.Remove(filepath.Join(tmpDir, ".X11-unix", "X2"))
	exec.Command("setsid", "Xvfb", ":2", "-screen", "0", "1280x720x24").Start()
	time.Sleep(2 * time.Second)
}
