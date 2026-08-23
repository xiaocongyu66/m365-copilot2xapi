package proxypool

import (
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"
)

type Fingerprint struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Weight              int      `json:"weight,omitempty"`
	UserAgent           string   `json:"user_agent"`
	Platform            string   `json:"platform"`
	Languages           []string `json:"languages"`
	PlatformVersion     string   `json:"platform_version"`
	HardwareConcurrency int      `json:"hardware_concurrency"`
	DeviceMemory        int      `json:"device_memory"`
	ScreenWidth         int      `json:"screen_width"`
	ScreenHeight        int      `json:"screen_height"`
	ScreenAvailWidth    int      `json:"screen_avail_width"`
	ScreenAvailHeight   int      `json:"screen_avail_height"`
	ColorDepth          int      `json:"color_depth"`
	PixelRatio          float64  `json:"pixel_ratio"`
	Timezone            string   `json:"timezone"`
	TimezoneOffset      int      `json:"timezone_offset"`
	WebGLVendor         string   `json:"webgl_vendor"`
	WebGLRenderer       string   `json:"webgl_renderer"`
	SecCHUA             string   `json:"sec_ch_ua"`
	SecCHUAPlatform     string   `json:"sec_ch_ua_platform"`
	Plugins             []string `json:"plugins"`
	CanvasNoise         string   `json:"canvas_noise"`
	AudioNoise          float64  `json:"audio_noise"`
}

var (
	fingerprintRegistry map[string]*Fingerprint
	fpMu                sync.RWMutex
	fpRng               *rand.Rand
)

func init() {
	fingerprintRegistry = make(map[string]*Fingerprint)
	fpRng = rand.New(rand.NewSource(time.Now().UnixNano()))
	registerFingerprints()
}

func registerFingerprints() {
	fpMu.Lock()
	defer fpMu.Unlock()

	for _, fp := range buildAllFingerprints() {
		fingerprintRegistry[fp.ID] = fp
	}
	log.Printf("[指纹] 已注册 %d 个指纹配置", len(fingerprintRegistry))
}

func buildAllFingerprints() []*Fingerprint {
	return []*Fingerprint{
		genWinChrome131(),
		genMacChrome131(),
		genWinEdge130(),
		genLinuxChrome131(),
		genWinFirefox130(),
	}
}

// PickFingerprintForAccount selects a fingerprint based on account config or global pool
func PickFingerprintForAccount(acc Account, globalCfg GlobalFingerprintConfig) *Fingerprint {
	fpMu.Lock()
	defer fpMu.Unlock()

	var refs []FingerprintRef

	// If account has its own fingerprint config enabled, use it
	if acc.Fingerprint.Enabled && len(acc.Fingerprint.Refs) > 0 {
		refs = acc.Fingerprint.Refs
		log.Printf("[指纹] 账号 %s 使用独立指纹配置 (%d 个)", acc.Name, len(refs))
	} else if globalCfg.Enabled && len(globalCfg.Refs) > 0 {
		refs = globalCfg.Refs
	} else {
		refs = defaultFingerprintRefs()
	}

	// Calculate total weight
	totalWeight := 0
	for _, ref := range refs {
		if ref.Weight < 0 {
			ref.Weight = 0
		}
		totalWeight += ref.Weight
	}
	if totalWeight == 0 {
		totalWeight = 100
	}

	// Weighted random selection
	r := fpRng.Intn(totalWeight)
	cumulative := 0

	for _, ref := range refs {
		cumulative += ref.Weight
		if r < cumulative {
			if fp, ok := fingerprintRegistry[ref.ID]; ok {
				// Return a copy with the current weight
				result := *fp
				result.Weight = ref.Weight
				log.Printf("[指纹] 选中: %s (权重 %d/%d = %.1f%%)",
					fp.ID, ref.Weight, totalWeight, float64(ref.Weight)/float64(totalWeight)*100)
				return &result
			}
		}
	}

	// Fallback: return first available
	for _, fp := range fingerprintRegistry {
		return fp
	}
	return nil
}

// GetFingerprintByID returns a fingerprint by ID
func GetFingerprintByID(id string) *Fingerprint {
	fpMu.RLock()
	defer fpMu.RUnlock()
	if fp, ok := fingerprintRegistry[id]; ok {
		copy := *fp
		return &copy
	}
	return nil
}

// GetAllFingerprints returns all registered fingerprints
func GetAllFingerprints() []*Fingerprint {
	fpMu.RLock()
	defer fpMu.RUnlock()
	result := make([]*Fingerprint, 0, len(fingerprintRegistry))
	for _, fp := range fingerprintRegistry {
		copy := *fp
		result = append(result, &copy)
	}
	return result
}

