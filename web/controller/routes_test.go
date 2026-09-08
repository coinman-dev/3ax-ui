package controller

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestNginxRoutesRegister guards against a route tree gin refuses to build —
// it panics at registration, which means the panel would not start at all.
func TestNginxRoutesRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewNginxController(r.Group("/panel/api/nginx"))
}
