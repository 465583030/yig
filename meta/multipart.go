package meta

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"github.com/tsuna/gohbase/hrpc"
	"github.com/xxtea/xxtea-go/xxtea"
	"strconv"
	"strings"
	"time"
	"encoding/json"
)

var (
	XXTEA_KEY = []byte("hehehehe")
)

type Part struct {
	PartNumber   int
	Location     string
	Pool         string
	Size         int64
	ObjectId     string
	Offset       int64 // offset of this part in whole object, omitted in multipart table
	Etag         string
	LastModified time.Time // time in format "2006-01-02T15:04:05.000Z"
}

// Multipart table rowkey format:
// BucketName +
// bigEndian(uint16(count("/", ObjectName))) +
// ObjectName +
// bigEndian(unixNanoTimestamp)
func GetMultipartRowkey(bucketName, objectName string, now time.Time) (string, error) {
	var rowkey bytes.Buffer
	rowkey.WriteString(bucketName)
	err := binary.Write(&rowkey, binary.BigEndian, uint16(strings.Count(objectName, "/")))
	if err != nil {
		return "", err
	}
	rowkey.WriteString(objectName)
	err = binary.Write(&rowkey, binary.BigEndian, uint64(now.UnixNano()))
	if err != nil {
		return "", err
	}
	return rowkey.String(), nil
}

func TimestampStringFromUploadId(uploadId string) (string, error) {
	uploadIdBytes, err := hex.DecodeString(uploadId)
	if err != nil {
		return "", err
	}
	return string(xxtea.Decrypt(uploadIdBytes, XXTEA_KEY)), nil
}

func GetMultipartRowkeyFromUploadId(bucketName, objectName, uploadId string) (string, error) {
	var rowkey bytes.Buffer
	rowkey.WriteString(bucketName)
	err := binary.Write(&rowkey, binary.BigEndian, uint16(strings.Count(objectName, "/")))
	if err != nil {
		return "", err
	}
	rowkey.WriteString(objectName)
	timestampString, err := TimestampStringFromUploadId(uploadId)
	if err != nil {
		return "", err
	}
	timestamp, err := strconv.ParseUint(timestampString, 10, 64)
	if err != nil {
		return "", err
	}
	err = binary.Write(&rowkey, binary.BigEndian, timestamp)
	if err != nil {
		return "", err
	}
	return rowkey.String(), nil
}

func GetMultipartUploadId(now time.Time) string {
	timeData := []byte(strconv.FormatUint(uint64(now.UnixNano()), 10))
	return hex.EncodeToString(xxtea.Encrypt(timeData, XXTEA_KEY))
}

func UploadFromResponse(response *hrpc.Result, bucketName string) (upload UploadMetadata, err error) {
	rowkey := response.Cells[0].Row
	// rowkey = BucketName + bigEndian(uint16(count("/", ObjectName)))
	// + ObjectName
	// + bigEndian(unixNanoTimestamp)
	upload.Object = string(rowkey[len(bucketName)+2 : len(rowkey)-8])
	timestampReader := bytes.NewReader(rowkey[len(rowkey)-8:])
	var timestamp uint64
	err = binary.Read(timestampReader, binary.BigEndian, &timestamp)
	if err != nil {
		return
	}
	upload.Initiated = time.Unix(0, int64(timestamp))
	upload.UploadID = GetMultipartUploadId(upload.Initiated)
	upload.StorageClass = "STANDARD"
	return
}

func ValuesForParts(parts []Part) (values map[string][]byte, err error) {
	for partNumber, part := range parts {
		var marshaled []byte
		marshaled, err = json.Marshal(part)
		if err != nil {
			return
		}
		if values == nil {
			values = make(map[string][]byte)
		}
		values[strconv.Itoa(partNumber)] = marshaled
	}
	return
}
