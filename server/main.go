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

const version = "0.1.0"

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func main() {
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
	for _, d := range []string{cfg.DataDir, cfg.Inbox, filepath.Join(cfg.DataDir, "covers"), filepath.Join(cfg.DataDir, "cache")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatalf("创建目录 %s 失败: %v", d, err)
		}
	}
	if err := openDB(filepath.Join(cfg.DataDir, "shuku.db")); err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	initLibraries()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	appCtx = ctx
	go inboxLoop(ctx)
	go scanLoop(ctx)
	go fulltextLoop(ctx)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           routes(),
		ReadHeaderTimeout: 20 * time.Second,
	}
	go func() {
		log.Printf("书库已启动: http://0.0.0.0:%s  数据目录=%s  投递目录=%s", cfg.Port, cfg.DataDir, cfg.Inbox)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	sh, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(sh)
}
