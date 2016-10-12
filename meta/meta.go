package meta

import (
	"encoding/hex"
	"git.letv.cn/yig/yig/helper"
	"github.com/tsuna/gohbase"
	"github.com/xxtea/xxtea-go/xxtea"
	"log"
)

const (
	BUCKET_TABLE                          = "buckets"
	BUCKET_COLUMN_FAMILY                  = "b"
	BUCKET_ACL_COLUMN_FAMILY              = "a"
	BUCKET_CORS_COLUMN_FAMILY             = "c"
	USER_TABLE                            = "users"
	USER_COLUMN_FAMILY                    = "u"
	OBJECT_TABLE                          = "objects"
	OBJECT_COLUMN_FAMILY                  = "o"
	OBJECT_PART_COLUMN_FAMILY             = "p"
	GARBAGE_COLLECTION_TABLE              = "garbageCollection"
	GARBAGE_COLLECTION_COLUMN_FAMILY      = "gc"
	GARBAGE_COLLECTION_PART_COLUMN_FAMILY = "p"
	MULTIPART_TABLE                       = "multiparts"
	MULTIPART_COLUMN_FAMILY               = "m"

	CREATE_TIME_LAYOUT = "2006-01-02T15:04:05.000Z"

	ENCRYPTION_KEY_LENGTH        = 32 // 32 bytes for AES-"256"
	INITIALIZATION_VECTOR_LENGTH = 16 // 12 bytes is best performance for GCM, but for CTR
)

var (
	XXTEA_KEY         = []byte("hehehehe")
	SSE_S3_MASTER_KEY = []byte("hehehehehehehehehehehehehehehehe") // 32 bytes to select AES-256
)

type Meta struct {
	Hbase  gohbase.Client
	Logger *log.Logger
	Cache  *MetaCache
}

func New(logger *log.Logger) *Meta {
	hbase := gohbase.NewClient(helper.CONFIG.ZookeeperAddress)
	meta := Meta{
		Hbase:  hbase,
		Logger: logger,
		Cache:  newMetaCache(),
	}
	return &meta
}

func Decrypt(value string) (string, error) {
	bytes, err := hex.DecodeString(value)
	if err != nil {
		return "", err
	}
	return string(xxtea.Decrypt(bytes, XXTEA_KEY)), nil
}

func Encrypt(value string) string {
	return hex.EncodeToString(xxtea.Encrypt([]byte(value), XXTEA_KEY))
}
