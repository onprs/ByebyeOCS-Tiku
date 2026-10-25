// ocs-tiku-fork 是独立 fork 题库服务的维护入口。
//
// 命令：config check / migrate / serve。启动不会自动迁移，也不会创建默认模型或密钥。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/onprs/OCS-Tiku/internal/platform/answer"
	"github.com/onprs/OCS-Tiku/internal/platform/config"
	"github.com/onprs/OCS-Tiku/internal/platform/httpapi"
	"github.com/onprs/OCS-Tiku/internal/platform/modelclient"
	"github.com/onprs/OCS-Tiku/internal/platform/models"
	"github.com/onprs/OCS-Tiku/internal/platform/panel"
	"github.com/onprs/OCS-Tiku/internal/platform/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "错误："+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 {
		return errors.New("用法：ocs-tiku-fork <config check|migrate|serve|panel-password> [-config 路径]")
	}
	command := args[0]
	rest := args[1:]
	if command == "panel-password" {
		return panelPassword(rest)
	}
	if command != "config" && command != "migrate" && command != "serve" {
		return errors.New("未知命令，可用：config check、migrate、serve、panel-password")
	}
	if command == "config" {
		if len(rest) == 0 || rest[0] != "check" {
			return errors.New("用法：ocs-tiku-fork config check -config 路径")
		}
		rest = rest[1:]
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := flags.String("config", "ocs-tiku-fork.json", "配置文件路径")
	if err := flags.Parse(rest); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	switch command {
	case "config":
		return checkConfig(cfg)
	case "migrate":
		return migrate(cfg)
	case "serve":
		return serve(cfg)
	}
	return nil
}

func loadSecrets(cfg *config.Config) (string, map[string]string, error) {
	token, err := config.ReadSecretFile(cfg.ServiceTokenFile)
	if err != nil {
		return "", nil, errors.New("服务令牌文件不可用")
	}
	if len(token) < 32 {
		return "", nil, errors.New("服务令牌至少需要 32 个字符")
	}
	keys, err := config.LoadModelKeys(cfg.Models)
	if err != nil {
		return "", nil, err
	}
	return token, keys, nil
}

func checkConfig(cfg *config.Config) error {
	if _, err := config.ReadDatabaseDSN(cfg.DatabaseDSNFile); err != nil {
		return err
	}
	if cfg.UpstreamCAFile != "" {
		if _, err := os.Stat(cfg.UpstreamCAFile); err != nil {
			return errors.New("upstream_ca_file 不可用")
		}
	}
	if _, _, err := loadSecrets(cfg); err != nil {
		return err
	}
	if _, err := panel.LoadCredentials(cfg.Panel); err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(cfg.Listen)
	fmt.Printf("配置有效：监听 %s，模型 %d 个，默认模型 %s，图片转述模型 %s\n",
		cfg.Listen, len(cfg.Models), modelLabel(cfg.DefaultModelID), modelLabel(cfg.CaptionModelID))
	fmt.Printf("缓存 TTL %d 天，共享缓存 %s，并发 %d，超时 %d 秒\n",
		cfg.Cache.TTLDays, enabledLabel(cfg.Cache.SharedEnabled), cfg.Limits.Concurrency, cfg.Limits.TimeoutSeconds)
	if host != "" && host != "127.0.0.1" && host != "::1" && host != "localhost" {
		fmt.Println("提示：监听地址不是回环地址；请确认只允许私有网络和受保护入口访问。")
	}
	for _, model := range cfg.Models {
		fmt.Printf("- %s（%s）：协议 %s，能力 %s，状态 %s\n", model.ID, model.DisplayName, model.Protocol,
			strings.Join(model.Capabilities(), ","), enabledLabel(!model.Disabled))
	}
	return nil
}

func modelLabel(id string) string {
	if id == "" {
		return "未配置"
	}
	return id
}

func enabledLabel(value bool) string {
	if value {
		return "启用"
	}
	return "关闭"
}

func openPool(parent context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	dsn, err := config.ReadDatabaseDSN(cfg.DatabaseDSNFile)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("数据库连接配置无效")
	}
	poolConfig.MaxConns = cfg.DatabaseMaxConnections
	if poolConfig.MaxConns == 0 {
		poolConfig.MaxConns = config.DefaultDatabaseMaxConnections
	}
	poolConfig.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, errors.New("无法创建数据库连接池")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("无法连接 fork 题库数据库")
	}
	return pool, nil
}

func migrate(cfg *config.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := openPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	count, err := store.New(pool).Migrate(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("fork 题库迁移完成，共 %d 个版本\n", count)
	return nil
}

func serve(cfg *config.Config) error {
	if _, err := panel.LoadCredentials(cfg.Panel); err != nil {
		return err
	}
	token, keys, err := loadSecrets(cfg)
	if err != nil {
		return err
	}
	parent, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := openPool(parent, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	client, err := modelclient.NewWithOptions(modelclient.Options{
		AllowLoopback: cfg.AllowLoopbackUpstreams, CAFile: cfg.UpstreamCAFile, Timeout: cfg.Timeout(),
	})
	if err != nil {
		return err
	}
	st := store.New(pool)
	readyCtx, readyCancel := context.WithTimeout(parent, 15*time.Second)
	err = st.CheckSchema(readyCtx)
	readyCancel()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// 每日低峰按有限批次清理过期缓存、幂等响应载荷、答案原文与调用明细；
	// 幂等键、请求摘要与 token 汇总长期保留，旧键不会再次调用模型。
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		cleanup := func() {
			cleanupCtx, cancel := context.WithTimeout(parent, 30*time.Second)
			defer cancel()
			result, err := st.Cleanup(cleanupCtx, time.Now(), cfg.IdempotencyRetention())
			if err != nil {
				logger.Warn("fork 数据清理未完成", "cache_entries", result.CacheEntries,
					"request_payloads", result.RequestPayloads, "query_answers", result.QueryAnswers, "model_calls", result.ModelCalls, "panel_audit", result.PanelAudit)
				return
			}
			logger.Info("fork 数据清理完成", "cache_entries", result.CacheEntries,
				"request_payloads", result.RequestPayloads, "query_answers", result.QueryAnswers, "model_calls", result.ModelCalls, "panel_audit", result.PanelAudit)
		}
		cleanup()
		for {
			select {
			case <-parent.Done():
				return
			case <-ticker.C:
				cleanup()
			}
		}
	}()
	registry := models.New(cfg.Models, keys)
	svc := answer.NewWithRegistry(cfg, st, registry, client)
	handler := httpapi.New(cfg, svc, st, token, logger, registry)
	if cfg.Panel.Enabled {
		maintenance, err := panel.New(cfg, svc, st, registry)
		if err != nil {
			return err
		}
		handler = panel.Mount(handler, maintenance)
	}
	server := &http.Server{
		Addr: cfg.Listen, Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: cfg.Timeout() + 15*time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return errors.New("无法监听 fork 地址，请检查端口占用")
	}
	logger.Info("fork 题库服务已启动", "listen", listener.Addr().String(), "models", len(cfg.Models))
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case <-parent.Done():
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("fork HTTP 服务意外停止")
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return errors.New("fork 服务停止超时")
	}
	logger.Info("fork 题库服务已停止")
	return nil
}
