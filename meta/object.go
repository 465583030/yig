package meta

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"git.letv.cn/yig/yig/api/datatype"
	. "git.letv.cn/yig/yig/error"
	"git.letv.cn/yig/yig/helper"
	"git.letv.cn/yig/yig/redis"
	"github.com/cannium/gohbase/filter"
	"github.com/cannium/gohbase/hrpc"
	"github.com/xxtea/xxtea-go/xxtea"
)

const (
	ObjectNameEnding = ":"
)

type Object struct {
	Rowkey           []byte // Rowkey cache
	Name             string
	BucketName       string
	Location         string // which Ceph cluster this object locates
	Pool             string // which Ceph pool this object locates
	OwnerId          string
	Size             int64     // file size
	ObjectId         string    // object name in Ceph
	LastModifiedTime time.Time // in format "2006-01-02T15:04:05.000Z"
	Etag             string
	ContentType      string
	CustomAttributes map[string]string
	Parts            map[int]*Part
	ACL              datatype.Acl
	NullVersion      bool   // if this entry has `null` version
	DeleteMarker     bool   // if this entry is a delete marker
	VersionId        string // version cache
	// type of Server Side Encryption, could be "KMS", "S3", "C"(custom), or ""(none),
	// KMS is not implemented yet
	SseType string
	// encryption key for SSE-S3, the key itself is encrypted with SSE_S3_MASTER_KEY,
	// in AES256-GCM
	EncryptionKey        []byte
	InitializationVector []byte
}

func (o *Object) String() (s string) {
	s += "Name: " + o.Name + "\n"
	s += "Location: " + o.Location + "\n"
	s += "Pool: " + o.Pool + "\n"
	s += "Object ID: " + o.ObjectId + "\n"
	s += "Last Modified Time: " + o.LastModifiedTime.Format(CREATE_TIME_LAYOUT) + "\n"
	s += "Version: " + o.VersionId + "\n"
	for n, part := range o.Parts {
		s += fmt.Sprintln("Part", n, " Location:", part.Location, "Pool:", part.Pool,
			"Object ID:", part.ObjectId)
	}
	return s
}

// Rowkey format:
// BucketName +
// bigEndian(uint16(count("/", ObjectName))) +
// ObjectName +
// ObjectNameEnding +
// bigEndian(uint64.max - unixNanoTimestamp)
func (o *Object) GetRowkey() (string, error) {
	if len(o.Rowkey) != 0 {
		return string(o.Rowkey), nil
	}
	var rowkey bytes.Buffer
	rowkey.WriteString(o.BucketName)
	err := binary.Write(&rowkey, binary.BigEndian, uint16(strings.Count(o.Name, "/")))
	if err != nil {
		return "", err
	}
	rowkey.WriteString(o.Name + ObjectNameEnding)
	err = binary.Write(&rowkey, binary.BigEndian,
		math.MaxUint64-uint64(o.LastModifiedTime.UnixNano()))
	if err != nil {
		return "", err
	}
	o.Rowkey = rowkey.Bytes()
	return string(o.Rowkey), nil
}

func (o *Object) GetValues() (values map[string]map[string][]byte, err error) {
	var size bytes.Buffer
	err = binary.Write(&size, binary.BigEndian, o.Size)
	if err != nil {
		return
	}
	err = o.encryptSseKey()
	if err != nil {
		return
	}
	if o.EncryptionKey == nil {
		o.EncryptionKey = []byte{}
	}
	if o.InitializationVector == nil {
		o.InitializationVector = []byte{}
	}
	values = map[string]map[string][]byte{
		OBJECT_COLUMN_FAMILY: map[string][]byte{
			"bucket":        []byte(o.BucketName),
			"location":      []byte(o.Location),
			"pool":          []byte(o.Pool),
			"owner":         []byte(o.OwnerId),
			"oid":           []byte(o.ObjectId),
			"size":          size.Bytes(),
			"lastModified":  []byte(o.LastModifiedTime.Format(CREATE_TIME_LAYOUT)),
			"etag":          []byte(o.Etag),
			"content-type":  []byte(o.ContentType),
			"attributes":    []byte{}, // TODO
			"ACL":           []byte(o.ACL.CannedAcl),
			"nullVersion":   []byte(helper.Ternary(o.NullVersion, "true", "false").(string)),
			"deleteMarker":  []byte(helper.Ternary(o.DeleteMarker, "true", "false").(string)),
			"sseType":       []byte(o.SseType),
			"encryptionKey": o.EncryptionKey,
			"IV":            o.InitializationVector,
		},
	}
	if len(o.Parts) != 0 {
		values[OBJECT_PART_COLUMN_FAMILY], err = valuesForParts(o.Parts)
		if err != nil {
			return
		}
	}
	return
}

