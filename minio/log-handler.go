package minio

import (
	"git.letv.cn/yig/yig/helper"
	"net/http"
)

type logHandler struct {
	handler http.Handler
}

func (l logHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Serves the request.
	helper.Debugln(r.Method, r.Host, r.URL)
	logger.Printf("STARTING %s %s host: %s", r.Method, r.URL, r.Host)
	l.handler.ServeHTTP(w, r)
	logger.Printf("COMPLETE %s %s host: %s", r.Method, r.URL, r.Host)
}

func setLogHandler(handler http.Handler) http.Handler {
	return logHandler{handler: handler}
}
