package storage

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"git.letv.cn/yig/yig/api/datatype"
	. "git.letv.cn/yig/yig/error"
	"git.letv.cn/yig/yig/helper"
	"git.letv.cn/yig/yig/iam"
	"git.letv.cn/yig/yig/meta"
	"git.letv.cn/yig/yig/signature"
	"github.com/tsuna/gohbase/filter"
	"github.com/tsuna/gohbase/hrpc"
	"golang.org/x/net/context"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	MAX_PART_SIZE   = 5 << 30   // 5GB
	MIN_PART_SIZE   = 128 << 10 // 128KB
	MAX_PART_NUMBER = 10000
)

func (yig *YigStorage) ListMultipartUploads(credential iam.Credential, bucketName string,
	request datatype.ListUploadsRequest) (result datatype.ListMultipartUploadsResponse, err error) {

	bucket, err := yig.MetaStorage.GetBucket(bucketName)
	if err != nil {
		return
	}
	switch bucket.ACL.CannedAcl {
	case "public-read", "public-read-write":
		break
	case "authenticated-read":
		if credential.UserId == "" {
			err = ErrBucketAccessForbidden
			return
		}
	default:
		if bucket.OwnerId != credential.UserId {
			err = ErrBucketAccessForbidden
			return
		}
	}
	// TODO policy and fancy ACL

	var startRowkey bytes.Buffer
	startRowkey.WriteString(bucketName)
	// TODO: refactor, same as in getMultipartRowkeyFromUploadId
	if request.KeyMarker != "" {
		err = binary.Write(&startRowkey, binary.BigEndian,
			uint16(strings.Count(request.KeyMarker, "/")))
		if err != nil {
			return
		}
		startRowkey.WriteString(request.KeyMarker)
		if request.UploadIdMarker != "" {
			var timestampString string
			timestampString, err = meta.Decrypt(request.UploadIdMarker)
			if err != nil {
				return result, err
			}
			var timestamp uint64
			timestamp, err = strconv.ParseUint(timestampString, 10, 64)
			if err != nil {
				return result, err
			}
			err = binary.Write(&startRowkey, binary.BigEndian, timestamp)
			if err != nil {
				return
			}
		}
	}

	comparator := filter.NewRegexStringComparator(
		"^"+bucketName+".."+request.Prefix+".*"+".{8}"+"$",
		0x20, // Dot-all mode
		"ISO-8859-1",
		"JAVA", // regexp engine name, in `JAVA` or `JONI`
	)
	compareFilter := filter.NewCompareFilter(filter.Equal, comparator)
	rowFilter := filter.NewRowFilter(compareFilter)

	scanRequest, err := hrpc.NewScanRangeStr(context.Background(), meta.MULTIPART_TABLE,
		// scan for max+1 rows to determine if results are truncated
		startRowkey.String(), "", hrpc.Filters(rowFilter),
		hrpc.NumberOfRows(uint32(request.MaxUploads+1)))
	if err != nil {
		return
	}
	scanResponse, err := yig.MetaStorage.Hbase.Scan(scanRequest)
	if err != nil {
		return
	}

	if len(scanResponse) > request.MaxUploads {
		result.IsTruncated = true
		var nextUpload meta.Multipart
		nextUpload, err = meta.MultipartFromResponse(scanResponse[request.MaxUploads], bucketName)
		if err != nil {
			return
		}
		result.NextKeyMarker = nextUpload.ObjectName
		result.NextUploadIdMarker, err = nextUpload.GetUploadId()
		if err != nil {
			return
		}
		scanResponse = scanResponse[:request.MaxUploads]
	}

	var currentLevel int
	if request.Delimiter == "" {
		currentLevel = 0
	} else {
		currentLevel = strings.Count(request.Prefix, request.Delimiter)
	}

	uploads := make([]datatype.Upload, 0, len(scanResponse))
	prefixMap := make(map[string]int) // value is dummy, only need a set here
	for _, row := range scanResponse {
		var m meta.Multipart
		m, err = meta.MultipartFromResponse(row, bucketName)
		if err != nil {
			return
		}
		upload := datatype.Upload{
			StorageClass: "STANDARD",
			Initiated:    m.InitialTime.UTC().Format(meta.CREATE_TIME_LAYOUT),
		}
		if request.Delimiter == "" {
			upload.Key = m.ObjectName
		} else {
			level := strings.Count(m.ObjectName, request.Delimiter)
			if level > currentLevel {
				split := strings.Split(m.ObjectName, request.Delimiter)
				split = split[:currentLevel+1]
				prefix := strings.Join(split, request.Delimiter) + request.Delimiter
				prefixMap[prefix] = 1
				continue
			} else {
				upload.Key = m.ObjectName
			}
		}
		upload.Key = strings.TrimPrefix(upload.Key, request.Prefix)
		if request.EncodingType != "" { // only support "url" encoding for now
			upload.Key = url.QueryEscape(upload.Key)
		}
		upload.UploadId, err = m.GetUploadId()
		if err != nil {
			return
		}

		var owner iam.Credential
		owner, err = iam.GetCredentialByUserId(m.Metadata["OwnerId"])
		if err != nil {
			return
		}
		upload.Owner.ID = owner.UserId
		upload.Owner.DisplayName = owner.DisplayName
		owner, err = iam.GetCredentialByUserId(m.Metadata["InitiatorId"])
		if err != nil {
			return
		}
		upload.Initiator.ID = owner.UserId
		upload.Initiator.DisplayName = owner.DisplayName

		uploads = append(uploads, upload)
	}
	result.Uploads = uploads

	prefixes := helper.Keys(prefixMap)
	sort.Strings(prefixes)
	for _, prefix := range prefixes {
		result.CommonPrefixes = append(result.CommonPrefixes, datatype.CommonPrefix{
			Prefix: prefix,
		})
	}

	result.KeyMarker = request.KeyMarker
	result.UploadIdMarker = request.UploadIdMarker
	result.MaxUploads = request.MaxUploads
	result.Prefix = request.Prefix
	result.Delimiter = request.Delimiter
	result.EncodingType = request.EncodingType
	return
}

