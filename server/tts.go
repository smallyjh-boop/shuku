package main

// 听书：服务器合成语音
//   edge   —— 微软自然语音（Edge 浏览器“大声朗读”同款，免费，需要 NAS 能访问 speech.platform.bing.com）
//   openai —— 任何 OpenAI 兼容的 /audio/speech 接口（OpenAI、硅基流动、NAS 上的 Kokoro / openai-edge-tts 等）
//   browser—— 不走服务器，用设备自带语音（前端处理）
// 合成结果按“引擎+音色+语速+文字”缓存，重听、多设备都不再重复合成。

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type TTSConfig struct {
	Engine string `json:"engine"` // edge | openai | browser
	Voice  string `json:"voice"`
	OpenAI struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
		Voice   string `json:"voice"`
		Voices  string `json:"voices"` // 可选：逗号分隔的音色列表
	} `json:"openai"`
}

func ttsConfig() TTSConfig {
	c := TTSConfig{Engine: "edge", Voice: "zh-CN-XiaoxiaoNeural"}
	c.OpenAI.Model = "tts-1"
	c.OpenAI.Voice = "alloy"
	json.Unmarshal([]byte(getSetting("tts", "{}")), &c)
	return c
}

type Voice struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Lang   string `json:"lang"`
	Gender string `json:"gender"`
	Desc   string `json:"desc"`
}

// 微软自然语音里适合读书的音色
var edgeVoices = []Voice{
	{"zh-CN-XiaoxiaoNeural", "晓晓", "普通话", "女", "温暖自然，适合小说"},
	{"zh-CN-XiaoyiNeural", "晓伊", "普通话", "女", "活泼，适合轻小说"},
	{"zh-CN-YunxiNeural", "云希", "普通话", "男", "阳光，适合小说"},
	{"zh-CN-YunjianNeural", "云健", "普通话", "男", "浑厚有激情，适合武侠"},
	{"zh-CN-YunyangNeural", "云扬", "普通话", "男", "播音腔，适合社科新闻"},
	{"zh-CN-YunxiaNeural", "云夏", "普通话", "男", "少年音"},
	{"zh-CN-liaoning-XiaobeiNeural", "晓北", "东北话", "女", "幽默"},
	{"zh-CN-shaanxi-XiaoniNeural", "晓妮", "陕西话", "女", ""},
	{"zh-HK-HiuMaanNeural", "曉曼", "粤语", "女", ""},
	{"zh-HK-WanLungNeural", "雲龍", "粤语", "男", ""},
	{"zh-TW-HsiaoChenNeural", "曉臻", "台湾腔", "女", ""},
	{"zh-TW-YunJheNeural", "雲哲", "台湾腔", "男", ""},
	{"en-US-AvaMultilingualNeural", "Ava", "英语", "女", "多语言，也能读中文"},
	{"en-US-AndrewMultilingualNeural", "Andrew", "英语", "男", "多语言"},
	{"en-US-AriaNeural", "Aria", "英语", "女", ""},
	{"en-GB-SoniaNeural", "Sonia", "英式英语", "女", ""},
	{"ja-JP-NanamiNeural", "七海", "日语", "女", ""},
	{"ja-JP-KeitaNeural", "圭太", "日语", "男", ""},
}

// ---------- 最小 WebSocket 客户端（只用于连接微软语音服务） ----------

