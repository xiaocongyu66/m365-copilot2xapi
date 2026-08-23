package proxypool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":99"
	}

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
		"--headless=old",
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

func (t *TurnstileSolver) BrowserLogin(username, password string) (*BrowserSession, error) {
	if t.chromePath == "" {
		return nil, fmt.Errorf("未找到 Chrome/Chromium")
	}

	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":99"
	}

	fp := t.fingerprint
	if fp == nil {
		fp = genWinChrome131()
	}

	log.Printf("[Browser] 指纹: %s, UA: %s", fp.ID, fp.UserAgent)

	// Find a free port for callback server
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("无法分配端口: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	resultChan := make(chan map[string]interface{}, 1)

	// Start callback HTTP server
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
	// Also handle OPTIONS for CORS
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

	// Generate HTML page that:
	// 1. Loads Turnstile JS
	// 2. Renders Turnstile widget
	// 3. Gets token
	// 4. Calls login API with token
	// 5. Sends result back via HTTP callback
	// Key: NO CDP connection, Chrome runs autonomously
	html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<title>Login</title>
<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>
</head>
<body>
<div id="ts" style="position:fixed;top:10px;left:10px;"></div>
<div id="status">init</div>
<script>
(function() {
  var CALLBACK_URL = 'http://127.0.0.1:%d/result';
  var SITE_KEY = '%s';
  var BASE_URL = '%s';
  var USERNAME = '%s';
  var PASSWORD = '%s';
  var status = document.getElementById('status');

  function sendResult(session, userID, err) {
    status.textContent = 'done';
    fetch(CALLBACK_URL, {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({session: session, user_id: userID, error: err})
    }).catch(function(){});
  }

  function tryLogin(token) {
    status.textContent = 'logging_in';
    fetch(BASE_URL + '/api/user/login?turnstile=' + encodeURIComponent(token), {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({username: USERNAME, password: PASSWORD})
    }).then(function(r) { return r.json(); }).then(function(data) {
      if (data.success) {
        var session = '';
        var match = document.cookie.match(/session=([^;]+)/);
        if (match) session = match[1];
        if (!session && data.data && data.data.session) session = data.data.session;
        var uid = '';
        if (data.data && data.data.user && data.data.user.id) uid = String(data.data.user.id);
        if (!uid && data.data && data.data.id) uid = String(data.data.id);
        sendResult(session, uid, '');
      } else {
        sendResult('', '', data.message || 'Login failed');
      }
    }).catch(function(e) {
      sendResult('', '', e.message);
    });
  }

  // Wait for Turnstile to load
  function waitForTurnstile() {
    if (window.turnstile && typeof window.turnstile.render === 'function') {
      status.textContent = 'rendering';
      turnstile.render(document.getElementById('ts'), {
        sitekey: SITE_KEY,
        callback: function(token) {
          status.textContent = 'token_ok';
          tryLogin(token);
        },
        'error-callback': function() {
          // Retry after delay
          status.textContent = 'render_error_retry';
          setTimeout(function() {
            document.getElementById('ts').innerHTML = '';
            waitForTurnstile();
          }, 2000);
        },
        'timeout-callback': function() {
          sendResult('', '', 'Turnstile timeout');
        }
      });
    } else {
      setTimeout(waitForTurnstile, 200);
    }
  }

  // Overall timeout
  setTimeout(function() {
    if (status.textContent !== 'done' && status.textContent !== 'logging_in') {
      sendResult('', '', 'Overall timeout: ' + status.textContent);
    }
  }, 90000);

  waitForTurnstile();
})();
</script>
</body>
</html>`, port, t.siteKey, t.baseURL, username, password)

	// Write HTML to temp file
	htmlFile := filepath.Join(os.TempDir(), fmt.Sprintf("aiapisign_login_%d.html", time.Now().UnixNano()))
	defer os.Remove(htmlFile)
	os.WriteFile(htmlFile, []byte(html), 0644)

	userDataDir := filepath.Join(os.TempDir(), fmt.Sprintf("aiapisign_chrome_%d", time.Now().UnixNano()))
	defer os.RemoveAll(userDataDir)

	// Launch Chrome with NO CDP connection - completely autonomous
	// Key: --headless=old + user-agent override + disable AutomationControlled
	// No --remote-debugging-port, no chromedp, no CDP at all
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, t.chromePath,
		"--headless=old",
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

	log.Printf("[Browser] 启动 Chrome（无 CDP）, 回调端口: %d", port)
	if err := cmd.Start(); err != nil {
		srv.Close()
		return nil, fmt.Errorf("启动 Chrome 失败: %w", err)
	}

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		srv.Close()
	}()

	// Wait for result
	log.Printf("[Browser] 等待 Turnstile 验证和登录...")

	select {
	case result := <-resultChan:
		log.Printf("[Browser] 收到结果")

		session, _ := result["session"].(string)
		userID, _ := result["user_id"].(string)
		errMsg, _ := result["error"].(string)

		if errMsg != "" {
			return nil, fmt.Errorf("%s", errMsg)
		}
		if session == "" {
			return nil, fmt.Errorf("登录成功但未获取到 session")
		}

		if userID == "" {
			apiClient := NewAPIClient(t.baseURL)
			apiClient.SetSession(session)
			if info, err := apiClient.GetUserInfo(); err == nil {
				userID = fmt.Sprintf("%d", info.ID)
			}
		}

		log.Printf("[Browser] 登录成功, userID=%s", userID)
		return &BrowserSession{Session: session, UserID: userID}, nil

	case <-time.After(95 * time.Second):
		return nil, fmt.Errorf("等待登录结果超时（95秒），Turnstile 可能在当前环境中无法通过验证")
	}
}

func _suppressUnusedImportsTurnstile() {
	_ = strconv.Itoa
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
