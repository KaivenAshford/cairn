// cairn 服务端。
//
// 它只承担静态壳做不到的事——见 ../ARCHITECTURE.md 第 1 节的判断标准：
// 「这个页面的内容对每个访客是不是一样的？」一样就该在构建时生成，不该走这里。
//
// 目前只实现了写入通道。它是整个站的心脏：记录类内容的唯一死因是输入摩擦，
// 而不是前端不好看。
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type config struct {
	addr  string
	token string
	// 所有条目都落这里，可见度由 frontmatter 决定，不由目录决定。
	// 这个目录是单独的 private 仓库（见 ARCHITECTURE.md 第 2 节）：代码仓里没有内容，
	// 于是「私密条目被提交进公开仓库」这条不可逆的路径根本不存在。
	contentDir string
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadConfig() (config, error) {
	c := config{
		addr:       envOr("CAIRN_ADDR", "127.0.0.1:8787"),
		token:      os.Getenv("CAIRN_TOKEN"),
		contentDir: filepath.Clean(envOr("CAIRN_CONTENT_DIR", "../web/content/entries")),
	}
	// 没有默认 token。一个能往磁盘写文件的接口，绝不能因为忘配环境变量就裸奔。
	if c.token == "" {
		return c, errors.New("CAIRN_TOKEN 未设置；用 `openssl rand -hex 32` 生成一个")
	}
	if len(c.token) < 32 {
		return c, errors.New("CAIRN_TOKEN 太短，至少 32 个字符")
	}
	return c, nil
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime)

	// distroless 镜像里没有 shell 也没有 curl，所以健康检查由二进制自己做。
	healthcheck := flag.Bool("healthcheck", false, "探测本地 /health 后退出，供容器健康检查使用")
	flag.Parse()
	if *healthcheck {
		os.Exit(probeHealth(envOr("CAIRN_ADDR", "127.0.0.1:8787")))
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("配置错误：%v", err)
	}

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           withLogging(routes(cfg)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("cairn 服务端监听 %s", cfg.addr)
		log.Printf("  条目目录 → %s", cfg.contentDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("监听失败：%v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("退出时未能优雅关闭：%v", err)
	}
	log.Print("已停止")
}

func routes(cfg config) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	mux.Handle("POST /api/write", cfg.requireToken(http.HandlerFunc(cfg.handleWrite)))

	// circle / private 的服务端渲染。见 ../ARCHITECTURE.md 第 3 节：allowlist + magic link。
	// 还没实现——在实现之前它必须拒绝所有请求。绝不能为了「先跑起来」而暂时放行：
	// 那正是私有内容泄露的标准剧本。
	mux.HandleFunc("/circle/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "circle 尚未实现", http.StatusNotImplemented)
	})

	return mux
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap 让 http.ResponseController 和 net/http 内部的接口断言能穿透这一层包装。
// 没有它，超大请求体的连接处理会被跳过，将来的 Flusher / Hijacker 也会静默失效。
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

// probeHealth 打一次本地 /health，成功返回 0。
func probeHealth(addr string) int {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/health")
	if err != nil {
		log.Printf("健康检查失败：%v", err)
		return 1
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Printf("健康检查返回 %d", resp.StatusCode)
		return 1
	}
	return 0
}