type wsConn struct {
	c  net.Conn
	br *bufio.Reader
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func wsDial(ctx context.Context, rawURL string, headers http.Header) (*wsConn, *http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	secure := u.Scheme == "wss"
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = map[bool]string{true: "443", false: "80"}[secure]
	}
	addr := net.JoinHostPort(host, port)
	d := &net.Dialer{Timeout: 10 * time.Second}
	var raw net.Conn
	// 支持 HTTPS_PROXY（有的 NAS 走代理上网）
	httpsURL := &url.URL{Scheme: "https", Host: addr}
	if p, _ := http.ProxyFromEnvironment(&http.Request{URL: httpsURL}); p != nil && secure {
		raw, err = d.DialContext(ctx, "tcp", p.Host)
		if err == nil {
			fmt.Fprintf(raw, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", addr, addr)
			resp, err2 := http.ReadResponse(bufio.NewReader(raw), nil)
			if err2 != nil || resp.StatusCode != 200 {
				raw.Close()
				return nil, nil, fmt.Errorf("代理连接失败")
			}
		}
	} else {
		raw, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, nil, err
	}
	conn := raw
	if secure {
		tc := tls.Client(raw, &tls.Config{ServerName: host})
		if err := tc.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, nil, err
		}
		conn = tc
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(60 * time.Second))
	}
	key := base64.StdEncoding.EncodeToString(randBytes(16))
	var req bytes.Buffer
	fmt.Fprintf(&req, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n", u.RequestURI(), u.Host, key)
	for k, vs := range headers {
		for _, v := range vs {
			fmt.Fprintf(&req, "%s: %s\r\n", k, v)
		}
	}
	req.WriteString("\r\n")
	if _, err := conn.Write(req.Bytes()); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, resp, fmt.Errorf("语音服务拒绝连接（%d）", resp.StatusCode)
	}
	return &wsConn{c: conn, br: br}, resp, nil
}

func (w *wsConn) Close() error { return w.c.Close() }

func (w *wsConn) write(op byte, payload []byte) error {
	var hdr []byte
	hdr = append(hdr, 0x80|op)
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 65536:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		hdr = append(hdr, b[:]...)
	}
	mask := randBytes(4)
	hdr = append(hdr, mask...)
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	_, err := w.c.Write(append(hdr, masked...))
	return err
}

// 读一条完整消息（处理分片和 ping）
func (w *wsConn) read() (byte, []byte, error) {
	var msg []byte
	var msgOp byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(w.br, h[:]); err != nil {
			return 0, nil, err
		}
		fin, op := h[0]&0x80 != 0, h[0]&0x0F
		masked := h[1]&0x80 != 0
		n := uint64(h[1] & 0x7F)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(w.br, b[:]); err != nil {
				return 0, nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(w.br, b[:]); err != nil {
				return 0, nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		if n > 32<<20 {
			return 0, nil, errors.New("消息太大")
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(w.br, mask[:]); err != nil {
				return 0, nil, err
			}
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(w.br, data); err != nil {
			return 0, nil, err
		}
		if masked {
			for i := range data {
				data[i] ^= mask[i%4]
			}
		}
		switch op {
		case 0x8:
			return 0x8, data, io.EOF
		case 0x9:
			w.write(0xA, data)
			continue
		case 0xA:
			continue
		}
		if op != 0 {
			msgOp = op
		}
		msg = append(msg, data...)
		if fin {
			return msgOp, msg, nil
		}
	}
}

// ---------- 微软自然语音 ----------

const edgeToken = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"

var (
	edgeWSS      = "wss://speech.platform.bing.com/consumer/speech/synthesize/readaloud/edge/v1"
	edgeVersion  = "143.0.3650.75" // 和 edge-tts 保持一致；可以用环境变量 SHUKU_EDGE_VERSION 覆盖
	edgeSkewMu   sync.Mutex
	edgeSkewSecs int64
)

// Sec-MS-GEC 令牌：按 5 分钟取整的时间 + 固定令牌，v2 算法见 edge_drm.go
func edgeGEC() string {
	edgeSkewMu.Lock()
	skew := edgeSkewSecs
	edgeSkewMu.Unlock()
	return edgeGECv2(time.Now().Unix() + skew)
}

func edgeDate() string {
	return time.Now().UTC().Format("Mon Jan 02 2006 15:04:05") + " GMT+0000 (Coordinated Universal Time)"
}

// 去掉 SSML 里不允许的控制字符
func ssmlText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return ' '
		}
		return r
	}, s)
	return html.EscapeString(s)
}

func edgeSynth(ctx context.Context, text, voice string, speed float64) ([]byte, error) {
	audio, err := edgeSynthOnce(ctx, text, voice, speed)
	if err != nil && strings.Contains(err.Error(), "403") {
		// 多半是 NAS 时钟不准：已按服务器时间校正，再试一次
		audio, err = edgeSynthOnce(ctx, text, voice, speed)
	}
	return audio, err
}

