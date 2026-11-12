// 书库 Shuku —— NAS 电子书管理与阅读服务端
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Config struct {
	Port    string
	DataDir string
	Inbox   string
	WebDir  string
	// 初始书库，格式："图书:/books:book,漫画:/comics:comic"，只在首次启动时写入数据库
	InitLibraries string
}

var cfg Config

// 版本号：发布时由 GitHub Actions 用 -ldflags "-X main.version=..." 写入，和 App 的版本号一致
var version = "1.0.0"

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func main() {
	// 生成本地字典（发布时在 GitHub Actions 里用）：shuku -build-dict 输出文件 -moe … -cedict … -ecdict …
	if len(os.Args) > 1 && os.Args[1] == "-build-dict" {
		buildDictCLI(os.Args[2:])
		return
	}
	// 容器健康检查：shuku -health
	if len(os.Args) > 1 && os.Args[1] == "-health" {
		c := http.Client{Timeout: 4 * time.Second}
		r, err := c.Get("http://127.0.0.1:" + env("SHUKU_PORT", "8080") + "/api/ping")
		if err != nil || r.StatusCode != 200 {
			os.Exit(1)
		}
		os.Exit(0)
	}
	cfg = Config{
		Port:          env("SHUKU_PORT", "8080"),
		DataDir:       env("SHUKU_DATA", "./data"),
		Inbox:         env("SHUKU_INBOX", "./inbox"),
		WebDir:        env("SHUKU_WEB", "./web"),
		InitLibraries: env("SHUKU_LIBRARIES", "图书:./books:book,漫画:./comics:comic"),
	}
	// 联网服务地址可以换成镜像或本地模拟服务（测试用）
	for k, p := range map[string]*string{"SHUKU_DOUBAN_BASE": &doubanBase, "SHUKU_OPENLIBRARY_BASE": &olBase, "SHUKU_OPENLIBRARY_COVERS": &olCoverBase,
		"SHUKU_GOOGLE_BASE": &googleBase, "SHUKU_BANGUMI_BASE": &bangumiBase, "SHUKU_EDGE_WSS": &edgeWSS,
		"SHUKU_MS_TRANSLATE_AUTH": &msTranslateAuth, "SHUKU_MS_TRANSLATE_BASE": &msTranslateBase} {
		if v := env(k, ""); v != "" {
			*p = strings.TrimRight(v, "/")
		}
	}
	tesseractBin = env("SHUKU_TESSERACT", "tesseract")
	for _, d := range []string{cfg.DataDir, cfg.Inbox, filepath.Join(cfg.DataDir, "covers"), filepath.Join(cfg.DataDir, "cache")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatalf("创建目录 %s 失败: %v", d, err)
		}
	}
	log.Printf("书库 %s 启动中…", version)
	if err := openDB(filepath.Join(cfg.DataDir, "shuku.db")); err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	resetPasswordFromEnv()
	initLibraries()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// 恢复备份后要重启：正常关掉（数据库收尾），退出后 Docker 按 restart 设置自动拉起来
	var restarting atomic.Bool
	restartHook = func() {
		restarting.Store(true)
		stop()
	}

	appCtx = ctx
	var wg sync.WaitGroup
	for _, loop := range []func(context.Context){inboxLoop, scanLoop, fulltextLoop, backupLoop, purgeLoop, suggestionsLoop, ttsEmbedLoop} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loop(ctx)
		}()
	}

	startTTSPrefetch() // 听书时在后台提前合成后面的句子

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           routes(),
		ReadHeaderTimeout: 20 * time.Second,
	}
	go func() {
		log.Printf("书库 %s 已启动: http://0.0.0.0:%s  数据目录=%s  投递目录=%s", version, cfg.Port, cfg.DataDir, cfg.Inbox)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	log.Printf("书库正在停止…")
	// docker stop 默认等 10 秒：网页请求最多等 4 秒，后台任务最多再等 3 秒，然后关数据库
	sh, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	srv.Shutdown(sh)
	cancel()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		log.Printf("后台任务没有及时停下，先关数据库")
	}
	db.Close()
	if restarting.Load() {
		log.Printf("书库重启")
		os.Exit(3) // 非 0：restart 设成 on-failure 时也会重启
	}
	log.Printf("书库已停止")
}
