package main

// 听书稿和听书包（1.0.0.32）。
//
// 听书稿：NAS 按章把正文排好、切成句子（和手机、手表的 BookText 一模一样：每段前空两格、按句末标点切，
// 太短的并、太长的按逗号切），每句带阅读器认识的位置。所有设备（网页播放器、手机 App、手表）共用同一份，
// 第几章第几句（n:i）就是一句语音的名字。
//
// 听书包：按“听书稿 + 声音设置”合成好的语音，存在 /data/listen/<文件>-<声音>/<章>/<句>.mp3（NAS 本地语音
// 合成的是 <句>.l.mp3）。一律 1 倍速，设备播放时自己变速（保持音调），改语速不用重做。
// 准备任务在 NAS 空闲时做（有人在听书时让开）：先用微软；微软连不上时交给电脑加速器（桌面端，主动来领活），
// 电脑也没开就用 NAS 本地语音慢慢补。设备要的那一句还没有时现合成（最后一招），合成好的也存进包里。
// 总大小有上限（默认 5GB，SHUKU_LISTEN_LIMIT_MB），超了先删最久没用的包；书读完了删这本的包。

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// ---------- 切句（和 android/common BookText 一样，改的时候两边一起改） ----------

func listenFormat(raw string) string {
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, "　　"+l)
		}
	}
	return strings.Join(out, "\n")
}

type listenSentence struct {
	Text       string
	Start, End int // 在排好的正文里的位置（按字算）
}

var (
	listenPartRe  = regexp.MustCompile("[^。！？!?；;…\\n]*[。！？!?；;…]+[”’」』）)\"'》]*[ \\t\\x{3000}]*|[^。！？!?；;…\\n]+|\\n+")
	listenCommaRe = regexp.MustCompile("[^，,、：:]*[，,、：:]+|[^，,、：:]+")
)

// 和 Kotlin 的 String.trim 一样：去掉两头的空白（包括全角空格）
func ktTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return r <= ' ' || unicode.IsSpace(r) })
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func listenSentences(text string) []listenSentence {
	// 按字（rune）算位置：先把字节位置换成字的位置
	byteToRune := func(b int) int { return utf8.RuneCountInString(text[:b]) }
	var raw []listenSentence
	var buf strings.Builder
	start, end := -1, 0
	for _, m := range listenPartRe.FindAllStringIndex(text, -1) {
		if start < 0 {
			start = byteToRune(m[0])
		}
		v := text[m[0]:m[1]]
		buf.WriteString(v)
		end = byteToRune(m[1])
		clean := ktTrim(buf.String())
		if runeLen(clean) >= 24 || strings.Contains(v, "\n") {
			if clean != "" {
				raw = append(raw, listenSentence{clean, start, end})
			}
			buf.Reset()
			start = -1
		}
	}
	if t := ktTrim(buf.String()); t != "" {
		if start < 0 {
			start = 0
		}
		raw = append(raw, listenSentence{t, start, end})
	}
	var out []listenSentence
	for _, s := range raw {
		if runeLen(s.Text) <= 180 {
			out = append(out, s)
			continue
		}
		off := s.Start
		var piece []rune
		flush := func(endAt int) {
			p := piece
			for len(p) > 200 {
				cut := 120
				// 没有逗号的长段：有空格就在空格处切（lastIndexOf(' ', 160)，至少 60）
				sp := -1
				for k := 160; k >= 0; k-- {
					if k < len(p) && p[k] == ' ' {
						sp = k
						break
					}
				}
				if sp >= 60 {
					cut = sp + 1
				}
				out = append(out, listenSentence{ktTrim(string(p[:cut])), off, off + cut})
				off += cut
				p = p[cut:]
			}
			if strings.TrimSpace(string(p)) != "" {
				out = append(out, listenSentence{ktTrim(string(p)), off, endAt})
			}
			off = endAt
			piece = nil
		}
		for _, m := range listenCommaRe.FindAllString(s.Text, -1) {
			piece = append(piece, []rune(m)...)
			if len(piece) >= 90 {
				e := off + len(piece)
				if s.End < e {
					e = s.End
				}
				flush(e)
			}
		}
		if len(piece) > 0 {
			flush(s.End)
		}
	}
	return out
}

// ---------- 听书稿 ----------

type scriptSentence struct {
	I    int     `json:"i"`
	Text string  `json:"text"`
	Loc  string  `json:"loc,omitempty"`
	Pct  float64 `json:"pct"`
}

type scriptChapter struct {
	N         int    `json:"n"`
	Title     string `json:"title"`
	Chars     int    `json:"chars"`
	Count     int    `json:"count"`
	sentences []scriptSentence
	audioSecs float64 // 估计能听多久（1 倍速，按每分钟 260 字）
}

type listenScript struct {
	FileID   int64
	Key      string
	Chapters []*scriptChapter
	Total    int // 全书字数
}

var scriptCache = struct {
	sync.Mutex
	m     map[string]*listenScript
	order []string
}{m: map[string]*listenScript{}}