// GenerateInjectionJS creates JavaScript that overrides browser APIs to match the fingerprint
func (fp *Fingerprint) GenerateInjectionJS() string {
	languagesJSON := "["
	for i, lang := range fp.Languages {
		if i > 0 {
			languagesJSON += ","
		}
		languagesJSON += fmt.Sprintf(`"%s"`, lang)
	}
	languagesJSON += "]"

	pluginsJSON := "["
	for i, name := range fp.Plugins {
		if i > 0 {
			pluginsJSON += ","
		}
		pluginsJSON += fmt.Sprintf(`{"name":"%s","filename":"%s","description":"%s"}`,
			name, name+".dll", name+" Plugin")
	}
	pluginsJSON += "]"

	return fmt.Sprintf(`(function(){
Object.defineProperty(navigator,'userAgent',{get:function(){return '%s';}});
Object.defineProperty(navigator,'appVersion',{get:function(){return '%s';}});
Object.defineProperty(navigator,'platform',{get:function(){return '%s';}});
Object.defineProperty(navigator,'languages',{get:function(){return %s;}});
Object.defineProperty(navigator,'language',{get:function(){return '%s';}});
Object.defineProperty(navigator,'hardwareConcurrency',{get:function(){return %d;}});
Object.defineProperty(navigator,'deviceMemory',{get:function(){return %d;}});
Object.defineProperty(navigator,'webdriver',{get:function(){return undefined;}});
if(!window.chrome){window.chrome={};}
if(!window.chrome.runtime){window.chrome.runtime={};}
Object.defineProperty(navigator,'plugins',{get:function(){var p=%s;p.item=function(i){return p[i]||null;};p.namedItem=function(n){for(var i=0;i<p.length;i++){if(p[i].name===n)return p[i];}return null;};return p;}});
Object.defineProperty(screen,'width',{get:function(){return %d;}});
Object.defineProperty(screen,'height',{get:function(){return %d;}});
Object.defineProperty(screen,'availWidth',{get:function(){return %d;}});
Object.defineProperty(screen,'availHeight',{get:function(){return %d;}});
Object.defineProperty(screen,'colorDepth',{get:function(){return %d;}});
Object.defineProperty(screen,'pixelDepth',{get:function(){return %d;}});
Object.defineProperty(window,'devicePixelRatio',{get:function(){return %f;}});
Object.defineProperty(window,'outerWidth',{get:function(){return %d;}});
Object.defineProperty(window,'outerHeight',{get:function(){return %d;}});
var oGP=WebGLRenderingContext.prototype.getParameter;
WebGLRenderingContext.prototype.getParameter=function(p){if(p===0x9245)return '%s';if(p===0x9246)return '%s';return oGP.call(this,p);};
if(typeof WebGL2RenderingContext!=='undefined'){var oGP2=WebGL2RenderingContext.prototype.getParameter;WebGL2RenderingContext.prototype.getParameter=function(p){if(p===0x9245)return '%s';if(p===0x9246)return '%s';return oGP2.call(this,p);};}
try{var oDF=Intl.DateTimeFormat;Intl.DateTimeFormat=function(){var a=arguments;if(!a[1])a[1]={};if(!a[1].timeZone)a[1].timeZone='%s';return new oDF(a[0],a[1]);};Intl.DateTimeFormat.prototype=oDF.prototype;}catch(e){}
Date.prototype.getTimezoneOffset=function(){return %d;};
var oTDU=HTMLCanvasElement.prototype.toDataURL;HTMLCanvasElement.prototype.toDataURL=function(){var ctx=this.getContext('2d');if(ctx){var imgData=ctx.getImageData(0,0,this.width,this.height);var noise='%s';for(var i=0;i<imgData.data.length;i+=4){var h=0;for(var j=0;j<noise.length;j++){h=((h<<5)-h+noise.charCodeAt(j))|0;}imgData.data[i]=(imgData.data[i]+(h&0x3))&0xFF;}ctx.putImageData(imgData,0,0);}return oTDU.apply(this,arguments);};
if(navigator.userAgentData){Object.defineProperty(navigator,'userAgentData',{get:function(){return{brands:%s,mobile:false,platform:'%s'};}});}
})();`,
		fp.UserAgent,
		fp.UserAgent,
		fp.Platform,
		languagesJSON,
		fp.Languages[0],
		fp.HardwareConcurrency,
		fp.DeviceMemory,
		pluginsJSON,
		fp.ScreenWidth,
		fp.ScreenHeight,
		fp.ScreenAvailWidth,
		fp.ScreenAvailHeight,
		fp.ColorDepth,
		fp.ColorDepth,
		fp.PixelRatio,
		fp.ScreenWidth,
		fp.ScreenHeight,
		fp.WebGLVendor,
		fp.WebGLRenderer,
		fp.WebGLVendor,
		fp.WebGLRenderer,
		fp.Timezone,
		-fp.TimezoneOffset,
		fp.CanvasNoise,
		fp.SecCHUA,
		fp.SecCHUAPlatform,
	)
}

