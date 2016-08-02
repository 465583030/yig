package storage

import (
	"crypto/md5"
	"encoding/hex"
	"git.letv.cn/yig/yig/meta"
	"git.letv.cn/yig/yig/minio/datatype"
	"git.letv.cn/yig/yig/signature"
	"github.com/tsuna/gohbase/hrpc"
	"golang.org/x/net/context"
	"io"
	"time"
)

func (yig *YigStorage) PickOneClusterAndPool(bucket string, object string, size int64) (cluster *CephStorage, poolName string) {
	// always choose the first cluster for testing
	if size < 0 { // request.ContentLength is -1 if length is unknown
		return yig.DataStorage["2fc32752-04a3-48dc-8297-40fb4dd11ff5"], BIG_FILE_POOLNAME
	}
	if size < BIG_FILE_THRESHOLD {
		return yig.DataStorage["2fc32752-04a3-48dc-8297-40fb4dd11ff5"], SMALL_FILE_POOLNAME
	} else {
		return yig.DataStorage["2fc32752-04a3-48dc-8297-40fb4dd11ff5"], BIG_FILE_POOLNAME
	}
}

func (yig *YigStorage) GetObject(bucket, object string, startOffset int64, length int64, writer io.Writer) (err error) {
	return
}

func (yig *YigStorage) GetObjectInfo(bucket, object string) (objInfo datatype.ObjectInfo, err error) {
	return
}

func (yig *YigStorage) PutObject(bucketName string, objectName string, size int64,
	data io.Reader, metadata map[string]string) (md5String string, err error) {
	md5Writer := md5.New()

	// Limit the reader to its provided size if specified.
	var limitedDataReader io.Reader
	if size > 0 { // request.ContentLength is -1 if length is unknown
		limitedDataReader = io.LimitReader(data, size)
	} else {
		limitedDataReader = data
	}
	cephCluster, poolName := yig.PickOneClusterAndPool(bucketName, objectName, size)

	// Mapping a shorter name for the object
	oid := cephCluster.GetUniqUploadName()
	storageReader := io.TeeReader(limitedDataReader, md5Writer)
	bytesWritten, err := cephCluster.put(poolName, oid, storageReader)
	if err != nil {
		return "", err
	}
	if bytesWritten < size {
		return "", datatype.IncompleteBody{
			Bucket: bucketName,
			Object: objectName,
		}
	}

	calculatedMd5 := hex.EncodeToString(md5Writer.Sum(nil))
	if userMd5, ok := metadata["md5Sum"]; ok {
		if userMd5 != calculatedMd5 {
			return "", datatype.BadDigest{
				ExpectedMD5:   userMd5,
				CalculatedMD5: calculatedMd5,
			}
		}
	}

	credential, err := data.(*signature.SignVerifyReader).Verify()
	if err != nil {
		return "", err
	}

	bucket, err := yig.MetaStorage.GetBucketInfo(bucketName)
	if err != nil {
		return "", err
	}

	if bucket.OwnerId != credential.UserId {
		return "", datatype.BucketAccessForbidden{Bucket: bucketName}
		// TODO validate bucket policy and ACL
	}

	object := meta.Object{
		Name:             objectName,
		BucketName:       bucketName,
		Location:         "", // TODO
		Pool:             poolName,
		OwnerId:          credential.UserId,
		Size:             bytesWritten,
		ObjectId:         oid,
		LastModifiedTime: time.Now(),
		Etag:             calculatedMd5,
		ContentType:      metadata["Content-Type"],
		// TODO CustomAttributes
	}

	rowkey, err := object.GetRowkey()
	if err != nil {
		return "", err
	}
	values, err := object.GetValues()
	if err != nil {
		return "", err
	}
	put, err := hrpc.NewPutStr(context.Background(), meta.OBJECT_TABLE,
		rowkey, values)
	if err != nil {
		return "", err
	}
	_, err = yig.MetaStorage.Hbase.Put(put)
	if err != nil {
		return "", err
	}
	return calculatedMd5, nil
}

func (yig *YigStorage) DeleteObject(bucket, object string) error {
	return nil
}
