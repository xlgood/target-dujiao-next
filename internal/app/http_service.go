package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/dujiao-next/internal/config"
)

// HTTPService HTTP 服务封装
type HTTPService struct {
	name   string
	server *http.Server
}

// NewHTTPService 创建 HTTP 服务
func NewHTTPService(addr string, handler http.Handler, cfg config.ServerConfig) *HTTPService {
	return &HTTPService{
		name: "http",
		server: &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: secondsDuration(cfg.ReadHeaderTimeoutSeconds),
			ReadTimeout:       secondsDuration(cfg.ReadTimeoutSeconds),
			WriteTimeout:      secondsDuration(cfg.WriteTimeoutSeconds),
			IdleTimeout:       secondsDuration(cfg.IdleTimeoutSeconds),
			MaxHeaderBytes:    maxHeaderBytes(cfg.MaxHeaderBytes),
		},
	}
}

func secondsDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func maxHeaderBytes(value int) int {
	if value <= 0 {
		return http.DefaultMaxHeaderBytes
	}
	return value
}

// Name 服务名称
func (s *HTTPService) Name() string {
	if s == nil || s.name == "" {
		return "http"
	}
	return s.name
}

// Start 启动服务
func (s *HTTPService) Start(ctx context.Context) error {
	if s == nil || s.server == nil {
		return errors.New("http server not initialized")
	}
	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop 停止服务
func (s *HTTPService) Stop(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}