func defaultFingerprintRefs() []FingerprintRef {
	return []FingerprintRef{
		{ID: "win-chrome-131", Weight: 35},
		{ID: "mac-chrome-131", Weight: 25},
		{ID: "win-edge-130", Weight: 40},
	}
}

// --- Fingerprint generators ---

func genWinChrome131() *Fingerprint {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	sw := []int{1920, 2560, 1366}[r.Intn(3)]
	sh := map[int]int{1920: 1080, 2560: 1440, 1366: 768}[sw]
	return &Fingerprint{
		ID:                  "win-chrome-131",
		Name:                "Windows 10 Chrome 131",
		UserAgent:           "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		Platform:            "Win32",
		PlatformVersion:     "15.0.0",
		Languages:           []string{"zh-CN", "zh", "en-US", "en"},
		HardwareConcurrency: []int{4, 8, 12, 16}[r.Intn(4)],
		DeviceMemory:        []int{8, 16, 32}[r.Intn(3)],
		ScreenWidth:         sw,
		ScreenHeight:        sh,
		ScreenAvailWidth:    sw,
		ScreenAvailHeight:   sh - 40,
		ColorDepth:          24,
		PixelRatio:          1.0,
		Timezone:            "Asia/Shanghai",
		TimezoneOffset:      480,
		WebGLVendor:         "Google Inc. (Intel)",
		WebGLRenderer:       "ANGLE (Intel, Intel(R) UHD Graphics 630 Direct3D11 vs_5_0 ps_5_0, D3D11)",
		SecCHUA:             `[{"brand":"Google Chrome","version":"131"},{"brand":"Chromium","version":"131"},{"brand":"Not_A Brand","version":"24"}]`,
		SecCHUAPlatform:     "Windows",
		Plugins:             []string{"PDF Viewer", "Chrome PDF Viewer", "Chromium PDF Viewer", "WebKit built-in PDF"},
		CanvasNoise:         fmt.Sprintf("win-chrome-%d", r.Intn(999999)),
		AudioNoise:          0.0001 + r.Float64()*0.0001,
	}
}

func genMacChrome131() *Fingerprint {
	r := rand.New(rand.NewSource(time.Now().UnixNano() + 1))
	return &Fingerprint{
		ID:                  "mac-chrome-131",
		Name:                "macOS Chrome 131",
		UserAgent:           "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		Platform:            "MacIntel",
		PlatformVersion:     "14.5.0",
		Languages:           []string{"zh-CN", "zh", "en-US", "en"},
		HardwareConcurrency: []int{8, 10, 12}[r.Intn(3)],
		DeviceMemory:        []int{8, 16, 32}[r.Intn(3)],
		ScreenWidth:         2560,
		ScreenHeight:        1440,
		ScreenAvailWidth:    2560,
		ScreenAvailHeight:   1417,
		ColorDepth:          30,
		PixelRatio:          2.0,
		Timezone:            "Asia/Shanghai",
		TimezoneOffset:      480,
		WebGLVendor:         "Google Inc. (Apple)",
		WebGLRenderer:       "ANGLE (Apple, ANGLE Metal Renderer: Apple M1 Pro, Unspecified Version)",
		SecCHUA:             `[{"brand":"Google Chrome","version":"131"},{"brand":"Chromium","version":"131"},{"brand":"Not_A Brand","version":"24"}]`,
		SecCHUAPlatform:     "macOS",
		Plugins:             []string{"PDF Viewer", "Chrome PDF Viewer", "Chromium PDF Viewer", "WebKit built-in PDF"},
		CanvasNoise:         fmt.Sprintf("mac-chrome-%d", r.Intn(999999)),
		AudioNoise:          0.0001 + r.Float64()*0.0001,
	}
}

func genWinEdge130() *Fingerprint {
	r := rand.New(rand.NewSource(time.Now().UnixNano() + 2))
	return &Fingerprint{
		ID:                  "win-edge-130",
		Name:                "Windows 11 Edge 130",
		UserAgent:           "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0",
		Platform:            "Win32",
		PlatformVersion:     "15.0.0",
		Languages:           []string{"zh-CN", "zh", "en-US", "en"},
		HardwareConcurrency: []int{4, 6, 8, 12}[r.Intn(4)],
		DeviceMemory:        []int{8, 16, 32}[r.Intn(3)],
		ScreenWidth:         1920,
		ScreenHeight:        1080,
		ScreenAvailWidth:    1920,
		ScreenAvailHeight:   1040,
		ColorDepth:          24,
		PixelRatio:          1.0,
		Timezone:            "Asia/Shanghai",
		TimezoneOffset:      480,
		WebGLVendor:         "Google Inc. (NVIDIA)",
		WebGLRenderer:       "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 Direct3D11 vs_5_0 ps_5_0, D3D11)",
		SecCHUA:             `[{"brand":"Microsoft Edge","version":"130"},{"brand":"Chromium","version":"130"},{"brand":"Not_A Brand","version":"24"}]`,
		SecCHUAPlatform:     "Windows",
		Plugins:             []string{"PDF Viewer", "Chrome PDF Viewer", "Chromium PDF Viewer", "Microsoft Edge PDF Viewer"},
		CanvasNoise:         fmt.Sprintf("win-edge-%d", r.Intn(999999)),
		AudioNoise:          0.0001 + r.Float64()*0.0001,
	}
}