func edgeSynthOnce(ctx context.Context, text, voice string, speed float64) ([]byte, error) {
	if v := os.Getenv("SHUKU_EDGE_VERSION"); v != "" {
		edgeVersion = v
	}
	connID := hex.EncodeToString(randBytes(16))
	u := fmt.Sprintf("%s?TrustedClientToken=%s&ConnectionId=%s&Sec-MS-GEC=%s&Sec-MS-GEC-Version=2-%s", edgeWSS, edgeToken, connID, edgeGEC(), edgeVersion)
	major := strings.Split(edgeVersion, ".")[0]
	h := http.Header{}
	h.Set("Pragma", "no-cache")
	h.Set("Cache-Control", "no-cache")
	h.Set("Origin", "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold")
	h.Set("User-Agent", fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s.0.0.0 Safari/537.36 Edg/%s.0.0.0", major, major))
	h.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("Cookie", "muid="+strings.ToUpper(hex.EncodeToString(randBytes(16)))+";")
	ws, resp, err := wsDial(ctx, u, h)
	if err != nil {
		if resp != nil && resp.StatusCode == 403 {
			if t, e := http.ParseTime(resp.Header.Get("Date")); e == nil {
				edgeSkewMu.Lock()
				edgeSkewSecs = t.Unix() - time.Now().Unix()
				edgeSkewMu.Unlock()
			}
		}
		return nil, err
	}
	defer ws.Close()
	cfg := "X-Timestamp:" + edgeDate() + "\r\nContent-Type:application/json; charset=utf-8\r\nPath:speech.config\r\n\r\n" +
		`{"context":{"synthesis":{"audio":{"metadataoptions":{"sentenceBoundaryEnabled":"false","wordBoundaryEnabled":"false"},"outputFormat":"audio-24khz-48kbitrate-mono-mp3"}}}}` + "\r\n"
	if err := ws.write(0x1, []byte(cfg)); err != nil {
		return nil, err
	}
	rate := int(math.Round((speed - 1) * 100))
	lang := "zh-CN"
	if parts := strings.Split(voice, "-"); len(parts) >= 2 {
		lang = parts[0] + "-" + parts[1]
	}
	ssml := fmt.Sprintf(`<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='%s'><voice name='%s'><prosody pitch='+0Hz' rate='%+d%%' volume='+0%%'>%s</prosody></voice></speak>`,
		lang, html.EscapeString(voice), rate, ssmlText(text))
	msg := "X-RequestId:" + connID + "\r\nContent-Type:application/ssml+xml\r\nX-Timestamp:" + edgeDate() + "Z\r\nPath:ssml\r\n\r\n" + ssml
	if err := ws.write(0x1, []byte(msg)); err != nil {
		return nil, err
	}
	var audio bytes.Buffer
	for {
		op, data, err := ws.read()
		if err != nil {
			if audio.Len() > 0 && errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("语音服务中断：%v", err)
		}
		if op == 0x1 {
			if strings.Contains(string(data), "Path:turn.end") {
				break
			}
			continue
		}
		if op == 0x2 && len(data) >= 2 {
			hl := int(binary.BigEndian.Uint16(data[:2]))
			if 2+hl <= len(data) && strings.Contains(string(data[2:2+hl]), "Path:audio") {
				audio.Write(data[2+hl:])
			}
		}
	}
	if audio.Len() == 0 {
		return nil, errors.New("语音服务没有返回声音，可能是音色名称不对")
	}
	return audio.Bytes(), nil
}

// ---------- OpenAI 兼容接口 ----------

func openaiSynth(ctx context.Context, c TTSConfig, text, voice string, speed float64) ([]byte, error) {
	if c.OpenAI.BaseURL == "" {
		return nil, errors.New("还没有填写语音接口地址")
	}
	if voice == "" {
		voice = c.OpenAI.Voice
	}
	body, _ := json.Marshal(map[string]any{"model": c.OpenAI.Model, "input": text, "voice": voice, "speed": speed, "response_format": "mp3"})
	u := strings.TrimRight(c.OpenAI.BaseURL, "/")
	if !strings.HasSuffix(u, "/audio/speech") {
		u += "/audio/speech"
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if c.OpenAI.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.OpenAI.APIKey)
	}
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("语音接口返回 %d：%s", resp.StatusCode, truncate(string(b), 160))
	}
	return b, nil
}

