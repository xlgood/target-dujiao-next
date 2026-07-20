package admin

import (
	"github.com/dujiao-next/internal/provider"
	"github.com/dujiao-next/internal/queue"
)

// Handler 后台管理接口处理器入口
// 说明：该处理器仅用于管理端 API。
type Handler struct {
	*provider.Container
	ProviderCatalogClientFactory    ProviderCatalogClientFactory
	ProviderCatalogInventoryEnqueue func() error
	ProviderCatalogContentEnqueue   func(queue.ProviderCatalogContentSyncPayload) error
}

// New 创建后台处理器
func New(c *provider.Container) *Handler {
	return &Handler{
		Container:                    c,
		ProviderCatalogClientFactory: defaultProviderCatalogClientFactory,
	}
}
