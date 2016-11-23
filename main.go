package main

import (
	"git.letv.cn/yig/yig/api"
	"git.letv.cn/yig/yig/helper"
	"git.letv.cn/yig/yig/redis"
	"git.letv.cn/yig/yig/storage"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var logger *log.Logger

func main() {
	// Errors should cause panic so as to log to stderr for function calls in main()

	rand.Seed(time.Now().UnixNano())

	helper.SetupConfig()
	if helper.CONFIG.InstanceId == "" {
		helper.CONFIG.InstanceId = string(api.GenerateRandomId())
	}
	if helper.CONFIG.HbaseTimeout == 0 {
		helper.CONFIG.HbaseTimeout = 30
	}

	f, err := os.OpenFile(helper.CONFIG.LogPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		panic("Failed to open log file " + helper.CONFIG.LogPath)
	}
	defer f.Close()

	logger = log.New(f, "[yig]", log.LstdFlags)
	helper.Logger = logger

	logger.Println("YIG instance ID:", helper.CONFIG.InstanceId)

	redis.Initialize()
	defer redis.Close()

	yig := storage.New(logger)
	adminServerConfig := &adminServerConfig{
		Address: helper.CONFIG.BindAdminAddress,
		Logger:  logger,
		Yig:     yig,
	}
	startAdminServer(adminServerConfig)

	apiServerConfig := &ServerConfig{
		Address:      helper.CONFIG.BindApiAddress,
		KeyFilePath:  helper.CONFIG.SSLKeyPath,
		CertFilePath: helper.CONFIG.SSLCertPath,
		Logger:       logger,
		ObjectLayer:  yig,
	}
	startApiServer(apiServerConfig)

	// ignore signal handlers set by Iris
	signal.Ignore()
	signalQueue := make(chan os.Signal)
	signal.Notify(signalQueue, syscall.SIGINT, syscall.SIGTERM,
		syscall.SIGQUIT, syscall.SIGHUP)
	for {
		s := <-signalQueue
		switch s {
		case syscall.SIGHUP:
			// reload config file
			helper.SetupConfig()
		default:
			// stop YIG server, order matters
			stopAdminServer()
			stopApiServer()
			yig.Stop()
			return
		}
	}
}