// 第 cur 章里比例 frac 处的阅读位置（和 BookPos.location 一样）
func listenLoc(chs []WatchChapter, cur int, frac float64) string {
	loc := chs[cur].Loc
	next := ""
	if cur+1 < len(chs) {
		next = chs[cur+1].Loc
	}
	if strings.HasPrefix(loc, "sec:") {
		p := strings.Split(loc, ":")
		if len(p) < 2 {
			return loc
		}
		f0 := 0.0
		if len(p) > 2 {
			f0, _ = strconv.ParseFloat(p[2], 64)
		}
		f1 := 1.0
		if np := strings.Split(next, ":"); strings.HasPrefix(next, "sec:") && len(np) > 2 && np[1] == p[1] {
			f1, _ = strconv.ParseFloat(np[2], 64)
		}
		return fmt.Sprintf("sec:%s:%.4f", p[1], math.Max(0, math.Min(1, f0+frac*(f1-f0))))
	}
	if strings.HasPrefix(loc, "p:") {
		first, _ := strconv.Atoi(strings.Split(loc, ":")[1])
		page := first
		if np := strings.Split(next, ":"); strings.HasPrefix(next, "p:") && len(np) > 1 {
			if nf, _ := strconv.Atoi(np[1]); nf > first {
				page = first + int(math.Min(frac*float64(nf-first), float64(nf-first-1)))
			}
		}
		return fmt.Sprintf("p:%d:0", page)
	}
	return ""
}

func getListenScript(f *File) (*listenScript, error) {
	key := fmt.Sprintf("%d:%s", f.ID, f.Hash)
	scriptCache.Lock()
	if s := scriptCache.m[key]; s != nil {
		scriptCache.Unlock()
		return s, nil
	}
	scriptCache.Unlock()
	chs, err := bookChapters(f)
	if err != nil {
		return nil, err
	}
	if len(chs) == 0 {
		return nil, errors.New("这本书没有可以朗读的文字")
	}
	sc := &listenScript{FileID: f.ID, Key: key}
	total := chs[len(chs)-1].Start + chs[len(chs)-1].Chars
	sc.Total = total
	for n, c := range chs {
		text := listenFormat(c.text)
		l := runeLen(text)
		if l < 1 {
			l = 1
		}
		ch := &scriptChapter{N: n, Title: c.Title, Chars: c.Chars}
		for i, s := range listenSentences(text) {
			fr := float64(s.Start) / float64(l)
			pct := 0.0
			if total > 0 {
				pct = math.Max(0, math.Min(1, (float64(c.Start)+fr*float64(c.Chars))/float64(total)))
			}
			ch.sentences = append(ch.sentences, scriptSentence{I: i, Text: s.Text, Loc: listenLoc(chs, n, fr), Pct: math.Round(pct*10000) / 10000})
		}
		ch.Count = len(ch.sentences)
		ch.audioSecs = float64(c.Chars) / 260 * 60
		sc.Chapters = append(sc.Chapters, ch)
	}
	scriptCache.Lock()
	scriptCache.m[key] = sc
	scriptCache.order = append(scriptCache.order, key)
	if len(scriptCache.order) > 6 {
		delete(scriptCache.m, scriptCache.order[0])
		scriptCache.order = scriptCache.order[1:]
	}
	scriptCache.Unlock()
	return sc, nil
}

// ---------- 声音设置 ----------

// 每种语言用哪个微软音色。设备传它选过的（JSON），没传用书库的默认
type listenVoices map[string]string

func defaultListenVoices() listenVoices {
	v := listenVoices{}
	for k, x := range edgeDefaults() {
		v[k] = x
	}
	if c := ttsConfig(); c.Voice != "" && strings.HasPrefix(c.Voice, "zh-") {
		v["zh"] = c.Voice
	}
	return v
}

func parseListenVoices(s string) listenVoices {
	v := defaultListenVoices()
	var in map[string]string
	if s != "" && json.Unmarshal([]byte(s), &in) == nil {
		for k, x := range in {
			if strings.Contains(x, ":") { // nas: / ms: 这类前缀不算音色
				_, x, _ = strings.Cut(x, ":")
			}
			if k != "" && x != "" && len(k) <= 8 && len(x) <= 80 {
				v[k] = x
			}
		}
	}
	return v
}

func (v listenVoices) key() string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + v[k] + ";")
	}
	sum := sha1.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:10]
}

// 一句用哪个音色：看文字；拉丁字母按书的语言（不是拉丁字母的语言就用英语）
func (v listenVoices) forText(text, bookLang string) string {
	l := scriptLang(text)
	bl := strings.ToLower(strings.SplitN(bookLang, "-", 2)[0])
	switch l {
	case "", "latin":
		if bl != "" && !nonLatinLangs[bl] {
			l = bl
		} else {
			l = "en"
		}
	}
	if x := v[l]; x != "" {
		return x
	}
	if x := v["en"]; x != "" && l != "zh" {
		return x
	}
	return v["zh"]
}

// ---------- 听书包文件 ----------

func listenRoot() string { return filepath.Join(cfg.DataDir, "listen") }

func packDir(fileID int64, vkey string) string {
	return filepath.Join(listenRoot(), fmt.Sprintf("%d-%s", fileID, vkey))
}

func packFile(dir string, n, i int, local bool) string {
	name := strconv.Itoa(i) + ".mp3"
	if local {
		name = strconv.Itoa(i) + ".l.mp3"
	}
	return filepath.Join(dir, strconv.Itoa(n), name)
}

// 这一句准备好的语音（微软的优先）
func packExisting(dir string, n, i int) (string, bool) {
	for _, l := range []bool{false, true} {
		p := packFile(dir, n, i, l)
		if st, err := os.Stat(p); err == nil && st.Size() > 100 {
			return p, true
		}
	}
	return "", false
}

