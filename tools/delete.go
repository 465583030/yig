package main

import (
	"github.com/journeymidnight/yig/helper"
	"github.com/journeymidnight/yig/storage"
	"github.com/journeymidnight/yig/meta"
	"github.com/journeymidnight/yig/log"
	"os"
	"context"
	"os/signal"
	"syscall"
	"sync"
	"strings"
	"time"
)

const (
	SCAN_HBASE_LIMIT   = 50
	WATER_LOW   = 120
	TASKQ_MAX_LENGTH   = 200
)

var (
	RootContext = context.Background()
	logger *log.Logger
	yigs []*storage.YigStorage
	taskQ chan meta.GarbageCollection
	waitgroup sync.WaitGroup
	stop bool
)

func deleteFromCeph(index int)  {
	for {
		if stop {
			helper.Logger.Print(5, ".")
			return
		}
		var (
			p	*meta.Part
			err    error
		)
		garbage := <- taskQ
		waitgroup.Add(1)
		if len(garbage.Parts) == 0 {
			err = yigs[index].DataStorage[garbage.Location].
				Remove(garbage.Pool, garbage.ObjectId)
			if err != nil {
				if strings.Contains(err.Error(), "ret=-2") {
					goto release
				}
				helper.Logger.Println(5, "failed delete", garbage.BucketName, ":", garbage.ObjectName, ":",
					garbage.Location,":",garbage.Pool,":",garbage.ObjectId, " error:", err)
			} else {
				helper.Logger.Println(5, "success delete",garbage.BucketName, ":", garbage.ObjectName, ":",
					garbage.Location,":",garbage.Pool,":",garbage.ObjectId)
			}
		} else {
			for _, p = range garbage.Parts {
				err = yigs[index].DataStorage[garbage.Location].
					Remove(garbage.Pool, p.ObjectId)
				if err != nil {
					if strings.Contains(err.Error(), "ret=-2") {
						goto release
					}
					helper.Logger.Println(5, "failed delete part", garbage.Location, ":", garbage.Pool, ":", p.ObjectId, " error:", err)
				} else {
					helper.Logger.Println(5, "success delete part",garbage.Location, ":", garbage.Pool, ":", p.ObjectId)
				}
			}
		}
	release:
		yigs[index].MetaStorage.RemoveGarbageCollection(garbage)
		waitgroup.Done()
	}
}

func removeDeleted () {
	time.Sleep(time.Duration(1000) * time.Millisecond)
	var startRowKey string
	var garbages []meta.GarbageCollection
	var err error
	for {
		if stop {
			helper.Logger.Print(5, ".")
			return
		}
	wait:
		if len(taskQ) >= WATER_LOW {
			time.Sleep(time.Duration(1) * time.Millisecond)
			goto wait
		}

		if len(taskQ) < WATER_LOW {
			garbages = garbages[:0]
			garbages, err = yigs[0].MetaStorage.ScanGarbageCollection(SCAN_HBASE_LIMIT, startRowKey)
			if err != nil {
				continue
			}
		}

		if len(garbages) == 0 {
			time.Sleep(time.Duration(10000) * time.Millisecond)
			startRowKey = ""
			continue
		} else if len(garbages) == 1 {
			for _, garbage := range garbages {
				taskQ <- garbage
			}
			startRowKey = ""
			time.Sleep(time.Duration(5000) * time.Millisecond)
			continue
		} else {
			startRowKey = garbages[len(garbages)-1].Rowkey
			garbages = garbages[:len(garbages)-1]
			for _, garbage := range garbages{
				taskQ <- garbage
			}
		}
	}
}


func main() {
	helper.SetupConfig()

	f, err := os.OpenFile("delete.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		panic("Failed to open log file in current dir")
	}
	defer f.Close()
	stop = false
	logger = log.New(f, "[yig]", log.LstdFlags, helper.CONFIG.LogLevel)
	helper.Logger = logger
	taskQ = make(chan meta.GarbageCollection, TASKQ_MAX_LENGTH)
	signal.Ignore()
	signalQueue := make(chan os.Signal)

	numOfWorkers := helper.CONFIG.GcThread
	yigs = make([]*storage.YigStorage, helper.CONFIG.GcThread+1)
	yigs[0] = storage.New(logger, int(meta.NoCache), false, helper.CONFIG.CephConfigPattern)
	helper.Logger.Println(5, "start gc thread:",numOfWorkers)
	for i := 0; i< numOfWorkers; i++ {
		yigs[i+1] = storage.New(logger, int(meta.NoCache), false, helper.CONFIG.CephConfigPattern)
		go deleteFromCeph(i+1)
	}
	go removeDeleted()
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
			stop = true
			waitgroup.Wait()
			return
		}
	}

}