func (yig *YigStorage) NewMultipartUpload(credential iam.Credential, bucketName, objectName string,
	metadata map[string]string, acl datatype.Acl) (uploadId string, err error) {
	bucket, err := yig.MetaStorage.GetBucket(bucketName)
	if err != nil {
		return
	}
	switch bucket.ACL.CannedAcl {
	case "public-read-write":
		break
	default:
		if bucket.OwnerId != credential.UserId {
			return "", ErrBucketAccessForbidden
		}
	}
	// TODO policy and fancy ACL

	metadata["InitiatorId"] = credential.UserId
	metadata["OwnerId"] = bucket.OwnerId
	metadata["Acl"] = acl.CannedAcl

	multipart := &meta.Multipart{
		BucketName:  bucketName,
		ObjectName:  objectName,
		InitialTime: time.Now().UTC(),
		Metadata:    metadata,
	}

	uploadId, err = multipart.GetUploadId()
	if err != nil {
		return
	}
	multipartValues, err := multipart.GetValues()
	if err != nil {
		return
	}
	rowkey, err := multipart.GetRowkey()
	if err != nil {
		return
	}
	newMultipartPut, err := hrpc.NewPutStr(context.Background(), meta.MULTIPART_TABLE,
		rowkey, multipartValues)
	if err != nil {
		return
	}
	_, err = yig.MetaStorage.Hbase.Put(newMultipartPut)
	return
}

func (yig *YigStorage) PutObjectPart(bucketName, objectName, uploadId string,
	partId int, size int64, data io.Reader, md5Hex string) (md5String string, err error) {

	multipart, err := yig.MetaStorage.GetMultipart(bucketName, objectName, uploadId)
	if err != nil {
		return
	}

	if size > MAX_PART_SIZE {
		err = ErrEntityTooLarge
		return
	}

	md5Writer := md5.New()
	limitedDataReader := io.LimitReader(data, size)
	cephCluster, poolName := yig.PickOneClusterAndPool(bucketName, objectName, size)
	oid := cephCluster.GetUniqUploadName()
	storageReader := io.TeeReader(limitedDataReader, md5Writer)
	bytesWritten, err := cephCluster.put(poolName, oid, storageReader)
	if err != nil {
		return
	}
	if bytesWritten < size {
		err = ErrIncompleteBody
		return
	}

	calculatedMd5 := hex.EncodeToString(md5Writer.Sum(nil))
	if md5Hex != calculatedMd5 {
		err = ErrBadDigest
		return
	}

	credential, err := data.(*signature.SignVerifyReader).Verify()
	if err != nil {
		return
	}
	bucket, err := yig.MetaStorage.GetBucket(bucketName)
	if err != nil {
		return
	}
	switch bucket.ACL.CannedAcl {
	case "public-read-write":
		break
	default:
		if bucket.OwnerId != credential.UserId {
			return "", ErrBucketAccessForbidden
		}
	} // TODO policy and fancy ACL

	part := meta.Part{
		PartNumber:   partId,
		Location:     cephCluster.Name,
		Pool:         poolName,
		Size:         size,
		ObjectId:     oid,
		Etag:         calculatedMd5,
		LastModified: time.Now().UTC(),
	}
	partValues, err := part.GetValues()
	if err != nil {
		return
	}
	rowkey, err := multipart.GetRowkey()
	if err != nil {
		return
	}
	partMetaPut, err := hrpc.NewPutStr(context.Background(), meta.MULTIPART_TABLE,
		rowkey, partValues)
	if err != nil {
		return
	}
	_, err = yig.MetaStorage.Hbase.Put(partMetaPut)
	if err != nil {
		// TODO remove object in Ceph
		return
	}
	return calculatedMd5, nil
	// TODO remove possible old object in Ceph
}