func writePack(p string, b []byte) error {
	os.MkdirAll(filepath.Dir(p), 0o755)
	tmp := p + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// MP3 有多长（秒）：读第一帧的码率（微软和本地语音都是固定码率），按文件大小算
func mp3Duration(p string) float64 {
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	st, _ := f.Stat()
	head := make([]byte, 4096)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	off := 0
	if len(head) >= 10 && string(head[:3]) == "ID3" {
		off = 10 + int(head[6]&0x7f)<<21 | int(head[7]&0x7f)<<14 | int(head[8]&0x7f)<<7 | int(head[9]&0x7f)
	}
	rates := map[[2]int][]int{
		{1, 3}: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},
		{2, 3}: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
	}
	for k := off; k+4 <= len(head); k++ {
		if head[k] != 0xFF || head[k+1]&0xE0 != 0xE0 {
			continue
		}
		ver := 1
		if (head[k+1]>>3)&3 != 3 {
			ver = 2 // MPEG 2 / 2.5
		}
		layer := 4 - int((head[k+1]>>1)&3)
		bi := int(head[k+2] >> 4)
		tbl := rates[[2]int{ver, layer}]
		if tbl == nil || bi <= 0 || bi >= len(tbl) {
			continue
		}
		return float64(st.Size()-int64(k)) * 8 / float64(tbl[bi]*1000)
	}
	// 读不出来：按 48kbps 估
	return float64(st.Size()) * 8 / 48000
}

// 一个包里各句的长度（秒，按需算、记住）
var packDur = struct {
	sync.Mutex
	m map[string]float64
}{m: map[string]float64{}}

func sentenceDur(p string) float64 {
	packDur.Lock()
	d, ok := packDur.m[p]
	packDur.Unlock()
	if ok {
		return d
	}
	d = mp3Duration(p)
	packDur.Lock()
	packDur.m[p] = d
	packDur.Unlock()
	return d
}

// ---------- 合成一句 ----------

var (
	lastLiveTTS   atomic.Int64 // 最近一次有人在听（正式合成）的时间：准备任务让开
	msDownUntil   atomic.Int64 // 微软这段时间连不上：先不试
	msSpeed       = newEWMA()  // 微软合成 1 秒语音要几秒（含网络）
	listenLocalWG sync.Mutex   // NAS 本地语音一次只做一句准备
)

type ewma struct {
	sync.Mutex
	v float64
}

func newEWMA() *ewma { return &ewma{} }
func (e *ewma) add(x float64) {
	e.Lock()
	if e.v == 0 {
		e.v = x
	} else {
		e.v = e.v*0.8 + x*0.2
	}
	e.Unlock()
}
func (e *ewma) get() float64 { e.Lock(); defer e.Unlock(); return e.v }

func markLive() { lastLiveTTS.Store(time.Now().UnixMilli()) }

func msAvailable() bool {
	c := ttsConfig()
	// SHUKU_LISTEN_NO_MS=1：当作微软连不上（测电脑加速、NAS 本地兜底用）
	return c.Engine == "edge" && time.Now().UnixMilli() > msDownUntil.Load() && os.Getenv("SHUKU_LISTEN_NO_MS") != "1"
}

// 微软合成一句存进包（1 倍速）
func packSynthMS(ctx context.Context, dir string, n, i int, text, voice string) (string, error) {
	t0 := time.Now()
	b, err := edgeSynth(ctx, text, adaptVoice(text, voice, ""), 1)
	if err != nil {
		if isNetErr(err) || errors.Is(err, context.DeadlineExceeded) {
			msDownUntil.Store(time.Now().Add(3 * time.Minute).UnixMilli())
		}
		return "", err
	}
	p := packFile(dir, n, i, false)
	if err := writePack(p, b); err != nil {
		return "", err
	}
	if d := sentenceDur(p); d > 0.5 {
		msSpeed.add(time.Since(t0).Seconds() / d)
	}
	return p, nil
}

// NAS 本地语音合成一句存进包（只会中文、英文）
func packSynthLocal(ctx context.Context, dir string, n, i int, text string) (string, error) {
	if !localTTSStatus().OK || ttsConfig().Local == "off" {
		return "", errors.New("NAS 本地语音没开")
	}
	b, err := localSynth(ctx, text, 1)
	if err != nil {
		return "", err
	}
	p := packFile(dir, n, i, true)
	return p, writePack(p, b)
}

func localCanRead(text, bookLang string) bool {
	switch scriptLang(text) {
	case "zh", "":
		return true
	case "latin":
		bl := strings.ToLower(bookLang)
		return bl == "" || strings.HasPrefix(bl, "en") || strings.HasPrefix(bl, "zh")
	}
	return false
}

// ---------- 准备任务 ----------

type listenPos struct {
	N int `json:"n"`
	I int `json:"i"`
}

func (a listenPos) before(b listenPos) bool { return a.N < b.N || (a.N == b.N && a.I < b.I) }

