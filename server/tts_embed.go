package main

// 书库镜像里带着的本地语音（Dockerfile 的 tts 段：/opt/tts 里的 Python 环境和 MeloTTS 模型，程序 /app/tts/server.py）。
// 书库启动后自己在后台开着它，只听本机 127.0.0.1:27210，tts_local.go 自动发现、中文英文先用它。
// “设置 → 听书 → NAS 本地语音”选了“不用”（或者语音引擎不是微软）就关掉，省内存；进程退出了隔一会儿重开。
// 已经有别的程序在 27210 端口上提供本地语音（比如以前单独装的 shuku-tts 容器）就不开。

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	embedPython = "/opt/tts/venv/bin/python"
	embedScript = "/app/tts/server.py"
	embedModel  = "/opt/tts/models/vits-melo-tts-zh_en"
)

func embeddedTTSAvailable() bool {
	for _, p := range []string{embedPython, embedScript, filepath.Join(embedModel, "model.onnx")} {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return os.Getenv("SHUKU_TTS_EMBED") != "0"
}

func embeddedTTSWanted() bool {
	c := ttsConfig()
	return c.Engine == "edge" && c.Local != "off"
}

func ttsEmbedLoop(ctx context.Context) {
	if !embeddedTTSAvailable() {
		return
	}
	var cmd *exec.Cmd
	var exited chan struct{}
	var startedAt time.Time
	stop := func() {
		if cmd != nil && cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
				<-exited
			}
		}
		cmd = nil
	}
	defer stop()
	backoff := 10 * time.Second
	for {
		running := cmd != nil
		if running {
			select {
			case <-exited:
				log.Printf("本地语音退出了，%s后重开", backoff)
				cmd = nil
				running = false
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < 10*time.Minute {
					backoff *= 2
				}
			default:
			}
		}
		want := embeddedTTSWanted()
		switch {
		case want && !running:
			// 别的程序已经在这个端口上提供本地语音（单独装的 shuku-tts）：用它，不再开一个
			if localTTSStatus().OK {
				break
			}
			threads := os.Getenv("SHUKU_TTS_THREADS")
			if threads == "" {
				threads = "3" // NAS 是 4 核：留一个核给书库和别的程序
			}
			c := exec.Command(embedPython, embedScript)
			c.Env = append(os.Environ(), "TTS_HOST=127.0.0.1", "TTS_PORT="+strings.TrimPrefix(localTTSPort(), ":"),
				"TTS_THREADS="+threads, "TTS_MODEL_DIR="+embedModel, "TTS_VERSION="+version, "PYTHONUNBUFFERED=1")
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := c.Start(); err != nil {
				log.Printf("本地语音开不起来：%v", err)
				break
			}
			// 比书库低一档的优先级：合成时不抢书库自己的 CPU
			syscall.Setpriority(syscall.PRIO_PROCESS, c.Process.Pid, 5)
			cmd = c
			startedAt = time.Now()
			exited = make(chan struct{})
			go func(c *exec.Cmd, ch chan struct{}) { c.Wait(); close(ch) }(c, exited)
			log.Printf("本地语音已开（MeloTTS 中英，%s 线程，127.0.0.1%s）", threads, localTTSPort())
			// 刚开的要加载模型（半分钟左右）：之后再问它
			localTTS.Lock()
			localTTS.checked = time.Time{}
			localTTS.Unlock()
		case !want && running:
			log.Printf("本地语音设置成不用，关掉")
			stop()
		}
		// 稳定跑了 10 分钟以上：下次再出问题从 10 秒重开（一直起不来就越等越久，最多 10 分钟）
		if cmd != nil && time.Since(startedAt) > 10*time.Minute {
			backoff = 10 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

// 本地语音的端口（:27210），和 localTTSURL 一致
func localTTSPort() string {
	u := localTTSURL()
	if i := strings.LastIndex(u, ":"); i > strings.Index(u, "//") {
		return u[i:]
	}
	return ":27210"
}
