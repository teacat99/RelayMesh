package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDocPathParamEncodedSlash(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Params = gin.Params{{Key: "doc", Value: "design%2Fe2e.md"}}
	if got := docPathParam(c); got != "design/e2e.md" && got != "design%2Fe2e.md" {
		t.Fatalf("doc path %q", got)
	}
}
