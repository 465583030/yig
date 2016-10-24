package main

import (
	"git.letv.cn/yig/yig/api"
	"git.letv.cn/yig/yig/helper"
	"github.com/kataras/iris"
	"log"
)

type adminServerConfig struct {
	Address     string
	Logger      *log.Logger
	ObjectLayer api.ObjectLayer
}

var adminServer *adminServerConfig

func getUsage(ctx *iris.Context) {
	helper.Debugln("enter getusage")
	bucketName := ctx.Param("bucket")
	usage, err := adminServer.ObjectLayer.GetUsage(bucketName)
	if err != nil {
		ctx.Write("get usage for bucket:%s failed", bucketName)
		return
	}
	helper.Debugln("enter getusage", bucketName, usage)
	ctx.Write("usage for bucket:%s,%d", bucketName, usage)

	return

}
func startAdminServer(config *adminServerConfig) {
	adminServer = config
	iris.Get("/hi", func(ctx *iris.Context) {
		ctx.Write("Hi %s", "YIG")
	})
	iris.Get("/admin/usage/:bucket", getUsage)
	go iris.Listen(config.Address)
}

func stopAdminServer() {
	// TODO should shutdown admin API server gracefully
}
