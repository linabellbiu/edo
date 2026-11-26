package httpapi

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"
)

const authorizationCanceledKey = "authorization_canceled"
const statusClientClosedRequest = 499

// 仅在鉴权边界或只读身份查询使用。业务动作尚未执行，不生成成功或拒绝审计；
// 动作执行后的断线仍由原业务流程记录真实结果。
func abortCanceledAuthorization(c *gin.Context) bool {
	if !errors.Is(c.Request.Context().Err(), context.Canceled) {
		return false
	}
	c.Set(authorizationCanceledKey, true)
	c.AbortWithStatus(statusClientClosedRequest)
	return true
}
