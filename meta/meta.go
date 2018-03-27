package meta

import (
	"github.com/journeymidnight/yig/log"
	"github.com/journeymidnight/yig/meta/client"
	"github.com/journeymidnight/yig/meta/client/hbaseclient"
)

const (
	ENCRYPTION_KEY_LENGTH = 32 // 32 bytes for AES-"256"
)

type Meta struct {
	Client client.Client
	Logger *log.Logger
	Cache  MetaCache
}

func New(logger *log.Logger, myCacheType CacheType) *Meta {
	meta := Meta{
		Client: hbaseclient.NewHbaseClient(),
		Logger: logger,
		Cache:  newMetaCache(myCacheType),
	}
	return &meta
}