func genLinuxChrome131() *Fingerprint {
	r := rand.New(rand.NewSource(time.Now().UnixNano() + 3))
	return &Fingerprint{
		ID:                  "linux-chrome-131",
		Name:                "Linux Chrome 131",
		UserAgent:           "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		Platform:            "Linux x86_64",
		PlatformVersion:     "6.5.0",
		Languages:           []string{"zh-CN", "zh", "en-US", "en"},
		HardwareConcurrency: []int{4, 8, 16}[r.Intn(3)],
		DeviceMemory:        []int{8, 16, 32}[r.Intn(3)],
		ScreenWidth:         1920,
		ScreenHeight:        1080,
		ScreenAvailWidth:    1920,
		ScreenAvailHeight:   1053,
		ColorDepth:          24,
		PixelRatio:          1.0,
		Timezone:            "Asia/Shanghai",
		TimezoneOffset:      480,
		WebGLVendor:         "Google Inc. (Intel)",
		WebGLRenderer:       "ANGLE (Intel, Mesa Intel(R) UHD Graphics 770 (ADL-S GT1), OpenGL 4.6)",
		SecCHUA:             `[{"brand":"Google Chrome","version":"131"},{"brand":"Chromium","version":"131"},{"brand":"Not_A Brand","version":"24"}]`,
		SecCHUAPlatform:     "Linux",
		Plugins:             []string{"PDF Viewer", "Chrome PDF Viewer", "Chromium PDF Viewer"},
		CanvasNoise:         fmt.Sprintf("linux-chrome-%d", r.Intn(999999)),
		AudioNoise:          0.0001 + r.Float64()*0.0001,
	}
}

func genWinFirefox130() *Fingerprint {
	r := rand.New(rand.NewSource(time.Now().UnixNano() + 4))
	sw := 1920
	sh := 1080
	return &Fingerprint{
		ID:                  "win-firefox-130",
		Name:                "Windows 10 Firefox 130",
		UserAgent:           "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:130.0) Gecko/20100101 Firefox/130.0",
		Platform:            "Win32",
		PlatformVersion:     "15.0.0",
		Languages:           []string{"zh-CN", "zh", "en-US", "en"},
		HardwareConcurrency: []int{4, 8, 12}[r.Intn(3)],
		DeviceMemory:        8,
		ScreenWidth:         sw,
		ScreenHeight:        sh,
		ScreenAvailWidth:    sw,
		ScreenAvailHeight:   sh - 40,
		ColorDepth:          24,
		PixelRatio:          1.0,
		Timezone:            "Asia/Shanghai",
		TimezoneOffset:      480,
		WebGLVendor:         "Mozilla",
		WebGLRenderer:       "ANGLE (Intel, Intel(R) UHD Graphics 630 Direct3D11 vs_5_0 ps_5_0, D3D11)",
		SecCHUA:             `[]`,
		SecCHUAPlatform:     "",
		Plugins:             []string{"PDF Viewer", "Firefox PDF Viewer"},
		CanvasNoise:         fmt.Sprintf("win-ff-%d", r.Intn(999999)),
		AudioNoise:          0.0001 + r.Float64()*0.0001,
	}
}

// RerollWeights randomly redistributes weights for a given set of refs
func RerollWeights(refs []FingerprintRef) []FingerprintRef {
	if len(refs) == 0 {
		return defaultFingerprintRefs()
	}
	if len(refs) == 1 {
		refs[0].Weight = 100
		return refs
	}

	r := fpRng
	// Generate random weights that sum to 100
	weights := make([]int, len(refs))
	remaining := 100
	for i := 0; i < len(refs)-1; i++ {
		maxVal := remaining - (len(refs)-i-1)*10
		if maxVal < 10 {
			maxVal = 10
		}
		weights[i] = r.Intn(maxVal-9) + 10
		remaining -= weights[i]
	}
	weights[len(refs)-1] = remaining

	// Shuffle weight assignments
	r.Shuffle(len(weights), func(i, j int) {
		weights[i], weights[j] = weights[j], weights[i]
	})

	for i := range refs {
		refs[i].Weight = weights[i]
	}

	return refs
}
