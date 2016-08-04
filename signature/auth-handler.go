/*
 * Minio Cloud Storage, (C) 2015 Minio, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package signature

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io/ioutil"
	"net/http"
	"strings"

	. "git.letv.cn/yig/yig/error"
	"git.letv.cn/yig/yig/iam"
	. "git.letv.cn/yig/yig/minio/datatype"
)

// Verify if the request http Header "x-amz-content-sha256" == "UNSIGNED-PAYLOAD"
func isRequestUnsignedPayload(r *http.Request) bool {
	return r.Header.Get("x-amz-content-sha256") == unsignedPayload
}

// Verify if request has AWS Signature
// for v2, the Authorization header starts with "AWS ",
// for v4, starts with "AWS4-HMAC-SHA256 " (notice the space after string)
func isRequestSignature(r *http.Request) (bool, AuthType) {
	if _, ok := r.Header["Authorization"]; ok {
		header := r.Header.Get("Authorization")
		if strings.HasPrefix(header, signV4Algorithm+" ") {
			return true, AuthTypeSignedV4
		} else if strings.HasPrefix(header, SignV2Algorithm+" ") {
			return true, AuthTypeSignedV2
		}
	}
	return false, AuthTypeUnknown
}

// Verify if request is AWS presigned
func isRequestPresigned(r *http.Request) (bool, AuthType) {
	if _, ok := r.URL.Query()["X-Amz-Credential"]; ok {
		return true, AuthTypePresignedV4
	} else if _, ok := r.URL.Query()["AWSAccessKeyId"]; ok {
		return true, AuthTypePresignedV2
	}
	return false, AuthTypeUnknown
}

// Verify if request is of type AWS POST policy Signature
func isRequestPostPolicySignature(r *http.Request) bool {
	if r.Method != "POST" {
		return false
	}
	if _, ok := r.Header["Content-Type"]; ok {
		if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			return true
		}
	}
	return false
}

// Authorization type.
type AuthType int

// List of all supported auth types.
const (
	AuthTypeUnknown AuthType = iota
	AuthTypeAnonymous
	AuthTypePresignedV4
	AuthTypePresignedV2
	AuthTypePostPolicy // including v2 and v4, handled specially in API endpoint
	AuthTypeSignedV4
	AuthTypeSignedV2
)

// Get request authentication type.
func GetRequestAuthType(r *http.Request) AuthType {
	if isSignature, version := isRequestSignature(r); isSignature {
		return version
	} else if isPresigned, version := isRequestPresigned(r); isPresigned {
		return version
	} else if isRequestPostPolicySignature(r) {
		return AuthTypePostPolicy
	} else if _, ok := r.Header["Authorization"]; !ok {
		return AuthTypeAnonymous
	}
	return AuthTypeUnknown
}

// sum256 calculate sha256 sum for an input byte array
func sum256(data []byte) []byte {
	hash := sha256.New()
	hash.Write(data)
	return hash.Sum(nil)
}

// sumMD5 calculate md5 sum for an input byte array
func sumMD5(data []byte) []byte {
	hash := md5.New()
	hash.Write(data)
	return hash.Sum(nil)
}

// A helper function to verify if request has valid AWS Signature
func IsReqAuthenticated(r *http.Request) (c iam.Credential, e error) {
	if r == nil {
		return c, ErrInternalError
	}
	payload, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return c, ErrInternalError
	}
	// Verify Content-Md5, if payload is set.
	if r.Header.Get("Content-Md5") != "" {
		if r.Header.Get("Content-Md5") != base64.StdEncoding.EncodeToString(sumMD5(payload)) {
			return c, ErrBadDigest
		}
	}
	// Populate back the payload.
	r.Body = ioutil.NopCloser(bytes.NewReader(payload))
	validateRegion := true // TODO: Validate region.
	switch GetRequestAuthType(r) {
	case AuthTypePresignedV4:
		return DoesPresignedSignatureMatchV4(r, validateRegion)
	case AuthTypeSignedV4:
		return DoesSignatureMatchV4(hex.EncodeToString(sum256(payload)), r, validateRegion)
	case AuthTypePresignedV2:
		return DoesPresignedSignatureMatchV2(r)
	case AuthTypeSignedV2:
		return DoesSignatureMatchV2(r)
	}
	return c, ErrAccessDenied
}

// authHandler - handles all the incoming authorization headers and
// validates them if possible.
type AuthHandler struct {
	handler http.Handler
}

// setAuthHandler to validate authorization header for the incoming request.
func SetAuthHandler(h http.Handler) http.Handler {
	return AuthHandler{h}
}

// handler for validating incoming authorization headers.
func (a AuthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch GetRequestAuthType(r) {
	case AuthTypeUnknown:
		WriteErrorResponse(w, r, ErrSignatureVersionNotSupported, r.URL.Path)
		return
	default:
		// Let top level caller validate for anonymous and known
		// signed requests.
		a.handler.ServeHTTP(w, r)
		return
	}
}