type listenJob struct {
	ID       string       `json:"id"` // 文件-声音-人
	UserID   int64        `json:"user_id"`
	BookID   int64        `json:"book_id"`
	FileID   int64        `json:"file_id"`
	Title    string       `json:"title"`
	Lang     string       `json:"lang"`
	Voices   listenVoices `json:"voices"`
	From     listenPos    `json:"from"`
	Minutes  float64      `json:"minutes"` // 从 From 往后准备多久；0 表示到 End
	End      *listenPos   `json:"end,omitempty"`
	Scope    string       `json:"scope"` // auto（听书时自动）/ minutes / half / all
	Paused   bool         `json:"paused"`
	Updated  int64        `json:"updated"`
	Engine   string       `json:"engine,omitempty"` // 现在在用哪个合成：ms / pc / nas
	LastErr  string       `json:"last_error,omitempty"`
	Finished bool         `json:"finished"`
}

var listenJobs = struct {
	sync.Mutex
	m    map[string]*listenJob
	wake chan struct{}
}{m: map[string]*listenJob{}, wake: make(chan struct{}, 1)}

func jobsFile() string { return filepath.Join(listenRoot(), "jobs.json") }

func loadListenJobs() {
	b, err := os.ReadFile(jobsFile())
	if err != nil {
		return
	}
	var list []*listenJob
	if json.Unmarshal(b, &list) == nil {
		listenJobs.Lock()
		for _, j := range list {
			listenJobs.m[j.ID] = j
		}
		listenJobs.Unlock()
	}
}

func saveListenJobsLocked() {
	list := make([]*listenJob, 0, len(listenJobs.m))
	for _, j := range listenJobs.m {
		list = append(list, j)
	}
	os.MkdirAll(listenRoot(), 0o755)
	b, _ := json.Marshal(list)
	os.WriteFile(jobsFile()+".tmp", b, 0o644)
	os.Rename(jobsFile()+".tmp", jobsFile())
}

func wakeListen() {
	select {
	case listenJobs.wake <- struct{}{}:
	default:
	}
}

// 从 from 往后数：已经准备好的（连着的）能听多久、要准备到哪里、还差几句
type packStatus struct {
	ReadySecs  float64 `json:"ready_secs"`  // 从当前位置往后连着准备好的
	TargetSecs float64 `json:"target_secs"` // 要准备的总长（估计）
	Bytes      int64   `json:"bytes"`
	Missing    int     `json:"missing"` // 范围里还没准备的句数
	DoneSecs   float64 `json:"done_secs"`
}

// 准备范围里的每一句（按顺序），f 返回 false 停
func walkRange(sc *listenScript, from listenPos, end *listenPos, minutes float64, f func(n, i int, s scriptSentence) bool) {
	limitSecs := minutes * 60
	acc := 0.0
	for n := from.N; n < len(sc.Chapters); n++ {
		ch := sc.Chapters[n]
		i0 := 0
		if n == from.N {
			i0 = from.I
		}
		for i := i0; i < len(ch.sentences); i++ {
			if end != nil && !(listenPos{n, i}).before(*end) {
				return
			}
			if end == nil && limitSecs > 0 && acc >= limitSecs {
				return
			}
			s := ch.sentences[i]
			acc += float64(runeLen(s.Text)) / 260 * 60
			if !f(n, i, s) {
				return
			}
		}
	}
}

func jobStatus(j *listenJob, sc *listenScript) packStatus {
	dir := packDir(j.FileID, j.Voices.key())
	var st packStatus
	contiguous := true
	walkRange(sc, j.From, j.End, j.Minutes, func(n, i int, s scriptSentence) bool {
		est := float64(runeLen(s.Text)) / 260 * 60
		st.TargetSecs += est
		if p, ok := packExisting(dir, n, i); ok {
			d := sentenceDur(p)
			st.DoneSecs += d
			if contiguous {
				st.ReadySecs += d
			}
			if fi, err := os.Stat(p); err == nil {
				st.Bytes += fi.Size()
			}
		} else {
			contiguous = false
			st.Missing++
		}
		return true
	})
	return st
}

// ---------- 电脑加速器 ----------

type pcWorker struct {
	Name     string  `json:"name"`
	RTF      float64 `json:"rtf"`
	Seen     int64   `json:"seen"`
	Done     int     `json:"done"`
	Busy     bool    `json:"busy"` // 电脑正在用（降速）
	Version  string  `json:"version"`
	DeviceID int64   `json:"-"`
}

type pcTask struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	dir    string
	n, i   int
	issued time.Time
}

var pc = struct {
	sync.Mutex
	workers map[string]*pcWorker
	queue   []*pcTask          // 等电脑来领的
	out     map[string]*pcTask // 领走了还没交回的
}{workers: map[string]*pcWorker{}, out: map[string]*pcTask{}}

// 最近一分钟内来过的电脑
func pcOnline() *pcWorker {
	pc.Lock()
	defer pc.Unlock()
	var best *pcWorker
	for _, w := range pc.workers {
		if time.Since(time.UnixMilli(w.Seen)) < time.Minute && (best == nil || w.RTF < best.RTF) {
			best = w
		}
	}
	return best
}

// 这一句已经交给电脑、还在等它交回来（领走 3 分钟内；排着队的只在电脑还开着时算）
func pcPending(dir string, n, i int) bool {
	id := fmt.Sprintf("%s|%d|%d", dir, n, i)
	online := pcOnline() != nil
	pc.Lock()
	defer pc.Unlock()
	if !online && len(pc.queue) > 0 {
		pc.queue = nil // 电脑关了：排着的不等了，NAS 自己做
	}
	if t, ok := pc.out[id]; ok && time.Since(t.issued) < 3*time.Minute {
		return true
	}
	for _, t := range pc.queue {
		if t.ID == id {
			return true
		}
	}
	return false
}