func (yig *YigStorage) CopyObjectPart(bucketName, objectName, uploadId string, partId int,
	size int64, data io.Reader, credential iam.Credential) (result datatype.PutObjectResult, err error) {

	multipart, err := yig.MetaStorage.GetMultipart(bucketName, objectName, uploadId)
	if err != nil {
		return
	}

	if size > MAX_PART_SIZE {
		err = ErrEntityTooLarge
		return
	}

	md5Writer := md5.New()
	limitedDataReader := io.LimitReader(data, size)
	cephCluster, poolName := yig.PickOneClusterAndPool(bucketName, objectName, size)
	oid := cephCluster.GetUniqUploadName()
	storageReader := io.TeeReader(limitedDataReader, md5Writer)
	bytesWritten, err := cephCluster.put(poolName, oid, storageReader)
	if err != nil {
		return
	}
	if bytesWritten < size {
		err = ErrIncompleteBody
		return
	}

	result.Md5 = hex.EncodeToString(md5Writer.Sum(nil))

	bucket, err := yig.MetaStorage.GetBucket(bucketName)
	if err != nil {
		return
	}
	switch bucket.ACL.CannedAcl {
	case "public-read-write":
		break
	default:
		if bucket.OwnerId != credential.UserId {
			err = ErrBucketAccessForbidden
			return
		}
	} // TODO policy and fancy ACL

	part := meta.Part{
		PartNumber:   partId,
		Location:     cephCluster.Name,
		Pool:         poolName,
		Size:         size,
		ObjectId:     oid,
		Etag:         result.Md5,
		LastModified: time.Now().UTC(),
	}
	result.LastModified = part.LastModified

	partValues, err := part.GetValues()
	if err != nil {
		return
	}
	rowkey, err := multipart.GetRowkey()
	if err != nil {
		return
	}
	partMetaPut, err := hrpc.NewPutStr(context.Background(), meta.MULTIPART_TABLE,
		rowkey, partValues)
	if err != nil {
		return
	}
	_, err = yig.MetaStorage.Hbase.Put(partMetaPut)
	if err != nil {
		// TODO remove object in Ceph
		return
	}
	return result, nil
	// TODO remove possible old object in Ceph
}

func (yig *YigStorage) ListObjectParts(credential iam.Credential, bucketName, objectName, uploadId string,
	partNumberMarker int, maxParts int) (result meta.ListPartsInfo, err error) {

	multipart, err := yig.MetaStorage.GetMultipart(bucketName, objectName, uploadId)
	if err != nil {
		return
	}

	result.InitiatorId = multipart.Metadata["InitiatorId"]
	result.OwnerId = multipart.Metadata["OwnerId"]

	switch multipart.Metadata["Acl"] {
	case "public-read", "public-read-write":
		break
	case "authenticated-read":
		if credential.UserId == "" {
			err = ErrAccessDenied
			return
		}
	case "bucket-owner-read", "bucket-owner-full-controll":
		var bucket meta.Bucket
		bucket, err = yig.MetaStorage.GetBucket(bucketName)
		if err != nil {
			return
		}
		if bucket.OwnerId != credential.UserId {
			err = ErrAccessDenied
			return
		}
	default:
		if result.OwnerId != credential.UserId {
			err = ErrAccessDenied
			return
		}
	}
	partCount := 0
	for i := partNumberMarker; i <= MAX_PART_NUMBER; i++ {
		if p, ok := multipart.Parts[i]; ok {
			result.Parts = append(result.Parts, p)

			partCount++
			if partCount > maxParts+1 {
				break
			}
		}
	}
	if partCount == maxParts+1 {
		result.IsTruncated = true
		result.Parts = result.Parts[:maxParts]
	}

	result.Bucket = bucketName
	result.Object = objectName
	result.UploadID = uploadId
	result.StorageClass = "STANDARD"
	result.PartNumberMarker = partNumberMarker
	result.MaxParts = maxParts

	return
}

