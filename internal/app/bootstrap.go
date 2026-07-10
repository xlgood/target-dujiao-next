package app

import (
	"errors"

	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/provider"
	"github.com/dujiao-next/internal/router"
	"github.com/dujiao-next/internal/worker"
)

// BuildRunner 构建服务运行器
func BuildRunner(cfg *config.Config, mode string) (*Runner, error) {
	if cfg == nil {
		return nil, errors.New("config is nil")
	}

	container := provider.NewContainer(cfg)

	var services []Service

	// 初始化 HTTP 服务
	if mode == ModeAll || mode == ModeAPI {
		engine := router.SetupRouter(cfg, container)
		addr := cfg.Server.Host + ":" + cfg.Server.Port
		httpService := NewHTTPService(addr, engine, cfg.Server)
		services = append(services, httpService)
	}

	// 初始化 Worker 服务
	if mode == ModeAll || mode == ModeWorker {
		consumer := worker.NewConsumer(container)
		workerService, err := worker.NewService(&cfg.Queue, consumer)
		if err != nil {
			return nil, err
		}
		services = append(services, workerService)
	}

	// 如果没有服务被启动（例如模式错误或配置导致都没起），应该报错或至少打日志
	if len(services) == 0 {
		return nil, errors.New("no services initialized (check mode and config)")
	}

	return NewRunner(services...), nil
}

// Run 应用启动入口
func Run(opts Options) error {
	opts = normalizeOptions(opts)
	if opts.Config == nil {
		return errors.New("config is nil")
	}

	runner, err := BuildRunner(opts.Config, opts.Mode)
	if err != nil {
		return err
	}

	addr := opts.Config.Server.Host + ":" + opts.Config.Server.Port
	opts.Logger.Infow("app_start", "addr", addr, "mode", opts.Mode)
	return RunWithOptions(runner, opts)
}