func pcEnqueue(dir string, n, i int, text string) {
	if pcPending(dir, n, i) {
		return
	}
	pc.Lock()
	pc.queue = append(pc.queue, &pcTask{ID: fmt.Sprintf("%s|%d|%d", dir, n, i), Text: text, dir: dir, n: n, i: i})
	if len(pc.queue) > 400 {
		pc.queue = pc.queue[len(pc.queue)-400:]
	}
	pc.Unlock()
}

// ---------- 后台准备 ----------

func listenLoop(ctx context.Context) {
	loadListenJobs()
	go listenTrimLoop(ctx)
	for {
		did := listenStep(ctx)
		wait := 2 * time.Second
		if did {
			wait = 50 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-listenJobs.wake:
		case <-time.After(wait):
		}
	}
}

// 做一步：挑一个没准备完的任务，合成下一句（或者交给电脑）。做了事返回 true
func listenStep(ctx context.Context) bool {
	// 有人在听（正式合成）时让开
	if time.Now().UnixMilli()-lastLiveTTS.Load() < 6000 {
		return false
	}
	c := ttsConfig()
	if c.Engine != "edge" {
		return false
	}
	listenJobs.Lock()
	jobs := make([]*listenJob, 0, len(listenJobs.m))
	for _, j := range listenJobs.m {
		if !j.Paused && !j.Finished {
			jobs = append(jobs, j)
		}
	}
	listenJobs.Unlock()
	// 听书时自动的先做（正在听），再按最近改过的
	sort.Slice(jobs, func(a, b int) bool {
		if (jobs[a].Scope == "auto") != (jobs[b].Scope == "auto") {
			return jobs[a].Scope == "auto"
		}
		return jobs[a].Updated > jobs[b].Updated
	})
	for _, j := range jobs {
		f := fileByID(j.FileID)
		if f == nil {
			continue
		}
		sc, err := getListenScript(f)
		if err != nil {
			continue
		}
		dir := packDir(j.FileID, j.Voices.key())
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "voices.json"), mustJSON(j.Voices), 0o644)
		// 找范围里第一句还没准备的（交给电脑还没回来的跳过）
		var todo *struct {
			n, i int
			s    scriptSentence
		}
		walkRange(sc, j.From, j.End, j.Minutes, func(n, i int, s scriptSentence) bool {
			if _, ok := packExisting(dir, n, i); ok || pcPending(dir, n, i) {
				return true
			}
			todo = &struct {
				n, i int
				s    scriptSentence
			}{n, i, s}
			return false
		})
		if todo == nil {
			// 剩下的都在等电脑交回来：先不算做完
			if jobStatus(j, sc).Missing > 0 {
				continue
			}
			listenJobs.Lock()
			if j.Scope != "auto" && !j.Finished {
				j.Finished = true
				saveListenJobsLocked()
			}
			listenJobs.Unlock()
			continue
		}
		voice := j.Voices.forText(todo.s.Text, j.Lang)
		engine := ""
		var serr error
		if msAvailable() {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, serr = packSynthMS(cctx, dir, todo.n, todo.i, todo.s.Text, voice)
			cancel()
			engine = "ms"
		}
		if engine == "" || serr != nil {
			// 微软不行：电脑开着就交给电脑（一次交一批），不然 NAS 本地慢慢做
			if !localCanRead(todo.s.Text, j.Lang) {
				// 本地读不了的语言（泰文等）：只能等微软
				setJobEngine(j, "ms", errText(serr, "等网络（这种语言只能用微软合成）"))
				continue
			}
			if w := pcOnline(); w != nil {
				n := 0
				walkRange(sc, listenPos{todo.n, todo.i}, j.End, math.Max(10, j.Minutes), func(nn, ii int, s scriptSentence) bool {
					if _, ok := packExisting(dir, nn, ii); !ok && localCanRead(s.Text, j.Lang) {
						pcEnqueue(dir, nn, ii, s.Text)
						n++
					}
					return n < 40
				})
				setJobEngine(j, "pc", "")
				return false // 等电脑交回来，不空转
			}
			listenLocalWG.Lock()
			cctx, cancel := context.WithTimeout(context.WithValue(ctx, prefetchCtxKey{}, true), 3*time.Minute)
			_, lerr := packSynthLocal(cctx, dir, todo.n, todo.i, todo.s.Text)
			cancel()
			listenLocalWG.Unlock()
			if lerr != nil {
				setJobEngine(j, "nas", errText(lerr, ""))
				continue
			}
			setJobEngine(j, "nas", "")
			return true
		}
		setJobEngine(j, "ms", "")
		return true
	}
	return false
}

func errText(err error, def string) string {
	if err != nil {
		return simplifyNetErr(err)
	}
	return def
}

