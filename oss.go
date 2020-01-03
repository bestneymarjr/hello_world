package internal

// 通过aliclould sdk实现oss cacher

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"go.danale.net/be/be/biz/devops/devops-svr/go-proxy-svr/goproxy"
	"go.danale.net/be/be/go/logx"
	"golang.org/x/net/context"
)

// OSS implements the `goproxy.Cacher` by using the Alibaba Cloud Object Storage
// Service.
type OSS struct {
	// Endpoint is the endpoint of the Alibaba Cloud Object Storage Service.
	//
	// If the `Endpoint` is empty,
	// the "https://oss-cn-hangzhou.aliyuncs.com" is used.
	Endpoint string `mapstructure:"endpoint"`

	// AccessKeyID is the access key ID of the Alibaba Cloud.
	AccessKeyID string `mapstructure:"access_key_id"`

	// AccessKeySecret is the access key secret of the Alibaba Cloud.
	AccessKeySecret string `mapstructure:"access_key_secret"`

	// BucketName is the name of the bucket.
	BucketName string `mapstructure:"bucket_name"`

	// Root is the root of the caches.
	Root string `mapstructure:"root"`

	loadOnce  sync.Once
	loadError error
	bucket    *oss.Bucket
}

// load loads the stuff of the m up.
func (o *OSS) load() {
	var client *oss.Client
	if client, o.loadError = oss.New(o.Endpoint, o.AccessKeyID, o.AccessKeySecret); o.loadError != nil {
		return
	}
	if o.bucket, o.loadError = client.Bucket(o.BucketName); o.loadError != nil {
		return
	}
}

// NewHash implements the `goproxy.Cacher`.
func (o *OSS) NewHash() hash.Hash {
	return md5.New()
}

// Cache implements the `goproxy.Cacher`.
func (o *OSS) Cache(ctx context.Context, name string) (goproxy.Cache, error) {
	if o.loadOnce.Do(o.load); o.loadError != nil {
		return nil, o.loadError
	}

	objectName := path.Join(o.Root, name)
	logx.X.Debug(objectName)
	if e, err := o.bucket.IsObjectExist(objectName); err != nil {
		return nil, err
	} else if !e {
		return nil, goproxy.ErrCacheNotFound
	}

	h, err := o.bucket.GetObjectMeta(objectName)
	if err != nil {
		return nil, err
	}

	contentLength, err := strconv.ParseInt(h.Get("Content-Length"), 10, 64)
	if err != nil {
		return nil, err
	}

	lastModified, err := http.ParseTime(h.Get("Last-Modified"))
	if err != nil {
		return nil, err
	}

	checksum, err := hex.DecodeString(strings.Trim(h.Get("ETag"), `"`))
	if err != nil {
		return nil, err
	}

	mimeType := mime.TypeByExtension(path.Ext(name))

	return &ossCache{
		bucket:     o.bucket,
		objectName: objectName,
		mimeType:   mimeType,
		name:       name,
		size:       contentLength,
		modTime:    lastModified,
		checksum:   checksum,
	}, nil

}

// SetCache implements the `goproxy.Cacher`.
func (o *OSS) SetCache(ctx context.Context, c goproxy.Cache) error {
	if o.loadOnce.Do(o.load); o.loadError != nil {
		return o.loadError
	}

	logx.X.Debug(c.MIMEType())
	return o.bucket.PutObject(
		path.Join(o.Root, c.Name()),
		c,
		oss.ContentType(mime.TypeByExtension(path.Ext(c.Name()))),
	)
}

// ossCache implements the `goproxy.Cache`. It is the cache unit of the `OSS`.
type ossCache struct {
	bucket     *oss.Bucket
	objectName string
	mimeType   string
	offset     int64
	closed     bool
	name       string
	size       int64
	modTime    time.Time
	checksum   []byte
}

// Read implements the `goproxy.Cache`.
func (oc *ossCache) Read(b []byte) (int, error) {
	if oc.closed {
		return 0, os.ErrClosed
	} else if oc.offset >= oc.size {
		return 0, io.EOF
	}

	rc, err := oc.bucket.GetObject(
		oc.objectName,
		oss.Range(oc.offset, oc.size),
	)
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	n, err := rc.Read(b)
	oc.offset += int64(n)

	return n, err
}

// Seek implements the `goproxy.Cache`.
func (oc *ossCache) Seek(offset int64, whence int) (int64, error) {
	if oc.closed {
		return 0, os.ErrClosed
	}

	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += oc.offset
	case io.SeekEnd:
		offset += oc.size
	default:
		return 0, errors.New("invalid whence")
	}

	if offset < 0 {
		return 0, errors.New("negative position")
	}

	oc.offset = offset

	return oc.offset, nil
}

// Close implements the `goproxy.Cache`.
func (oc *ossCache) Close() error {
	if oc.closed {
		return os.ErrClosed
	}

	oc.closed = true

	return nil
}

// Name implements the `goproxy.Cache`.
func (oc *ossCache) Name() string {
	return oc.name
}

// Size implements the `goproxy.Cache`.
func (oc *ossCache) Size() int64 {
	return oc.size
}

// ModTime implements the `goproxy.Cache`.
func (oc *ossCache) ModTime() time.Time {
	return oc.modTime
}

// Checksum implements the `goproxy.Cache`.
func (oc *ossCache) Checksum() []byte {
	return oc.checksum
}

func (oc *ossCache) MIMEType() string {
	return oc.mimeType
}
