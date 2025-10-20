package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tr1v3r/pkg/log"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/httpserver"
	"github.com/tr1v3r/rcast/internal/netutil"
	"github.com/tr1v3r/rcast/internal/ssdp"
	"github.com/tr1v3r/rcast/internal/state"
	"github.com/tr1v3r/rcast/internal/uuid"
)

const serverName = "RCast-DMR/1.1" // "GoDLNA-DMR/1.1"

func main() {
	ctx := context.Background()
	defer log.Close()

	if debug := len(os.Args) > 1 && os.Args[1] == "--debug"; debug {
		log.SetLevel(log.DebugLevel)
	}

	cfg := config.Load()

	// 设备 UUID
	deviceUUID, err := uuid.LoadOrCreate(cfg.UUIDPath, config.DefaultUUID)
	if err != nil {
		log.Info("UUID load error, using default: %v", err)
		deviceUUID = config.DefaultUUID
	}

	// 网卡 IP
	ip, err := netutil.FirstUsableIPv4()
	if err != nil {
		log.Error("no IPv4: %v", err)
		return
	}
	baseURL := fmt.Sprintf("http://%s:%d", ip, cfg.HTTPPort)

	// 状态
	st := state.New(ctx)
	defer st.Stop()

	// HTTP
	mux := httpserver.NewMux()
	httpserver.RegisterHTTP(mux, baseURL, deviceUUID, st, cfg)
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler: httpserver.LogMiddleware(mux),
	}

	// SSDP
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go ssdp.Announce(ctx, baseURL, deviceUUID, serverName)
	go ssdp.SearchResponder(ctx, baseURL, deviceUUID, serverName)

	// 启动 HTTP
	go func() {
		log.Info("HTTP listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("HTTP error: %v", err)
			return
		}
	}()

	// 优雅退出
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	cancel()
	ctxShutdown, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	_ = srv.Shutdown(ctxShutdown)
	log.Info("bye")
}
