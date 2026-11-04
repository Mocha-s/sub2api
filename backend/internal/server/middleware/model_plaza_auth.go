package middleware

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// plazaAPIKeyReader 是广场可选认证需要的 API key 校验能力（抽成接口便于测试替身，
// 与 jwtUserReader 同样的做法）。
type plazaAPIKeyReader interface {
	ValidateKey(ctx context.Context, key string) (*service.APIKey, *service.User, error)
}

// NewModelPlazaOptionalAuth 创建模型广场的可选认证中间件。
//
// 广场是公开只读数据，凭据处理比 OptionalJWT 宽松：
//   - 无凭据 → 匿名放行；
//   - JWT 形态的 Bearer token → 交给 OptionalJWT 严格校验（坏 token 仍 401，前端
//     refresh-token 流程不变）；
//   - API key 形态的凭据（Bearer / x-api-key / x-goog-api-key）→ 认到用户即以该用户
//     身份展示（专属分组与专属倍率），无效 / 过期 / 用户停用则降级匿名，不因坏 key
//     阻断公开数据。
//
// 匿名能否读取由广场自身的 model_plaza_require_auth 开关在 handler 判定。
func NewModelPlazaOptionalAuth(apiKeyService plazaAPIKeyReader, optionalJWT OptionalJWTAuthMiddleware) gin.HandlerFunc {
	strictJWT := gin.HandlerFunc(optionalJWT)
	return func(c *gin.Context) {
		key, ok := plazaAPIKeyCredential(c)
		if !ok {
			strictJWT(c)
			return
		}
		// key 已在此消费：摘掉 Authorization，避免下游把 sk- 当坏 JWT 严格校验。
		c.Request.Header.Del("Authorization")
		if apiKeyService != nil && looksLikeAPIKey(key) {
			if _, user, err := apiKeyService.ValidateKey(c.Request.Context(), key); err == nil && user != nil {
				c.Set(string(ContextKeyUser), AuthSubject{UserID: user.ID, Concurrency: user.Concurrency})
				c.Set(string(ContextKeyUserRole), user.Role)
			}
		}
		c.Next()
	}
}

// plazaAPIKeyCredential 提取广场请求里 API key 形态的凭据。
// ok=false 表示没有凭据，或凭据是 JWT 形态 / 非 Bearer —— 都交回 OptionalJWT 处理。
func plazaAPIKeyCredential(c *gin.Context) (string, bool) {
	if auth := c.GetHeader("Authorization"); strings.TrimSpace(auth) != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(strings.TrimSpace(parts[0]), "Bearer") {
			return "", false
		}
		token := strings.TrimSpace(parts[1])
		return token, !isJWTSyntax(token)
	}
	if k := strings.TrimSpace(c.GetHeader("x-api-key")); k != "" {
		return k, true
	}
	if k := strings.TrimSpace(c.GetHeader("x-goog-api-key")); k != "" {
		return k, true
	}
	return "", false
}

// isJWTSyntax 判断 token 是否为 JWT 形态：base64url 字符集不含点，三段点分即 JWT。
func isJWTSyntax(token string) bool {
	return strings.Count(token, ".") == 2
}

// looksLikeAPIKey 便宜的形态预检，避免把明显不是 key 的字符串打到认证缓存/DB。
// 字符集与 service.ValidateCustomKey 一致（key = 前缀 + alnum，见 APIKeyService.GenerateKey）。
func looksLikeAPIKey(s string) bool {
	if len(s) < 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '_', ch == '-':
		default:
			return false
		}
	}
	return true
}