func (yig *YigStorage) AbortMultipartUpload(credential iam.Credential,
	bucketName, objectName, uploadId string) error {

	bucket, err := yig.MetaStorage.GetBucket(bucketName)
	if err != nil {
		return err
	}
	switch bucket.ACL.CannedAcl {
	case "public-read-write":
		break
	default:
		if bucket.OwnerId != credential.UserId {
			return ErrBucketAccessForbidden
		}
	} // TODO policy and fancy ACL

	multipart, err := yig.MetaStorage.GetMultipart(bucketName, objectName, uploadId)
	if err != nil {
		return err
	}

	values := multipart.GetValuesForDelete()
	rowkey, err := multipart.GetRowkey()
	if err != nil {
		return err
	}

	deleteRequest, err := hrpc.NewDelStr(context.Background(), meta.MULTIPART_TABLE,
		rowkey, values)
	if err != nil {
		return err
	}
	_, err = yig.MetaStorage.Hbase.Delete(deleteRequest)
	return err
	// TODO remove parts in Ceph
}

func (yig *YigStorage) CompleteMultipartUpload(credential iam.Credential, bucketName,
	objectName, uploadId string, uploadedParts []meta.CompletePart) (result datatype.CompleteMultipartResult,
	err error) {

	bucket, err := yig.MetaStorage.GetBucket(bucketName)
	if err != nil {
		return
	}
	switch bucket.ACL.CannedAcl {
	case "public-read-write":
		break
	default:
		if bucket.OwnerId != credential.UserId {
			err = ErrBucketAccessForbidden
			return
		}
	}
	// TODO policy and fancy ACL

	multipart, err := yig.MetaStorage.GetMultipart(bucketName, objectName, uploadId)
	if err != nil {
		return
	}

	md5Writer := md5.New()
	var totalSize int64 = 0
	for i := 0; i < len(uploadedParts); i++ {
		if uploadedParts[i].PartNumber != i+1 {
			err = ErrInvalidPart
			return
		}
		part, ok := multipart.Parts[i+1]
		if !ok {
			err = ErrInvalidPart
			return
		}
		if part.Size < MIN_PART_SIZE && part.PartNumber != len(uploadedParts) {
			err = meta.PartTooSmall{
				PartSize:   part.Size,
				PartNumber: part.PartNumber,
				PartETag:   part.Etag,
			}
			return
		}
		if part.Etag != uploadedParts[i].ETag {
			err = ErrInvalidPart
			return
		}
		var etagBytes []byte
		etagBytes, err = hex.DecodeString(part.Etag)
		if err != nil {
			err = ErrInvalidPart
			return
		}
		part.Offset = totalSize
		totalSize += part.Size
		md5Writer.Write(etagBytes)
	}
	result.ETag = hex.EncodeToString(md5Writer.Sum(nil))
	result.ETag += "-" + strconv.Itoa(len(uploadedParts))
	// See http://stackoverflow.com/questions/12186993
	// for how to calculate multipart Etag

	// Add to objects table
	contentType, ok := multipart.Metadata["Content-Type"]
	if !ok {
		contentType = "application/octet-stream"
	}
	object := meta.Object{
		Name:             objectName,
		BucketName:       bucketName,
		OwnerId:          multipart.Metadata["OwnerId"],
		Size:             totalSize,
		LastModifiedTime: time.Now().UTC(),
		Etag:             result.ETag,
		ContentType:      contentType,
		Parts:            multipart.Parts,
	}

	var olderObject meta.Object
	if bucket.Versioning == "Enabled" {
		result.VersionId = object.GetVersionId()
	} else { // remove older object if versioning is not enabled
		// FIXME use removeNullVersionObject for `Suspended` after fixing GetNullVersionObject
		olderObject, err = yig.MetaStorage.GetObject(bucketName, objectName)
		if err != ErrNoSuchKey {
			if err != nil {
				return
			}
			if olderObject.NullVersion {
				err = yig.removeByObject(olderObject)
				if err != nil {
					return
				}
			}
		} else {
			err = nil
		}
	}

	err = putObjectEntry(object, yig.MetaStorage)
	if err != nil {
		return
	}

	// Remove from multiparts table
	deleteValues := multipart.GetValuesForDelete()
	rowkey, err := multipart.GetRowkey()
	if err != nil {
		return
	}
	deleteRequest, err := hrpc.NewDelStr(context.Background(), meta.MULTIPART_TABLE,
		rowkey, deleteValues)
	if err != nil {
		return
	}
	_, err = yig.MetaStorage.Hbase.Delete(deleteRequest)
	if err != nil { // rollback objects table
		objectDeleteValues := object.GetValuesForDelete()
		objectDeleteRequest, err := hrpc.NewDelStr(context.Background(), meta.OBJECT_TABLE,
			object.Rowkey, objectDeleteValues)
		if err != nil {
			return result, err
		}
		_, err = yig.MetaStorage.Hbase.Delete(objectDeleteRequest)
		if err != nil {
			yig.Logger.Println("Error deleting object: ", err)
			yig.Logger.Println("Inconsistent data: object with rowkey ", object.Rowkey,
				"should be removed in HBase")
		}
		return result, err
	}
	return
}