func (o *Object) GetValuesForDelete() (values map[string]map[string][]byte) {
	return map[string]map[string][]byte{
		OBJECT_COLUMN_FAMILY:      map[string][]byte{},
		OBJECT_PART_COLUMN_FAMILY: map[string][]byte{},
	}
}

func (o *Object) GetVersionId() string {
	if o.VersionId != "" {
		return o.VersionId
	}
	if o.NullVersion {
		o.VersionId = "null"
		return o.VersionId
	}
	timeData := []byte(strconv.FormatUint(uint64(o.LastModifiedTime.UnixNano()), 10))
	o.VersionId = hex.EncodeToString(xxtea.Encrypt(timeData, XXTEA_KEY))
	return o.VersionId
}

func (o *Object) encryptSseKey() (err error) {
	// Don't encrypt if `EncryptionKey` is not set
	if len(o.EncryptionKey) == 0 {
		return
	}

	if len(o.InitializationVector) == 0 {
		o.InitializationVector = make([]byte, INITIALIZATION_VECTOR_LENGTH)
		_, err = io.ReadFull(rand.Reader, o.InitializationVector)
		if err != nil {
			return
		}
	}

	block, err := aes.NewCipher(SSE_S3_MASTER_KEY)
	if err != nil {
		return err
	}

	aesGcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	// InitializationVector is 16 bytes(because of CTR), but use only first 12 bytes in GCM
	// for performance
	o.EncryptionKey = aesGcm.Seal(nil, o.InitializationVector[:12], o.EncryptionKey, nil)
	return nil
}

// Rowkey format:
// BucketName +
// bigEndian(uint16(count("/", ObjectName))) +
// ObjectName +
// ObjectNameEnding +
// bigEndian(uint64.max - unixNanoTimestamp)
// The prefix excludes timestamp part if version is empty
func getObjectRowkeyPrefix(bucketName string, objectName string, version string) ([]byte, error) {
	var rowkey bytes.Buffer
	rowkey.WriteString(bucketName)
	err := binary.Write(&rowkey, binary.BigEndian, uint16(strings.Count(objectName, "/")))
	if err != nil {
		return []byte{}, err
	}
	rowkey.WriteString(objectName + ObjectNameEnding)
	if version != "" {
		decrypted, err := Decrypt(version)
		if err != nil {
			return []byte{}, err
		}
		unixNanoTimestamp, err := strconv.ParseUint(decrypted, 10, 64)
		if err != nil {
			return []byte{}, ErrInvalidVersioning
		}
		err = binary.Write(&rowkey, binary.BigEndian,
			math.MaxUint64-unixNanoTimestamp)
		if err != nil {
			return []byte{}, err
		}
	}
	return rowkey.Bytes(), nil
}