func setJobEngine(j *listenJob, e, errMsg string) {
	listenJobs.Lock()
	j.Engine, j.LastErr = e, errMsg
	listenJobs.Unlock()
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func fileByID(id int64) *File {
	var bookID int64
	if db.QueryRow(`SELECT book_id FROM files WHERE id=?`, id).Scan(&bookID) != nil {
		return nil
	}
	for _, x := range bookFiles(bookID) {
		if x.ID == id {
			return &x
		}
	}
	return nil
}

// ---------- 清理：总大小上限，读完的书删掉 ----------

func listenLimit() int64 {
	if mb, _ := strconv.ParseInt(os.Getenv("SHUKU_LISTEN_LIMIT_MB"), 10, 64); mb > 0 {
		return mb << 20
	}
	return 5 << 30
}

func listenTrimLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
		trimListen()
	}
}

func trimListen() {
	// 读完的书：这本的任务和包删掉
	listenJobs.Lock()
	for id, j := range listenJobs.m {
		var state string
		db.QueryRow(`SELECT read_state FROM user_books WHERE user_id=? AND book_id=?`, j.UserID, j.BookID).Scan(&state)
		var deleted int64
		db.QueryRow(`SELECT deleted_at FROM books WHERE id=?`, j.BookID).Scan(&deleted)
		if state == "done" || deleted > 0 {
			os.RemoveAll(packDir(j.FileID, j.Voices.key()))
			delete(listenJobs.m, id)
		}
	}
	saveListenJobsLocked()
	active := map[string]bool{}
	for _, j := range listenJobs.m {
		active[packDir(j.FileID, j.Voices.key())] = true
	}
	listenJobs.Unlock()
	// 超过上限：先删没有任务的包，再按最近用过的时间删最旧的
	type pk struct {
		dir  string
		size int64
		t    time.Time
	}
	var packs []pk
	var total int64
	entries, _ := os.ReadDir(listenRoot())
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d := filepath.Join(listenRoot(), e.Name())
		var size int64
		var newest time.Time
		filepath.Walk(d, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				size += info.Size()
				if info.ModTime().After(newest) {
					newest = info.ModTime()
				}
			}
			return nil
		})
		packs = append(packs, pk{d, size, newest})
		total += size
	}
	limit := listenLimit()
	if total <= limit {
		return
	}
	sort.Slice(packs, func(a, b int) bool {
		if active[packs[a].dir] != active[packs[b].dir] {
			return !active[packs[a].dir]
		}
		return packs[a].t.Before(packs[b].t)
	})
	for _, p := range packs {
		if total <= limit*9/10 {
			break
		}
		os.RemoveAll(p.dir)
		total -= p.size
		log.Printf("听书包超过上限，删掉 %s", filepath.Base(p.dir))
	}
}

// ---------- 接口 ----------

func listenBook(w http.ResponseWriter, r *http.Request) (*Book, *File, *listenScript, bool) {
	b, f, _, ok := chapterBook(w, r)
	if !ok {
		return nil, nil, nil, false
	}
	sc, err := getListenScript(f)
	if err != nil {
		errJSON(w, 400, err.Error())
		return nil, nil, nil, false
	}
	return b, f, sc, true
}

// 读到哪一句：阅读器给的位置（loc/pct），没给就按存的进度
func listenResume(r *http.Request, b *Book, f *File, sc *listenScript) listenPos {
	chs, _ := bookChapters(f)
	n, frac := aiReadPoint(r, b, f, chs)
	if n >= len(sc.Chapters) {
		return listenPos{}
	}
	ch := sc.Chapters[n]
	// 这一章里第一句开头不早于 frac 的
	text := listenFormat(chs[n].text)
	l := runeLen(text)
	target := int(frac * float64(l))
	sents := listenSentences(text)
	for i, s := range sents {
		if s.End > target {
			return listenPos{n, i}
		}
	}
	if n+1 < len(sc.Chapters) {
		return listenPos{n + 1, 0}
	}
	return listenPos{n, max(0, ch.Count-1)}
}

func jobID(fileID int64, vkey string, uid int64) string { return fmt.Sprintf("%d-%s-%d", fileID, vkey, uid) }

// 各种合成方式合成 1 秒语音要几秒（给“准备要多久”的提示）
func engineSpeeds() map[string]any {
	out := map[string]any{"ms": msSpeed.get(), "ms_ok": msAvailable()}
	if l := localTTSStatus(); l.OK {
		out["nas"] = l.RTF
	}
	if w := pcOnline(); w != nil {
		out["pc"] = w.RTF
		out["pc_name"] = w.Name
	}
	return out
}

// GET /api/books/{id}/listen?voices=&loc=&pct=：听书稿目录、从哪句开始、准备情况
func handleListenInfo(w http.ResponseWriter, r *http.Request) {
	b, f, sc, ok := listenBook(w, r)
	if !ok {
		return
	}
	voices := parseListenVoices(r.URL.Query().Get("voices"))
	vkey := voices.key()
	resume := listenResume(r, b, f, sc)
	chs := make([]map[string]any, 0, len(sc.Chapters))
	for _, c := range sc.Chapters {
		chs = append(chs, map[string]any{"n": c.N, "title": c.Title, "count": c.Count, "chars": c.Chars, "secs": math.Round(c.audioSecs)})
	}
	out := map[string]any{
		"book_id": b.ID, "file_id": f.ID, "title": b.Title, "author": b.Author, "lang": b.Language, "has_cover": b.Cover != "",
		"voices_key": vkey, "chapters": chs, "resume": resume, "total_chars": sc.Total,
		"total_secs": math.Round(float64(sc.Total) / 260 * 60), "engines": engineSpeeds(),
	}
	listenJobs.Lock()
	j := listenJobs.m[jobID(f.ID, vkey, meID(r))]
	var jc listenJob
	if j != nil {
		jc = *j
	}
	listenJobs.Unlock()
	if j != nil {
		out["job"] = jc
		out["status"] = jobStatus(&jc, sc)
	} else {
		// 没有任务：从读到的地方往后已经准备好多少
		tmp := listenJob{FileID: f.ID, Voices: voices, From: resume, Minutes: 600}
		st := jobStatus(&tmp, sc)
		st.TargetSecs = 0
		out["status"] = st
	}
	writeJSON(w, 200, out)
}

