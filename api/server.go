package api

import (
	"git.letv.cn/yig/yig/helper"
	"net/http"
)

type Server struct {
	Server *http.Server
}

func (s *Server) Stop() {
	helper.Logger.Print("Stopping API server...")
	rateLimiter.ShutdownServer()
	helper.Logger.Println("done")
}
