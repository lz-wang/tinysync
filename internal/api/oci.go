package api

import (
	"github.com/gin-gonic/gin"

	"tinysync/internal/auth"
	"tinysync/internal/oci"
)

// registerOciRoutes 注册 OCI 领域端点（handler 在 internal/oci 内实现，
// 本层只做路由与 scope 中间件）。svc 为 nil 时跳过注册（依赖缺失由
// 未知路径 404 兜底）。
//
// 权限矩阵：Registry 列表与镜像状态查询 read；配置变更（CRUD）admin；
// tags / inspect / remote 系列会以已存凭据主动访问远端，与
// /sources/:id/test 同例取 admin。
func registerOciRoutes(group *gin.RouterGroup, svc *oci.Service) {
	if svc == nil {
		return
	}
	h := oci.NewHandlers(svc)
	group.GET("/registries", requireScope(auth.ScopeRead), h.ListRegistries)
	group.POST("/registries", requireScope(auth.ScopeAdmin), h.CreateRegistry)
	group.PATCH("/registries/:id", requireScope(auth.ScopeAdmin), h.UpdateRegistry)
	group.DELETE("/registries/:id", requireScope(auth.ScopeAdmin), h.DeleteRegistry)

	group.GET("/images", requireScope(auth.ScopeRead), h.ListImages)
	group.GET("/images/tags", requireScope(auth.ScopeAdmin), h.ListImageTags)
	group.GET("/images/inspect", requireScope(auth.ScopeAdmin), h.InspectImage)
	group.GET("/images/inspect/platform", requireScope(auth.ScopeAdmin), h.InspectImagePlatform)
	group.GET("/remote/repositories", requireScope(auth.ScopeAdmin), h.SearchRemoteRepositories)
	group.GET("/remote/tags", requireScope(auth.ScopeAdmin), h.ListRemoteTags)
}