// GET /api/books/{id}/listen/{n}?voices=：这一章的句子、哪句准备好了
func handleListenChapter(w http.ResponseWriter, r *http.Request) {
	_, f, sc, ok := listenBook(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 || n >= len(sc.Chapters) {
		errJSON(w, 404, "没有这一章")
		return
	}
	dir := packDir(f.ID, parseListenVoices(r.URL.Query().Get("voices")).key())
	ch := sc.Chapters[n]
	list := make([]map[string]any, 0, len(ch.sentences))
	for _, s := range ch.sentences {
		item := map[string]any{"i": s.I, "text": s.Text, "loc": s.Loc, "pct": s.Pct}
		if p, ok := packExisting(dir, n, s.I); ok {
			item["dur"] = math.Round(sentenceDur(p)*100) / 100
		}
		list = append(list, item)
	}
	writeJSON(w, 200, map[string]any{"n": n, "title": ch.Title, "sentences": list, "count": len(list)})
}

// GET /api/books/{id}/listen/{n}/{i}?voices=：一句的语音（1 倍速）。准备好了直接给；没有就现合成（微软，不行用 NAS 本地），也存进包里
func handleListenAudio(w http.ResponseWriter, r *http.Request) {
	b, f, sc, ok := listenBook(w, r)
	if !ok {
		return
	}
	n, err1 := strconv.Atoi(r.PathValue("n"))
	i, err2 := strconv.Atoi(strings.TrimSuffix(r.PathValue("i"), ".mp3"))
	if err1 != nil || err2 != nil || n < 0 || n >= len(sc.Chapters) || i < 0 || i >= sc.Chapters[n].Count {
		errJSON(w, 404, "没有这一句")
		return
	}
	voices := parseListenVoices(r.URL.Query().Get("voices"))
	dir := packDir(f.ID, voices.key())
	p, ok := packExisting(dir, n, i)
	if !ok {
		// 现合成：正在听，准备任务让开
		markLive()
		s := sc.Chapters[n].sentences[i]
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		var err error
		if msAvailable() {
			mctx, mc := context.WithTimeout(ctx, 15*time.Second)
			p, err = packSynthMS(mctx, dir, n, i, s.Text, voices.forText(s.Text, b.Language))
			mc()
		} else {
			err = errors.New("微软暂时连不上")
		}
		if err != nil && localCanRead(s.Text, b.Language) {
			p, err = packSynthLocal(ctx, dir, n, i, s.Text)
		}
		if err != nil {
			errJSON(w, 502, "语音合成失败："+simplifyNetErr(err))
			return
		}
	}
	now := time.Now()
	os.Chtimes(filepath.Dir(p), now, now)
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "private, max-age=604800")
	w.Header().Set("X-Duration", strconv.FormatFloat(sentenceDur(p), 'f', 2, 64))
	http.ServeFile(w, r, p)
}