// ---------- 缓存 ----------

var ttsWrites int
var ttsMu sync.Mutex

func ttsCachePath(key string) string {
	return filepath.Join(cfg.DataDir, "cache", "tts", key[:2], key+".mp3")
}

// 缓存超过 400MB 时删掉最旧的
func trimTTSCache() {
	root := filepath.Join(cfg.DataDir, "cache", "tts")
	type fi struct {
		p string
		t time.Time
		n int64
	}
	var all []fi
	var total int64
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			all = append(all, fi{p, info.ModTime(), info.Size()})
			total += info.Size()
		}
		return nil
	})
	if total < 400<<20 {
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	for _, f := range all {
		if total < 300<<20 {
			break
		}
		os.Remove(f.p)
		total -= f.n
	}
}

var ttsSem = make(chan struct{}, 4) // 同时最多合成 4 段

func synthesize(ctx context.Context, text, voice string, speed float64) ([]byte, error) {
	c := ttsConfig()
	if voice == "" {
		voice = c.Voice
		if c.Engine == "openai" {
			voice = c.OpenAI.Voice
		}
	}
	return synthesizeWith(ctx, c.Engine, text, voice, speed)
}

// 用指定的语音服务合成（查字典的发音按语言挑微软的音色，不一定是设置里选的那个）
func synthesizeWith(ctx context.Context, engine, text, voice string, speed float64) ([]byte, error) {
	c := ttsConfig()
	if speed <= 0 {
		speed = 1
	}
	if speed < 0.5 {
		speed = 0.5
	}
	if speed > 3 {
		speed = 3
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%s|%s|%.2f|%s", engine, c.OpenAI.Model, voice, speed, text)))
	key := hex.EncodeToString(sum[:])
	p := ttsCachePath(key)
	if b, err := os.ReadFile(p); err == nil {
		now := time.Now()
		os.Chtimes(p, now, now)
		return b, nil
	}
	select {
	case ttsSem <- struct{}{}:
		defer func() { <-ttsSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var b []byte
	var err error
	switch engine {
	case "openai":
		b, err = openaiSynth(ctx, c, text, voice, speed)
	case "edge":
		b, err = edgeSynth(ctx, text, voice, speed)
	default:
		return nil, errors.New("当前设置为使用设备自带语音")
	}
	if err != nil {
		return nil, err
	}
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, b, 0o644)
	ttsMu.Lock()
	ttsWrites++
	doTrim := ttsWrites%200 == 0
	ttsMu.Unlock()
	if doTrim {
		go trimTTSCache()
	}
	return b, nil
}

// ---------- 接口 ----------

func handleTTSVoices(w http.ResponseWriter, r *http.Request) {
	c := ttsConfig()
	voices := []Voice{}
	def := c.Voice
	switch c.Engine {
	case "edge":
		voices = edgeVoices
	case "openai":
		list := c.OpenAI.Voices
		if list == "" {
			list = "alloy,echo,fable,onyx,nova,shimmer"
		}
		for _, v := range strings.Split(list, ",") {
			if v = strings.TrimSpace(v); v != "" {
				voices = append(voices, Voice{ID: v, Name: v})
			}
		}
		def = c.OpenAI.Voice
	}
	writeJSON(w, 200, map[string]any{"engine": c.Engine, "voices": voices, "default": def})
}

func handleTTS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text  string  `json:"text"`
		Voice string  `json:"voice"`
		Speed float64 `json:"speed"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "格式不正确")
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		errJSON(w, 400, "没有要读的文字")
		return
	}
	if len([]rune(req.Text)) > 3000 {
		req.Text = string([]rune(req.Text)[:3000])
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	b, err := synthesize(ctx, req.Text, req.Voice, req.Speed)
	if err != nil {
		errJSON(w, 502, "语音合成失败："+simplifyNetErr(err))
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(b)
}
