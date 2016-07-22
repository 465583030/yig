package storage

import (
	"git.letv.cn/yig/yig/iam"
	"git.letv.cn/yig/yig/meta"
	"git.letv.cn/yig/yig/minio/datatype"
	"github.com/tsuna/gohbase/hrpc"
	"golang.org/x/net/context"
	"time"
)

const (
	CREATE_TIME_LAYOUT = "2006-01-02T15:04:05.000Z"
)

func (yig *YigStorage) MakeBucket(bucket string, credential iam.Credential) error {
	now := time.Now().UTC().Format(CREATE_TIME_LAYOUT)
	values := map[string]map[string][]byte{
		meta.BUCKET_COLUMN_FAMILY: map[string][]byte{
			"CORS":       []byte{}, // TODO
			"UID":        []byte(credential.UserId),
			"ACL":        []byte{}, // TODO
			"createTime": []byte(now),
		},
	}
	put, err := hrpc.NewPutStr(context.Background(), meta.BUCKET_TABLE, bucket, values)
	if err != nil {
		yig.Logger.Println("Error making hbase put: ", err)
		return err
	}
	processed, err := yig.MetaStorage.Hbase.CheckAndPut(put, meta.BUCKET_COLUMN_FAMILY,
		"UID", []byte{})
	if err != nil {
		yig.Logger.Println("Error making hbase checkandput: ", err)
		return err
	}
	if !processed {
		family := map[string][]string{meta.BUCKET_COLUMN_FAMILY: []string{"UID"}}
		get, err := hrpc.NewGetStr(context.Background(), meta.BUCKET_TABLE, bucket,
			hrpc.Families(family))
		if err != nil {
			yig.Logger.Println("Error making hbase get: ", err)
			return err
		}
		b, err := yig.MetaStorage.Hbase.Get(get)
		if err != nil {
			yig.Logger.Println("Error get bucket: ", bucket, "with error: ", err)
			return datatype.BucketExists{Bucket: bucket}
		}
		if string(b.Cells[0].Value) == credential.UserId {
			return datatype.BucketExistsAndOwned{Bucket: bucket}
		} else {
			return datatype.BucketExists{Bucket: bucket}
		}
	}
	err = yig.MetaStorage.AddBucketForUser(bucket, credential.UserId)
	if err != nil { // roll back bucket table, i.e. remove inserted bucket
		yig.Logger.Println("Error AddBucketForUser: ", err)
		del, err := hrpc.NewDelStr(context.Background(), meta.BUCKET_TABLE, bucket, values)
		if err != nil {
			yig.Logger.Println("Error making hbase del: ", err)
			yig.Logger.Println("Leaving junk bucket unremoved: ", bucket)
			return err
		}
		_, err = yig.MetaStorage.Hbase.Delete(del)
		if err != nil {
			yig.Logger.Println("Error deleting: ", err)
			yig.Logger.Println("Leaving junk bucket unremoved: ", bucket)
			return err
		}
	}
	return err
}

func (yig *YigStorage) GetBucketInfo(bucket string) (bucketInfo datatype.BucketInfo, err error) {
	return
}

func (yig *YigStorage) ListBuckets() (buckets []datatype.BucketInfo, err error) {
	return
}

func (yig *YigStorage) DeleteBucket(bucket string) error {
	return nil
}

func (yig *YigStorage) ListObjects(bucket, prefix, marker, delimiter string, maxKeys int) (result datatype.ListObjectsInfo, err error) {
	return
}