// POST /api/books/{id}/listen/prepare {voices, from:{n,i}, minutes, scope: auto|minutes|half|all}
// 新建或更新这个人这本书的准备任务。auto：听书时设备每隔一会儿报一次位置和要准备多久
func handleListenPrepare(w http.ResponseWriter, r *http.Request) {
	b, f, sc, ok := listenBook(w, r)
	if !ok {
		return
	}
	var req struct {
		Voices  string     `json:"voices"`
		From    *listenPos `json:"from"`
		Minutes float64    `json:"minutes"`
		Scope   string     `json:"scope"`
		Pause   *bool      `json:"pause"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "格式不正确")
		return
	}
	voices := parseListenVoices(req.Voices)
	id := jobID(f.ID, voices.key(), meID(r))
	from := listenResume(r, b, f, sc)
	if req.From != nil && req.From.N >= 0 && req.From.N < len(sc.Chapters) {
		from = *req.From
	}
	j := &listenJob{ID: id, UserID: meID(r), BookID: b.ID, FileID: f.ID, Title: b.Title, Lang: b.Language, Voices: voices, From: from, Scope: req.Scope}
	switch req.Scope {
	case "all":
		j.End = &listenPos{len(sc.Chapters), 0}
	case "half":
		// 到全书一半的那一章
		acc, half := 0, sc.Total/2
		for _, c := range sc.Chapters {
			acc += c.Chars
			if acc >= half || c.N == len(sc.Chapters)-1 {
				end := listenPos{c.N + 1, 0}
				if !from.before(end) {
					end = listenPos{len(sc.Chapters), 0}
				}
				j.End = &end
				break
			}
		}
	default:
		if req.Scope != "auto" {
			j.Scope = "minutes"
		}
		j.Minutes = math.Max(30, math.Min(24*60, req.Minutes))
	}
	listenJobs.Lock()
	if old := listenJobs.m[id]; old != nil {
		// 听书时自动报的（auto）不覆盖手动选的更大范围：只往后挪起点
		if req.Scope == "auto" && old.Scope != "auto" && !old.Finished {
			old.From = from
			old.Updated = time.Now().Unix()
			j = old
		}
		if req.Pause != nil {
			j.Paused = *req.Pause
		}
	}
	j.Updated = time.Now().Unix()
	listenJobs.m[id] = j
	saveListenJobsLocked()
	jc := *j
	listenJobs.Unlock()
	wakeListen()
	writeJSON(w, 200, map[string]any{"job": jc, "status": jobStatus(&jc, sc), "engines": engineSpeeds()})
}

// DELETE /api/books/{id}/listen/prepare?voices=：取消准备（已经准备好的留着，按上限清理）
func handleListenCancel(w http.ResponseWriter, r *http.Request) {
	b, ok := visibleBook(w, r, pathID(r, "id"))
	if !ok {
		return
	}
	voices := parseListenVoices(r.URL.Query().Get("voices"))
	listenJobs.Lock()
	for id, j := range listenJobs.m {
		if j.BookID == b.ID && j.UserID == meID(r) && j.Voices.key() == voices.key() {
			delete(listenJobs.m, id)
		}
	}
	saveListenJobsLocked()
	listenJobs.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true})
}

// GET /api/listen/jobs：这个人所有的准备任务和进度（设置页、书的详情用）
func handleListenJobs(w http.ResponseWriter, r *http.Request) {
	listenJobs.Lock()
	var mine []listenJob
	for _, j := range listenJobs.m {
		if j.UserID == meID(r) {
			mine = append(mine, *j)
		}
	}
	listenJobs.Unlock()
	out := []map[string]any{}
	for _, j := range mine {
		f := fileByID(j.FileID)
		if f == nil {
			continue
		}
		sc, err := getListenScript(f)
		if err != nil {
			continue
		}
		out = append(out, map[string]any{"job": j, "status": jobStatus(&j, sc)})
	}
	pc.Lock()
	workers := []pcWorker{}
	for _, wk := range pc.workers {
		workers = append(workers, *wk)
	}
	pc.Unlock()
	writeJSON(w, 200, map[string]any{"jobs": out, "engines": engineSpeeds(), "workers": workers})
}

// POST /api/listen/worker/next {name, rtf, version, busy, max}：电脑加速器来领活（顺便报到）
func handleWorkerNext(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string  `json:"name"`
		RTF     float64 `json:"rtf"`
		Version string  `json:"version"`
		Busy    bool    `json:"busy"`
		Max     int     `json:"max"`
	}
	if readJSON(r, &req) != nil || req.Name == "" {
		errJSON(w, 400, "格式不正确")
		return
	}
	if req.Max <= 0 || req.Max > 20 {
		req.Max = 5
	}
	pc.Lock()
	wk := pc.workers[req.Name]
	if wk == nil {
		wk = &pcWorker{Name: req.Name}
		pc.workers[req.Name] = wk
	}
	wk.Seen, wk.RTF, wk.Version, wk.Busy = time.Now().UnixMilli(), req.RTF, req.Version, req.Busy
	var tasks []*pcTask
	for len(pc.queue) > 0 && len(tasks) < req.Max {
		t := pc.queue[0]
		pc.queue = pc.queue[1:]
		if _, ok := packExisting(t.dir, t.n, t.i); ok {
			continue
		}
		t.issued = time.Now()
		pc.out[t.ID] = t
		tasks = append(tasks, t)
	}
	pc.Unlock()
	if tasks == nil {
		tasks = []*pcTask{}
	}
	writeJSON(w, 200, map[string]any{"tasks": tasks, "wait": ifs(len(tasks) == 0, "5", "0")})
}

// POST /api/listen/worker/done?id=…：电脑合成好了一句（body 是 MP3）
func handleWorkerDone(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	name := r.URL.Query().Get("name")
	pc.Lock()
	t := pc.out[id]
	delete(pc.out, id)
	if wk := pc.workers[name]; wk != nil {
		wk.Seen = time.Now().UnixMilli()
	}
	pc.Unlock()
	if t == nil {
		errJSON(w, 404, "这一句已经不用了")
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil || len(b) < 200 {
		errJSON(w, 400, "语音不完整")
		return
	}
	if err := writePack(packFile(t.dir, t.n, t.i, true), b); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	pc.Lock()
	if wk := pc.workers[name]; wk != nil {
		wk.Done++
	}
	pc.Unlock()
	wakeListen()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func routesListen(mux *http.ServeMux) {
	a := requireAuth
	mux.HandleFunc("GET /api/books/{id}/listen", a(handleListenInfo))
	mux.HandleFunc("GET /api/books/{id}/listen/{n}", a(handleListenChapter))
	mux.HandleFunc("GET /api/books/{id}/listen/{n}/{i}", a(handleListenAudio))
	mux.HandleFunc("POST /api/books/{id}/listen/prepare", a(handleListenPrepare))
	mux.HandleFunc("DELETE /api/books/{id}/listen/prepare", a(handleListenCancel))
	mux.HandleFunc("GET /api/listen/jobs", a(handleListenJobs))
	mux.HandleFunc("POST /api/listen/worker/next", a(handleWorkerNext))
	mux.HandleFunc("POST /api/listen/worker/done", a(handleWorkerDone))
}