// Decode response from HBase and return an Object object
func ObjectFromResponse(response *hrpc.Result) (object *Object, err error) {
	var rowkey []byte
	object = new(Object)
	object.Parts = make(map[int]*Part)
	for _, cell := range response.Cells {
		rowkey = cell.Row
		switch string(cell.Family) {
		case OBJECT_COLUMN_FAMILY:
			switch string(cell.Qualifier) {
			case "bucket":
				object.BucketName = string(cell.Value)
			case "location":
				object.Location = string(cell.Value)
			case "pool":
				object.Pool = string(cell.Value)
			case "owner":
				object.OwnerId = string(cell.Value)
			case "size":
				err = binary.Read(bytes.NewReader(cell.Value), binary.BigEndian,
					&object.Size)
				if err != nil {
					return
				}
			case "oid":
				object.ObjectId = string(cell.Value)
			case "lastModified":
				object.LastModifiedTime, err = time.Parse(CREATE_TIME_LAYOUT,
					string(cell.Value))
				if err != nil {
					return
				}
			case "etag":
				object.Etag = string(cell.Value)
			case "content-type":
				object.ContentType = string(cell.Value)
			case "ACL":
				object.ACL.CannedAcl = string(cell.Value)
			case "nullVersion":
				object.NullVersion = helper.Ternary(string(cell.Value) == "true",
					true, false).(bool)
			case "deleteMarker":
				object.DeleteMarker = helper.Ternary(string(cell.Value) == "true",
					true, false).(bool)
			case "sseType":
				object.SseType = string(cell.Value)
			case "encryptionKey":
				object.EncryptionKey = cell.Value
			case "IV":
				object.InitializationVector = cell.Value
			}
		case OBJECT_PART_COLUMN_FAMILY:
			var partNumber int
			partNumber, err = strconv.Atoi(string(cell.Qualifier))
			if err != nil {
				return
			}
			var p Part
			err = json.Unmarshal(cell.Value, &p)
			if err != nil {
				return
			}
			object.Parts[partNumber] = &p
		}
	}

	// To decrypt encryption key, we need to know IV first
	object.EncryptionKey, err = decryptSseKey(object.InitializationVector, object.EncryptionKey)
	if err != nil {
		return
	}

	object.Rowkey = rowkey
	// rowkey = BucketName + bigEndian(uint16(count("/", ObjectName)))
	// + ObjectName
	// + ObjectNameEnding
	// + bigEndian(uint64.max - unixNanoTimestamp)
	object.Name = string(rowkey[len(object.BucketName)+2 : len(rowkey)-9])
	if object.NullVersion {
		object.VersionId = "null"
	} else {
		reversedTimeBytes := rowkey[len(rowkey)-8:]
		var reversedTime uint64
		err = binary.Read(bytes.NewReader(reversedTimeBytes), binary.BigEndian,
			&reversedTime)
		if err != nil {
			return
		}
		timestamp := math.MaxUint64 - reversedTime
		timeData := []byte(strconv.FormatUint(timestamp, 10))
		object.VersionId = hex.EncodeToString(xxtea.Encrypt(timeData, XXTEA_KEY))
	}
	helper.Debugln("ObjectFromResponse:", object)
	return
}

func (m *Meta) GetObject(bucketName string, objectName string) (object *Object, err error) {
	getObject := func() (o interface{}, err error) {
		objectRowkeyPrefix, err := getObjectRowkeyPrefix(bucketName, objectName, "")
		if err != nil {
			return
		}
		prefixFilter := filter.NewPrefixFilter(objectRowkeyPrefix)
		stopKey := helper.CopiedBytes(objectRowkeyPrefix)
		stopKey[len(stopKey)-1]++
		ctx, done := context.WithTimeout(RootContext, helper.CONFIG.HbaseTimeout)
		defer done()
		scanRequest, err := hrpc.NewScanRangeStr(ctx, OBJECT_TABLE,
			string(objectRowkeyPrefix), string(stopKey),
			hrpc.Filters(prefixFilter), hrpc.NumberOfRows(1))
		if err != nil {
			return
		}
		scanResponse, err := m.Hbase.Scan(scanRequest)
		if err != nil {
			return
		}
		helper.Debugln("GetObject scanResponse length:", len(scanResponse))
		if len(scanResponse) == 0 {
			err = ErrNoSuchKey
			return
		}
		object, err := ObjectFromResponse(scanResponse[0])
		if err != nil {
			return
		}
		helper.Debugln("GetObject object.Name:", object.Name)
		if object.Name != objectName {
			err = ErrNoSuchKey
			return
		}
		return object, nil
	}
	unmarshaller := func(in []byte) (interface{}, error) {
		var object Object
		err := json.Unmarshal(in, &object)
		return &object, err
	}
	o, err := m.Cache.Get(redis.ObjectTable, bucketName+":"+objectName+":",
		getObject, unmarshaller)
	if err != nil {
		return
	}
	object, ok := o.(*Object)
	if !ok {
		err = ErrInternalError
		return
	}
	return object, nil
}

