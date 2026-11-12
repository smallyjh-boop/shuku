package main

// NAS 本地语音（shuku-tts，tts/ 文件夹，MeloTTS 中英混读）：书库自动发现本机 27210 端口上的这个服务。
// 设置里“NAS 本地语音”是自动（默认）时，中文、英文句子先用它（不经过外网，网络不好也不卡）；
// 它没开、出错、太忙时用微软。泰文、日文、法文等别的语言照旧用微软。
// 提前合成的句子带 X-Priority: prefetch，本地服务先做正在听的那句。

import (
	"bytes"
	"context"
	"encoding/json"

	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type localTTSInfo struct {
	OK      bool    `json:"ok"`
	Version string  `json:"version"`
	Model   string  `json:"model"`
	RTF     float64 `json:"rtf"` // 合成 1 秒语音要几秒
	URL     string  `json:"url"`
}

var localTTS = struct {
	sync.Mutex
	info    localTTSInfo
	checked time.Time
	downTo  time.Time // 出错后这段时间里直接用微软
}{}

func localTTSURL() string {
	if u := strings.TrimRight(os.Getenv("SHUKU_LOCAL_TTS"), "/"); u != "" {
		return u
	}
	return "http://127.0.0.1:27210"
}

// 本地语音服务现在能不能用（最多一分钟问一次）
func localTTSStatus() localTTSInfo {
	localTTS.Lock()
	if time.Since(localTTS.checked) < time.Minute {
		i := localTTS.info
		localTTS.Unlock()
		return i
	}
	localTTS.checked = time.Now()
	localTTS.Unlock()
	info := localTTSInfo{URL: localTTSURL()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", info.URL+"/health", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		var h struct {
			App     string  `json:"app"`
			Version string  `json:"version"`
			Model   string  `json:"model"`
			RTF     float64 `json:"rtf"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&h) == nil && h.App == "shuku-tts" {
			info.OK, info.Version, info.Model, info.RTF = true, h.Version, h.Model, h.RTF
		}
		resp.Body.Close()
	}
	localTTS.Lock()
	localTTS.info = info
	localTTS.Unlock()
	return info
}

// 本地语音够不够快：合成 1 秒语音不到 1 秒（这台 NAS 上实际测的，随时在变）。
// 够快就中文、英文先用本地；不够快平时先用微软，微软不行再用本地兜底
func localTTSFast() bool {
	r := localTTSStatus().RTF
	return r > 0 && r <= 1.0
}

// 这句能不能用本地语音：设置没关、服务在、最近没出错，而且是中文或英文
func useLocalTTS(c TTSConfig, text, lang string) bool {
	if c.Engine != "edge" || c.Local == "off" {
		return false
	}
	localTTS.Lock()
	down := time.Now().Before(localTTS.downTo)
	localTTS.Unlock()
	if down || !localTTSStatus().OK {
		return false
	}
	switch scriptLang(text) {
	case "zh":
		return true
	case "latin":
		// 英文书、中文书里的英文句子；法文、德文这些书照旧用微软
		l := strings.ToLower(lang)
		return l == "" || strings.HasPrefix(l, "en") || strings.HasPrefix(l, "zh")
	}
	return false
}

func localTTSDown(err error) {
	localTTS.Lock()
	localTTS.downTo = time.Now().Add(time.Minute)
	localTTS.checked = time.Time{} // 下次重新问
	localTTS.Unlock()
	logEvent("warn", 0, "NAS 本地语音出错，先用微软："+err.Error())
}

type prefetchCtxKey struct{}

func localSynth(ctx context.Context, text string, speed float64) ([]byte, error) {
	body, _ := json.Marshal(map[string]any{"model": "melo", "input": text, "speed": speed})
	req, err := http.NewRequestWithContext(ctx, "POST", localTTSURL()+"/v1/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if ctx.Value(prefetchCtxKey{}) != nil {
		req.Header.Set("X-Priority", "prefetch")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != 200 || len(b) < 100 {
		return nil, fmt.Errorf("本地语音返回 %d", resp.StatusCode)
	}
	return b, nil
}


