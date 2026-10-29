package server

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

// Hertz front end (CloudWeGo's HTTP framework): the same routes and core as
// Handler(), so a deployment can pick either with `serve -http hertz`.

func hwrite(c *app.RequestContext, r Resp) {
	if r.Text != "" {
		c.Data(r.Code, "text/markdown; charset=utf-8", []byte(r.Text))
		return
	}
	b, _ := json.MarshalIndent(r.JSON, "", "  ")
	c.Data(r.Code, "application/json", append(b, '\n'))
}

// HertzEngine builds the Hertz server without starting it (tests use it).
func (s *Server) HertzEngine(addr string) *server.Hertz {
	h := server.New(server.WithHostPorts(addr), server.WithMaxRequestBodySize(maxUpload), server.WithDisablePrintRoute(true))
	h.GET("/healthz", func(_ context.Context, c *app.RequestContext) { c.Data(200, "text/plain", []byte("ok\n")) })
	h.PUT("/v1/runs/:name", func(ctx context.Context, c *app.RequestContext) {
		hwrite(c, s.PutRun(ctx, c.Param("name"), string(c.Query("format")), c.Request.Body()))
	})
	h.GET("/v1/runs", func(ctx context.Context, c *app.RequestContext) { hwrite(c, s.ListRuns(ctx)) })
	h.POST("/v1/compare", func(ctx context.Context, c *app.RequestContext) {
		if len(c.Request.Body()) > 1<<20 {
			hwrite(c, errResp(errTooLarge))
			return
		}
		hwrite(c, s.Compare(ctx, c.Request.Body()))
	})
	h.GET("/v1/reports", func(ctx context.Context, c *app.RequestContext) {
		hwrite(c, s.ListReports(ctx, string(c.Query("limit"))))
	})
	h.GET("/v1/reports/:id", func(ctx context.Context, c *app.RequestContext) { hwrite(c, s.GetReport(ctx, c.Param("id"), false)) })
	h.GET("/v1/reports/:id/comment", func(ctx context.Context, c *app.RequestContext) { hwrite(c, s.GetReport(ctx, c.Param("id"), true)) })
	return h
}

// ServeHertz runs the Hertz front end until SIGINT/SIGTERM (Hertz handles
// the signals and drains connections).
func (s *Server) ServeHertz(addr string) error {
	s.HertzEngine(addr).Spin()
	return nil
}