func (m *Meta) GetNullVersionObject(bucketName, objectName string) (object *Object, err error) {
	objectRowkeyPrefix, err := getObjectRowkeyPrefix(bucketName, objectName, "")
	if err != nil {
		return
	}
	prefixFilter := filter.NewPrefixFilter(objectRowkeyPrefix)
	stopKey := helper.CopiedBytes(objectRowkeyPrefix)
	stopKey[len(stopKey)-1]++
	ctx, done := context.WithTimeout(RootContext, helper.CONFIG.HbaseTimeout)
	defer done()
	scanRequest, err := hrpc.NewScanRangeStr(ctx, OBJECT_TABLE,
		string(objectRowkeyPrefix), string(stopKey),
		// FIXME use a proper filter instead of naively getting 1000 and compare
		hrpc.Filters(prefixFilter), hrpc.NumberOfRows(1000))
	if err != nil {
		return
	}
	scanResponse, err := m.Hbase.Scan(scanRequest)
	if err != nil {
		return
	}
	if len(scanResponse) == 0 {
		err = ErrNoSuchVersion
		return
	}
	for _, response := range scanResponse {
		object, err = ObjectFromResponse(response)
		if err != nil {
			return
		}
		if object.Name == objectName && object.NullVersion {
			return object, nil
		}
	}
	return object, ErrNoSuchVersion
}

func (m *Meta) GetObjectVersion(bucketName, objectName, version string) (object *Object, err error) {
	getObjectVersion := func() (o interface{}, err error) {
		objectRowkeyPrefix, err := getObjectRowkeyPrefix(bucketName, objectName, version)
		if err != nil {
			return
		}
		ctx, done := context.WithTimeout(RootContext, helper.CONFIG.HbaseTimeout)
		defer done()
		getRequest, err := hrpc.NewGetStr(ctx, OBJECT_TABLE, string(objectRowkeyPrefix))
		if err != nil {
			return
		}
		getResponse, err := m.Hbase.Get(getRequest)
		if err != nil {
			return
		}
		if len(getResponse.Cells) == 0 {
			err = ErrNoSuchVersion
			return
		}
		object, err := ObjectFromResponse(getResponse)
		if err != nil {
			return
		}
		if object.Name != objectName {
			err = ErrNoSuchKey
			return
		}
		return object, nil
	}
	unmarshaller := func(in []byte) (interface{}, error) {
		var object Object
		err := json.Unmarshal(in, &object)
		return &object, err
	}
	o, err := m.Cache.Get(redis.ObjectTable, bucketName+":"+objectName+":"+version,
		getObjectVersion, unmarshaller)
	if err != nil {
		return
	}
	object, ok := o.(*Object)
	if !ok {
		err = ErrInternalError
		return
	}
	return object, nil
}

func (m *Meta) PutObjectEntry(object *Object) error {
	rowkey, err := object.GetRowkey()
	if err != nil {
		return err
	}
	values, err := object.GetValues()
	if err != nil {
		return err
	}
	helper.Debugln("values", values)
	ctx, done := context.WithTimeout(RootContext, helper.CONFIG.HbaseTimeout)
	defer done()
	put, err := hrpc.NewPutStr(ctx, OBJECT_TABLE, rowkey, values)
	if err != nil {
		return err
	}
	_, err = m.Hbase.Put(put)
	return err
}

func (m *Meta) DeleteObjectEntry(object *Object) error {
	rowkeyToDelete, err := object.GetRowkey()
	if err != nil {
		return err
	}
	ctx, done := context.WithTimeout(RootContext, helper.CONFIG.HbaseTimeout)
	defer done()
	deleteRequest, err := hrpc.NewDelStr(ctx, OBJECT_TABLE, rowkeyToDelete,
		object.GetValuesForDelete())
	if err != nil {
		return err
	}
	_, err = m.Hbase.Delete(deleteRequest)
	return err
}

func decryptSseKey(initializationVector []byte, cipherText []byte) (plainText []byte, err error) {
	if len(cipherText) == 0 {
		return
	}

	block, err := aes.NewCipher(SSE_S3_MASTER_KEY)
	if err != nil {
		return
	}

	aesGcm, err := cipher.NewGCM(block)
	if err != nil {
		return
	}

	// InitializationVector is 16 bytes(because of CTR), but use only first 12 bytes in GCM
	// for performance
	return aesGcm.Open(nil, initializationVector[:12], cipherText, nil)
}
