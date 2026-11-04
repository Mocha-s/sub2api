//go:build unit

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubPlazaKeyReader struct {
	user  *service.User
	err   error
	calls []string
}

func (s *stubPlazaKeyReader) ValidateKey(_ context.Context, key string) (*service.APIKey, *service.User, error) {
	s.calls = append(s.calls, key)
	if s.err != nil {
		return nil, nil, s.err
	}
	return &service.APIKey{ID: 1, UserID: s.user.ID}, s.user, nil
}

// newPlazaAuthEnv 组装广场认证链：新中间件 → handler 回写「是否带 AuthSubject」。
// jwtCalls 记录 Strict JWT 分支被调用的次数（0 表示 API key / 匿名路径）。
func newPlazaAuthEnv(reader plazaAPIKeyReader) (*gin.Engine, *int) {
	gin.SetMode(gin.TestMode)
	jwtCalls := 0
	strict := OptionalJWTAuthMiddleware(func(c *gin.Context) { jwtCalls++; c.Next() })
	mw := NewModelPlazaOptionalAuth(reader, strict)

	r := gin.New()
	r.Use(gin.HandlerFunc(mw))
	r.GET("/plaza", func(c *gin.Context) {
		subject, authed := GetAuthSubjectFromContext(c)
		c.JSON(http.StatusOK, gin.H{"authed": authed, "user_id": subject.UserID})
	})
	return r, &jwtCalls
}

func doPlazaRequest(r *gin.Engine, headers map[string]string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plaza", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	r.ServeHTTP(w, req)
	return w
}

func TestPlazaAuth_ValidAPIKeyBecomesUser(t *testing.T) {
	reader := &stubPlazaKeyReader{user: &service.User{ID: 7, Role: "user", Concurrency: 5}}
	r, jwtCalls := newPlazaAuthEnv(reader)

	w := doPlazaRequest(r, map[string]string{"Authorization": "Bearer sk-" + "0123456789abcdef"})

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"authed":true`)
	require.Contains(t, w.Body.String(), `"user_id":7`)
	require.Equal(t, []string{"sk-0123456789abcdef"}, reader.calls)
	require.Zero(t, *jwtCalls, "API key 不应走严格 JWT 校验")
}

func TestPlazaAuth_ValidAPIKeyViaXAPIKeyHeader(t *testing.T) {
	reader := &stubPlazaKeyReader{user: &service.User{ID: 9, Role: "user"}}
	r, _ := newPlazaAuthEnv(reader)

	w := doPlazaRequest(r, map[string]string{"x-api-key": "sk-" + "0123456789abcdef"})

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"authed":true`)
	require.Contains(t, w.Body.String(), `"user_id":9`)
}

func TestPlazaAuth_UnknownAPIKeyFallsBackToAnonymous(t *testing.T) {
	reader := &stubPlazaKeyReader{err: errors.New("not found")}
	r, jwtCalls := newPlazaAuthEnv(reader)

	w := doPlazaRequest(r, map[string]string{"Authorization": "Bearer sk-" + "deadbeefdeadbeef"})

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"authed":false`)
	require.Len(t, reader.calls, 1)
	require.Zero(t, *jwtCalls, "坏 key 不应回落成 JWT 严格校验")
}

func TestPlazaAuth_GarbageBearerSkipsValidatorAndJWT(t *testing.T) {
	reader := &stubPlazaKeyReader{user: &service.User{ID: 1}}
	r, jwtCalls := newPlazaAuthEnv(reader)

	w := doPlazaRequest(r, map[string]string{"Authorization": "Bearer short"})

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"authed":false`)
	require.Empty(t, reader.calls, "形态预检不通过不应打 DB")
	require.Zero(t, *jwtCalls)
}

func TestPlazaAuth_JWTShapedTokenDelegatesToStrictJWT(t *testing.T) {
	reader := &stubPlazaKeyReader{user: &service.User{ID: 1}}
	r, jwtCalls := newPlazaAuthEnv(reader)

	w := doPlazaRequest(r, map[string]string{"Authorization": "Bearer aaa.bbb.ccc"})

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, *jwtCalls)
	require.Empty(t, reader.calls, "JWT 形态不应被当成 API key")
}

func TestPlazaAuth_NonBearerAuthorizationDelegatesToStrictJWT(t *testing.T) {
	reader := &stubPlazaKeyReader{user: &service.User{ID: 1}}
	r, jwtCalls := newPlazaAuthEnv(reader)

	w := doPlazaRequest(r, map[string]string{"Authorization": "Basic dXNlcjpwYXNz"})

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, *jwtCalls)
	require.Empty(t, reader.calls)
}

func TestPlazaAuth_NoCredentialIsAnonymous(t *testing.T) {
	reader := &stubPlazaKeyReader{user: &service.User{ID: 1}}
	r, jwtCalls := newPlazaAuthEnv(reader)

	w := doPlazaRequest(r, nil)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"authed":false`)
	require.Equal(t, 1, *jwtCalls)
	require.Empty(t, reader.calls)
}
