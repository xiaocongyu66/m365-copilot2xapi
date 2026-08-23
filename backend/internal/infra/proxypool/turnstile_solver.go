package proxypool

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type BrowserSession struct {
	Session string
	UserID  string
}

type TurnstileSolver struct {
	siteKey    string
	baseURL    string
	chromePath string
	fingerprint *Fingerprint
}

func NewTurnstileSolver(baseURL, siteKey string, fp *Fingerprint) *TurnstileSolver {
	return &TurnstileSolver{
		siteKey:     siteKey,
		baseURL:     baseURL,
		chromePath:  findChromePath(),
		fingerprint: fp,
	}
}

func findChromePath() string {
	for _, p := range []string{
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/local/bin/chromium",
	} {
		if pathExists(p) {
			return p
		}
	}
	return ""
}

// Solve 用 Chrome 无头浏览器求解 Cloudflare Turnstile token
// 不需要 CDP/chromedp,Chrome 完全自主运行:
//   1. 生成一个 HTML 页面(内嵌 Turnstile JS)
//   2. Chrome 无头模式打开 HTML(--headless=old + --disable-blink-features=AutomationControlled)
//   3. Chrome 里的 JS 求解 Turnstile 后通过 HTTP 回调返回 token
func (t *TurnstileSolver) Solve() (string, error) {
	if t.chromePath == "" {
		return "", fmt.Errorf("未找到 Chrome/Chromium,请安装: apt install chromium-browser")
	}

	// 确保 Xvfb 虚拟显示器在运行(Turnstile JS 需要渲染环境)
	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":99"
		os.Setenv("DISPLAY", display)
	}
	ensureXvfb(display)

	fp := t.fingerprint
	if fp == nil {
		fp = genWinChrome131()
	}

	// 分配回调端口
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("无法分配端口: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	resultChan := make(chan map[string]interface{}, 1)

	// 启动回调 HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/result", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var result map[string]interface{}
			json.NewDecoder(r.Body).Decode(&result)
			select {
			case resultChan <- result:
			default:
			}
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	})

	srv := &http.Server{Handler: mux}
	go srv.ListenAndServe()

	// 生成 HTML 页面:只求解 Turnstile token,不登录
	html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<title>Turnstile Solver</title>
<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>
</head>
<body>
<div id="ts" style="position:fixed;top:10px;left:10px;"></div>
<div id="status">init</div>
<script>
(function() {
  var CALLBACK_URL = 'http://127.0.0.1:%d/result';
  var SITE_KEY = '%s';
  var status = document.getElementById('status');

  function sendResult(token, err) {
    status.textContent = 'done';
    fetch(CALLBACK_URL, {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({token: token, error: err})
    }).catch(function(){});
  }

  function waitForTurnstile() {
    if (window.turnstile && typeof window.turnstile.render === 'function') {
      status.textContent = 'rendering';
      turnstile.render(document.getElementById('ts'), {
        sitekey: SITE_KEY,
        callback: function(token) {
          status.textContent = 'token_ok';
          sendResult(token, '');
        },
        'error-callback': function() {
          status.textContent = 'render_error_retry';
          setTimeout(function() {
            document.getElementById('ts').innerHTML = '';
            waitForTurnstile();
          }, 2000);
        },
        'timeout-callback': function() {
          sendResult('', 'Turnstile timeout');
        }
      });
    } else {
      setTimeout(waitForTurnstile, 200);
    }
  }

  setTimeout(function() {
    if (status.textContent !== 'done') {
      sendResult('', 'Overall timeout: ' + status.textContent);
    }
  }, 90000);

  waitForTurnstile();
})();
</script>
</body>
</html>`, port, t.siteKey)

	// 写 HTML 到临时文件
	htmlFile := filepath.Join(os.TempDir(), fmt.Sprintf("m365_turnstile_%d.html", time.Now().UnixNano()))
	defer os.Remove(htmlFile)
	os.WriteFile(htmlFile, []byte(html), 0644)

	userDataDir := filepath.Join(os.TempDir(), fmt.Sprintf("m365_chrome_%d", time.Now().UnixNano()))
	defer os.RemoveAll(userDataDir)

	// 启动 Chrome(无 CDP,完全自主运行)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, t.chromePath,
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--disable-blink-features=AutomationControlled",
		"--disable-extensions",
		"--no-first-run",
		"--disable-component-update",
		"--disable-infobars",
		"--disable-background-networking",
		"--disable-default-apps",
		"--window-size=1920,1080",
		"--user-agent="+fp.UserAgent,
		"--user-data-dir="+userDataDir,
		"file://"+htmlFile,
	)
	cmd.Env = append(os.Environ(), "DISPLAY="+display)

	if err := cmd.Start(); err != nil {
		srv.Close()
		return "", fmt.Errorf("启动 Chrome 失败: %w", err)
	}

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		srv.Close()
	}()

	// 等待结果
	select {
	case result := <-resultChan:
		token, _ := result["token"].(string)
		errMsg, _ := result["error"].(string)
		if errMsg != "" {
			return "", fmt.Errorf("%s", errMsg)
		}
		if token == "" {
			return "", fmt.Errorf("Turnstile 求解失败,未获取到 token")
		}
		return token, nil

	case <-time.After(95 * time.Second):
		return "", fmt.Errorf("等待 Turnstile 求解超时(95秒)")
	}
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ensureXvfb 确保指定 display 上有 Xvfb 虚拟显示器在运行
func ensureXvfb(display string) {
	// 检查 Xvfb 是否已在运行
	displayNum := strings.TrimPrefix(display, ":")
	tmpFile := "/tmp/.X" + displayNum + "-lock"
	if _, err := os.Stat(tmpFile); err == nil {
		return // Xvfb 已在运行
	}
	// 查找 Xvfb 路径
	xvfbPath := ""
	for _, p := range []string{"/usr/bin/Xvfb", "/usr/local/bin/Xvfb", "/usr/bin/xvfb-run"} {
		if pathExists(p) {
			xvfbPath = p
			break
		}
	}
	if xvfbPath == "" {
		return // 没安装 Xvfb,Chrome --headless=new 可以不需要
	}
	// 启动 Xvfb
	cmd := exec.Command(xvfbPath, display, "-screen", "0", "1920x1080x24", "-ac")
	cmd.Start()
	time.Sleep(2 * time.Second)
	fmt.Printf("[Turnstile] Xvfb started on display %s\n", display)
}
