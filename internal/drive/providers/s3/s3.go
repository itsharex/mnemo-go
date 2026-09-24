// Package s3 implements the S3-compatible object storage provider
// (mounted storage, direct upload).
package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

const providerID = model.ProviderS3

const (
	s3MultipartThreshold = 16 * 1024 * 1024
	s3MultipartPartSize  = 16 * 1024 * 1024
	s3CopyCutoff         = 5*1024*1024*1024 - 64*1024*1024
	s3CopyPartSize       = 64 * 1024 * 1024
)

func init() {
	drive.Register(drive.Registration{
		ID:   providerID,
		Meta: drive.GetMeta(providerID),
		Caps: drive.NewCapabilities(providerID, map[string]bool{
			"mountedStorage":  true,
			"createShare":     true,
			"shareExpiration": true,
			"permanentDelete": true,
			"recycleBin":      false,
			"trashView":       false,
		}, func(c *drive.Capabilities) {
			c.SetUploadMode(drive.UploadModeDirect)
			c.SetShareExpirationOptions(1, 7)
		}),
		Factory: func() drive.Driver { return &Driver{} },
	})
}

// Driver implements drive.Driver for S3. File ids are bucket-relative keys.
type Driver struct {
	drive.BaseDriver
}

func (d *Driver) ID() string                       { return providerID }
func (d *Driver) Meta() drive.Meta                 { return drive.GetMeta(providerID) }
func (d *Driver) Capabilities() drive.Capabilities { return drive.RegistryCaps(providerID) }
func (d *Driver) RootID() string                   { return "/" }

func (d *Driver) ValidateConnection(ctx context.Context, cfg *model.ConnConfig) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if cfg == nil {
		return errors.New("s3: 连接配置为空")
	}
	c, err := connOf(drive.Context{Token: &model.TokenInfo{Conn: cfg}})
	if err != nil {
		return err
	}
	if c.prefix != "" {
		_, err := c.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(c.bucket), Prefix: aws.String(c.prefix), Delimiter: aws.String("/"), MaxKeys: aws.Int32(1),
		})
		return err
	}
	_, headErr := c.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(c.bucket)})
	if headErr == nil {
		return nil
	}
	if !canFallbackFromHeadBucket(headErr) {
		return fmt.Errorf("s3: bucket 校验失败: %w", headErr)
	}
	// Prefix-scoped IAM policies frequently permit object listing but deny
	// s3:ListBucket without a prefix condition, which makes HeadBucket return
	// 403 even though the configured mount is usable. Only perform this second
	// request after a compatibility/permission response to keep validation cheap.
	_, listErr := c.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(c.bucket),
		Prefix:  aws.String(c.prefix),
		MaxKeys: aws.Int32(1),
	})
	if listErr == nil {
		return nil
	}
	return fmt.Errorf("s3: bucket 与前缀校验均失败: HeadBucket: %v; ListObjectsV2: %w", headErr, listErr)
}

func ccAllowsPrivateNetwork(c drive.Context) bool {
	return c.Token != nil && c.Token.Conn != nil && c.Token.Conn.AllowPrivateNetwork
}

// ValidateWriteConnection performs an opt-in, low-volume write probe. It
// writes one empty object under a reserved random key and deletes it before
// returning. Login validation deliberately does not call this method.
func (d *Driver) ValidateWriteConnection(ctx context.Context, cfg *model.ConnConfig) error {
	if cfg == nil {
		return errors.New("s3: 连接配置为空")
	}
	c, err := connOf(drive.Context{Token: &model.TokenInfo{Conn: cfg}})
	if err != nil {
		return err
	}
	key, err := writeProbeKey(c.prefix)
	if err != nil {
		return err
	}
	if _, err := c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(c.bucket), Key: aws.String(key), Body: strings.NewReader(""), ContentLength: aws.Int64(0),
	}); err != nil {
		return fmt.Errorf("s3: 写入权限验证失败: %w", err)
	}
	if _, err := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("s3: 写入验证成功，但清理测试对象失败（对象: %s）: %w", key, err)
	}
	return nil
}

func writeProbeKey(prefix string) (string, error) {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("s3: 生成写入验证对象名失败: %w", err)
	}
	return prefix + ".mnemo-connection-check/" + fmt.Sprintf("%x", suffix[:]), nil
}

type conn struct {
	client *s3.Client
	bucket string
	prefix string // base path prefix, "" or "dir/"
}

// TransportOverride, when set, is used as the S3 SDK transport. Test-only hook
// to route real S3 traffic to a local mock (never set in production).
var TransportOverride http.RoundTripper

func httpClientForS3() *http.Client {
	const timeout = 60 * time.Second
	if TransportOverride != nil {
		return &http.Client{Transport: TransportOverride, Timeout: timeout}
	}
	return netx.NewClientWithSystemProxy(timeout).HTTP
}

func canFallbackFromHeadBucket(err error) bool {
	var responseErr *awshttp.ResponseError
	if !errors.As(err, &responseErr) {
		return false
	}
	switch responseErr.HTTPStatusCode() {
	case http.StatusForbidden, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func connOf(c drive.Context) (*conn, error) {
	cfg := c.Token
	if cfg == nil || cfg.Conn == nil {
		return nil, errors.New("s3: 连接不存在，请重新连接")
	}
	cc := cfg.Conn
	endpoint, err := normalizeEndpoint(cc.Endpoint)
	if err != nil {
		return nil, err
	}
	bucket := strings.TrimSpace(cc.Bucket)
	if err := validateBucket(bucket); err != nil {
		return nil, err
	}
	prefix, err := normalizePrefix(cc.BasePath)
	if err != nil {
		return nil, err
	}
	if bucket == "" {
		return nil, errors.New("s3: 缺少 bucket")
	}
	// Path-style addressing is the default (MinIO / Aliyun OSS / most S3-compatible
	// endpoints need it). Users who want virtual-host style (e.g. AWS S3 native)
	// set ForcePathStyle=false explicitly via a non-nil pointer.
	usePathStyle := true
	if cc.ForcePathStyle != nil {
		usePathStyle = *cc.ForcePathStyle
	}
	options := s3.Options{
		Region:           firstNonEmpty(cc.Region, "us-east-1"),
		Credentials:      credentials.NewStaticCredentialsProvider(firstNonEmpty(cc.Username, "minioadmin"), cc.Password, cc.SessionToken),
		UsePathStyle:     usePathStyle,
		HTTPClient:       httpClientForS3(),
		RetryMaxAttempts: 2,
		RetryMode:        aws.RetryModeStandard,
	}
	// An empty endpoint means the AWS SDK's regional endpoint. Custom S3
	// compatible services still provide their endpoint explicitly.
	if endpoint != "" {
		options.BaseEndpoint = aws.String(endpoint)
	}
	client := s3.New(options)
	return &conn{client: client, bucket: bucket, prefix: prefix}, nil
}

func (c *conn) keyOf(id string) string {
	key := strings.TrimPrefix(pathOf(id), "/")
	return c.prefix + key
}

func (c *conn) idOf(key string) string {
	return "/" + strings.TrimPrefix(key, c.prefix)
}

func pathOf(id string) string {
	if id == "" || id == "/" || id == "root" {
		return "/"
	}
	if !strings.HasPrefix(id, "/") {
		return "/" + id
	}
	return id
}

func normalizePrefix(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "", nil
	}
	if strings.ContainsRune(p, '\\') || strings.IndexFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", errors.New("s3: base path 含有非法字符")
	}
	parts := make([]string, 0, strings.Count(p, "/")+1)
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", errors.New("s3: base path 不允许路径穿越")
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return strings.Join(parts, "/") + "/", nil
}

func normalizeEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("s3: endpoint 必须是有效的主机地址")
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return "", errors.New("s3: endpoint scheme 必须是 http 或 https")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("s3: endpoint 不能包含账号、查询参数或片段")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func validateBucket(bucket string) error {
	if bucket == "" {
		return errors.New("s3: 缺少 bucket")
	}
	if strings.ContainsAny(bucket, "/\\") || strings.IndexFunc(bucket, func(r rune) bool { return r < 0x20 || r == 0x7f || r == ' ' }) >= 0 {
		return errors.New("s3: bucket 名称无效")
	}
	return nil
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" || name == "." || name == ".." {
		return errors.New("s3: 名称为空或无效")
	}
	if strings.ContainsAny(name, "/\\") || strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return errors.New("s3: 名称不能包含路径分隔符或控制字符")
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (d *Driver) List(ctx context.Context, c drive.Context, dirID string, _ *drive.ListOptions) ([]model.File, error) {
	cc, err := connOf(c)
	if err != nil {
		return nil, err
	}
	prefix := cc.keyOf(dirID)
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	var out []model.File
	var token *string
	seenTokens := map[string]struct{}{}
	for {
		requestToken := ""
		if token != nil {
			requestToken = aws.ToString(token)
			if requestToken == "" {
				return nil, errors.New("s3: 列表分页返回空游标")
			}
			if _, exists := seenTokens[requestToken]; exists {
				return nil, errors.New("s3: 列表分页游标重复")
			}
			seenTokens[requestToken] = struct{}{}
		}
		resp, err := cc.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(cc.bucket),
			Prefix:            aws.String(prefix),
			Delimiter:         aws.String("/"),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("s3: list: %w", err)
		}
		for _, p := range resp.CommonPrefixes {
			if p.Prefix == nil {
				continue
			}
			key := strings.TrimSuffix(*p.Prefix, "/")
			if key == "" {
				continue
			}
			name := baseOf(key)
			out = append(out, driveutil.NewFile(c.DriveID, cc.idOf(key), dirID, name, true, 0, 0))
		}
		for _, o := range resp.Contents {
			if o.Key == nil {
				continue
			}
			key := *o.Key
			// A directory marker is an implementation detail, not a child file.
			if key == strings.TrimSuffix(prefix, "/") || key == prefix || strings.HasSuffix(key, "/") {
				continue
			}
			name := baseOf(key)
			size := int64(0)
			if o.Size != nil {
				size = *o.Size
			}
			t := time.Time{}
			if o.LastModified != nil {
				t = *o.LastModified
			}
			out = append(out, driveutil.NewFile(c.DriveID, cc.idOf(key), dirID, name, false, size, t.Unix()))
		}
		if resp.IsTruncated == nil || !*resp.IsTruncated {
			break
		}
		nextToken := aws.ToString(resp.NextContinuationToken)
		if nextToken == "" {
			return nil, errors.New("s3: 列表分页缺少下一页游标")
		}
		token = aws.String(nextToken)
	}
	return out, nil
}

func (d *Driver) GetInfo(ctx context.Context, c drive.Context, fileID string) (any, error) {
	cc, err := connOf(c)
	if err != nil {
		return nil, err
	}
	p := pathOf(fileID)
	if p == "/" {
		return driveutil.NewFile(c.DriveID, "/", "", "S3", true, 0, 0), nil
	}
	key := cc.keyOf(p)
	head, err := cc.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(cc.bucket), Key: aws.String(key)})
	if err != nil {
		if !isNotFoundError(err) {
			return nil, fmt.Errorf("s3: 获取对象信息失败: %w", err)
		}
		// maybe it's a folder prefix
		ls, lerr := cc.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(cc.bucket), Prefix: aws.String(key + "/"), MaxKeys: aws.Int32(1),
		})
		if lerr != nil {
			return nil, fmt.Errorf("s3: 检查目录失败: %w", lerr)
		}
		if len(ls.Contents) > 0 || len(ls.CommonPrefixes) > 0 {
			return driveutil.NewFile(c.DriveID, cc.idOf(key), parentOf(p), baseOf(key), true, 0, 0), nil
		}
		return nil, drive.ErrNotFound
	}
	size := int64(0)
	if head.ContentLength != nil {
		size = *head.ContentLength
	}
	var modified int64
	if head.LastModified != nil {
		modified = head.LastModified.Unix()
	}
	return driveutil.NewFile(c.DriveID, cc.idOf(key), parentOf(p), baseOf(key), false, size, modified), nil
}

func (d *Driver) GetFile(ctx context.Context, c drive.Context, fileID string) (*model.File, error) {
	v, err := d.GetInfo(ctx, c, fileID)
	if err != nil {
		return nil, err
	}
	f, ok := v.(model.File)
	if !ok {
		return nil, drive.ErrNotFound
	}
	return &f, nil
}

func (d *Driver) GetDownloadURL(ctx context.Context, c drive.Context, fileID string, expireSec int) (*model.DownloadURL, error) {
	cc, err := connOf(c)
	if err != nil {
		return nil, err
	}
	p := pathOf(fileID)
	info, err := d.GetInfo(ctx, c, p)
	if err != nil {
		return nil, err
	}
	f, ok := info.(model.File)
	if !ok {
		return nil, drive.ErrNotFound
	}
	if f.IsDir {
		return nil, errors.New("文件夹不能直接下载")
	}
	if expireSec < 60 {
		expireSec = 14400
	}
	presigner := s3.NewPresignClient(cc.client)
	req, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(cc.bucket),
		Key:    aws.String(cc.keyOf(p)),
	}, func(po *s3.PresignOptions) {
		po.Expires = time.Duration(expireSec) * time.Second
	})
	if err != nil {
		return nil, err
	}
	return &model.DownloadURL{
		DriveID:             c.DriveID,
		FileID:              fileID,
		ExpireTime:          time.Now().Add(time.Duration(expireSec) * time.Second).UnixMilli(),
		URL:                 req.URL,
		Size:                f.Size,
		DownloadMode:        "redirect",
		AllowPrivateNetwork: ccAllowsPrivateNetwork(c),
	}, nil
}

func (d *Driver) GetVideoPreview(ctx context.Context, c drive.Context, fileID string) (*model.VideoPreview, error) {
	u, err := d.GetDownloadURL(ctx, c, fileID, 3600)
	if err != nil {
		return nil, err
	}
	return &model.VideoPreview{
		DriveID:             c.DriveID,
		FileID:              fileID,
		Size:                u.Size,
		AllowPrivateNetwork: u.AllowPrivateNetwork,
		Qualities: []model.VideoQuality{{
			Quality: "origin", Label: "原画", Value: "origin",
			URL: u.URL, Headers: u.Headers, ForceProxy: true,
		}},
	}, nil
}

func (d *Driver) Mkdir(ctx context.Context, c drive.Context, parentID, name string) (*drive.MkdirResult, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	cc, err := connOf(c)
	if err != nil {
		return nil, err
	}
	key := cc.keyOf(driveutil.JoinPath(pathOf(parentID), name)) + "/"
	_, err = cc.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cc.bucket), Key: aws.String(key)})
	if err != nil {
		return &drive.MkdirResult{Error: err.Error()}, nil
	}
	return &drive.MkdirResult{FileID: cc.idOf(strings.TrimSuffix(key, "/"))}, nil
}

func (d *Driver) Rename(ctx context.Context, c drive.Context, fileID, name string) (*drive.RenameResult, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	cc, err := connOf(c)
	if err != nil {
		return nil, err
	}
	p := pathOf(fileID)
	info, err := d.GetInfo(ctx, c, p)
	if err != nil {
		return nil, err
	}
	f, ok := info.(model.File)
	if !ok {
		return nil, drive.ErrNotFound
	}
	to := driveutil.JoinPath(parentOf(p), name)
	if err := d.copyObj(ctx, cc, cc.keyOf(p), cc.keyOf(to)); err != nil {
		return nil, err
	}
	if err := cc.deleteRecursive(ctx, cc.keyOf(p)); err != nil {
		return nil, fmt.Errorf("s3: 重命名后删除源对象失败: %w", err)
	}
	return &drive.RenameResult{FileID: cc.idOf(strings.TrimPrefix(cc.keyOf(to), cc.prefix)), ParentFileID: parentOf(to), Name: name, IsDir: f.IsDir}, nil
}

func (d *Driver) Trash(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	return d.Delete(ctx, c, idsToRefs(fileIDs))
}

func (d *Driver) Delete(ctx context.Context, c drive.Context, refs []drive.FileRef) ([]string, error) {
	cc, err := connOf(c)
	if err != nil {
		return nil, err
	}
	var ok []string
	var failed []error
	for _, ref := range refs {
		if err := cc.deleteRecursive(ctx, cc.keyOf(ref.ID)); err == nil {
			ok = append(ok, ref.ID)
		} else {
			failed = append(failed, fmt.Errorf("%s: %w", ref.ID, err))
		}
	}
	return ok, errors.Join(failed...)
}

func (c *conn) deleteObj(ctx context.Context, key string) error {
	if key == "" || key == "/" {
		return nil
	}
	_, err := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	return err
}

// listAllUnder returns every object key under the given prefix (no delimiter),
// paginating through all results. Used for recursive directory operations.
func (c *conn) listAllUnder(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	var token *string
	seenTokens := map[string]struct{}{}
	for {
		requestToken := ""
		if token != nil {
			requestToken = aws.ToString(token)
			if requestToken == "" {
				return nil, errors.New("s3: 对象分页返回空游标")
			}
			if _, exists := seenTokens[requestToken]; exists {
				return nil, errors.New("s3: 对象分页游标重复")
			}
			seenTokens[requestToken] = struct{}{}
		}
		resp, err := c.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(c.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, obj := range resp.Contents {
			if obj.Key != nil {
				out = append(out, *obj.Key)
			}
		}
		if resp.IsTruncated == nil || !*resp.IsTruncated {
			break
		}
		nextToken := aws.ToString(resp.NextContinuationToken)
		if nextToken == "" {
			return nil, errors.New("s3: 对象分页缺少下一页游标")
		}
		token = aws.String(nextToken)
	}
	return out, nil
}

// deleteRecursive removes a key and, when it is a directory prefix, every
// object under it. Deletion is batched (DeleteObjects, 1000 per batch) for
// efficiency.
func (c *conn) deleteRecursive(ctx context.Context, key string) error {
	if key == "" || key == "/" {
		return nil
	}
	// gather the object itself plus everything under key + "/"
	prefix := strings.TrimPrefix(key, "/")
	keys := []string{prefix}
	under, err := c.listAllUnder(ctx, prefix+"/")
	if err != nil {
		// Listing may fail for a plain object on a restricted-compatible
		// service; only fall back when HEAD confirms that exact object exists.
		exists, headErr := c.objectExists(ctx, prefix)
		if headErr != nil || !exists {
			return err
		}
		return c.deleteObj(ctx, key)
	}
	keys = append(keys, under...)
	// batch delete (max 1000 per DeleteObjects call)
	for i := 0; i < len(keys); i += 1000 {
		end := i + 1000
		if end > len(keys) {
			end = len(keys)
		}
		var objs []types.ObjectIdentifier
		for _, k := range keys[i:end] {
			objs = append(objs, types.ObjectIdentifier{Key: aws.String(k)})
		}
		out, err := c.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(c.bucket),
			Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return err
		}
		if len(out.Errors) > 0 {
			return fmt.Errorf("s3: 批量删除失败: %s", deleteErrors(out.Errors))
		}
	}
	return nil
}

// copyRecursive copies a single object (CopyObject) or, when the source is a
// directory prefix, every object under it to the destination prefix.
func (c *conn) copyRecursive(ctx context.Context, fromKey, toKey string) error {
	fromKey = strings.TrimSuffix(strings.TrimPrefix(fromKey, "/"), "/")
	toKey = strings.TrimSuffix(strings.TrimPrefix(toKey, "/"), "/")
	if fromKey == "" || toKey == "" {
		return errors.New("s3: 不允许复制根目录")
	}
	if fromKey == toKey || strings.HasPrefix(toKey, fromKey+"/") {
		return errors.New("s3: 不能复制到自身或子目录")
	}
	marker, err := c.objectExists(ctx, fromKey)
	if err != nil {
		return err
	}
	under, err := c.listAllUnder(ctx, fromKey+"/")
	if err != nil {
		if !marker {
			return err
		}
		return c.copyOne(ctx, fromKey, toKey)
	}
	if len(under) == 0 && marker {
		return c.copyOne(ctx, fromKey, toKey)
	}
	if !marker && len(under) == 0 {
		return errors.New("s3: 源对象不存在")
	}
	if marker {
		if err := c.copyOne(ctx, fromKey, toKey); err != nil {
			return err
		}
	}
	for _, k := range under {
		dst := toKey + "/" + strings.TrimPrefix(k, fromKey+"/")
		if err := c.copyOne(ctx, k, dst); err != nil {
			return err
		}
	}
	return nil
}

func (c *conn) copyOne(ctx context.Context, from, to string) error {
	if from == "" || from == "/" {
		return nil
	}
	head, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket), Key: aws.String(from),
	})
	if err != nil {
		if isNotFoundError(err) {
			return drive.ErrNotFound
		}
		return err
	}
	if head.ContentLength != nil && *head.ContentLength > s3CopyCutoff {
		return c.copyMultipart(ctx, from, to, *head.ContentLength)
	}
	_, err = c.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(c.bucket),
		CopySource: aws.String(copySource(c.bucket, from)),
		Key:        aws.String(to),
	})
	return err
}

func (c *conn) copyMultipart(ctx context.Context, from, to string, size int64) (err error) {
	created, err := c.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(c.bucket), Key: aws.String(to),
	})
	if err != nil {
		return err
	}
	if created.UploadId == nil || *created.UploadId == "" {
		return errors.New("multipart copy: 服务端未返回 upload id")
	}
	uploadID := *created.UploadId
	completed := false
	defer func() {
		if !completed {
			_, _ = c.client.AbortMultipartUpload(context.Background(), &s3.AbortMultipartUploadInput{
				Bucket: aws.String(c.bucket), Key: aws.String(to), UploadId: aws.String(uploadID),
			})
		}
	}()

	partSize := int64(s3CopyPartSize)
	if size > partSize*10000 {
		partSize = (size + 9999) / 10000
		const minPartSize = int64(5 * 1024 * 1024)
		partSize = ((partSize + minPartSize - 1) / minPartSize) * minPartSize
	}
	parts := make([]types.CompletedPart, 0, int((size+partSize-1)/partSize))
	for partNumber, offset := int32(1), int64(0); offset < size; partNumber, offset = partNumber+1, offset+partSize {
		end := offset + partSize
		if end > size {
			end = size
		}
		copied, copyErr := c.client.UploadPartCopy(ctx, &s3.UploadPartCopyInput{
			Bucket:          aws.String(c.bucket),
			Key:             aws.String(to),
			UploadId:        aws.String(uploadID),
			PartNumber:      aws.Int32(partNumber),
			CopySource:      aws.String(copySource(c.bucket, from)),
			CopySourceRange: aws.String(fmt.Sprintf("bytes=%d-%d", offset, end-1)),
		})
		if copyErr != nil {
			return copyErr
		}
		if copied.CopyPartResult == nil || copied.CopyPartResult.ETag == nil || *copied.CopyPartResult.ETag == "" {
			return fmt.Errorf("multipart copy: part %d 未返回 etag", partNumber)
		}
		parts = append(parts, types.CompletedPart{
			PartNumber: aws.Int32(partNumber), ETag: copied.CopyPartResult.ETag,
		})
	}
	_, err = c.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(c.bucket), Key: aws.String(to), UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		return err
	}
	completed = true
	return nil
}

func copySource(bucket, key string) string {
	return (&url.URL{Path: "/" + bucket + "/" + strings.TrimPrefix(key, "/")}).EscapedPath()
}

func deleteErrors(items []types.Error) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		code, msg := "", ""
		if item.Code != nil {
			code = *item.Code
		}
		if item.Message != nil {
			msg = *item.Message
		}
		parts = append(parts, strings.TrimSpace(code+" "+msg))
	}
	return strings.Join(parts, "; ")
}

// objectExists returns whether an object exists at key. A 404 (or NoSuchKey)
// reports exists=false,nil. Other errors are returned as-is.
func (c *conn) objectExists(ctx context.Context, key string) (bool, error) {
	_, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	if err == nil {
		return true, nil
	}
	if isNotFoundError(err) {
		return false, nil
	}
	return false, err
}

// objectState distinguishes a plain object from a virtual directory. A
// directory may have no marker object, so HEAD alone is not sufficient for
// upload conflict handling.
func (c *conn) objectState(ctx context.Context, key string) (exists, isDir bool, err error) {
	if _, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket), Key: aws.String(key),
	}); err == nil {
		return true, false, nil
	} else if !isNotFoundError(err) {
		return false, false, err
	}

	listing, err := c.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.bucket), Prefix: aws.String(key + "/"), MaxKeys: aws.Int32(1),
	})
	if err != nil {
		return false, false, err
	}
	found := len(listing.Contents) > 0 || len(listing.CommonPrefixes) > 0
	return found, found, nil
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch strings.ToLower(apiErr.ErrorCode()) {
		case "nosuchkey", "notfound", "nosuchobject":
			return true
		case "nosuchbucket":
			return false
		}
	}
	var responseErr *awshttp.ResponseError
	return errors.As(err, &responseErr) && responseErr.HTTPStatusCode() == http.StatusNotFound
}

func (d *Driver) Restore(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	return nil, drive.NotSupported("restore")
}

func (d *Driver) Move(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	cc, err := connOf(c)
	if err != nil {
		return nil, err
	}
	targetParent := pathOf(toParentID)
	var ok []string
	var failed []error
	for _, ref := range refs {
		base := baseOf(ref.ID)
		to := driveutil.JoinPath(targetParent, base)
		if err := d.copyObj(ctx, cc, cc.keyOf(ref.ID), cc.keyOf(to)); err != nil {
			failed = append(failed, fmt.Errorf("%s: 复制阶段失败: %w", ref.ID, err))
			continue
		}
		if err := cc.deleteRecursive(ctx, cc.keyOf(ref.ID)); err != nil {
			failed = append(failed, fmt.Errorf("%s: 删除源对象失败: %w", ref.ID, err))
			continue
		}
		ok = append(ok, ref.ID)
	}
	return ok, errors.Join(failed...)
}

func (d *Driver) Copy(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	cc, err := connOf(c)
	if err != nil {
		return nil, err
	}
	targetParent := pathOf(toParentID)
	var ok []string
	var failed []error
	for _, ref := range refs {
		base := baseOf(ref.ID)
		to := driveutil.JoinPath(targetParent, base)
		if err := d.copyObj(ctx, cc, cc.keyOf(ref.ID), cc.keyOf(to)); err == nil {
			ok = append(ok, ref.ID)
		} else {
			failed = append(failed, fmt.Errorf("%s: %w", ref.ID, err))
		}
	}
	return ok, errors.Join(failed...)
}

func (d *Driver) copyObj(ctx context.Context, cc *conn, from, to string) error {
	return cc.copyRecursive(ctx, from, to)
}

// UploadOneFile performs a direct upload. Small files use PUT; larger files
// use multipart upload with cleanup on failure. It honors ui.Info.ConflictPolicy
// when the target object already exists.
func (d *Driver) UploadOneFile(ctx context.Context, c drive.Context, ui *model.UploadingUI) error {
	if ui == nil || ui.Info.LocalFilePath == "" {
		return errors.New("s3: 上传文件路径为空")
	}
	if err := validateName(ui.Info.Name); err != nil {
		return err
	}
	cc, err := connOf(c)
	if err != nil {
		return err
	}
	key := cc.keyOf(driveutil.JoinPath(pathOf(ui.Info.ParentFileID), ui.Info.Name))

	policy := driveutil.ResolveConflictPolicy(ui.Info.ConflictPolicy)
	exists, isDir, err := cc.objectState(ctx, key)
	if err != nil {
		return fmt.Errorf("s3: 检查目标文件失败: %w", err)
	}
	if exists {
		switch policy {
		case driveutil.ConflictRefuse:
			return errors.New("s3: 目标文件已存在")
		case driveutil.ConflictSkip:
			return nil
		case driveutil.ConflictRename:
			for index := 1; index <= 9999; index++ {
				newName := s3ConflictName(ui.Info.Name, index)
				candidate := cc.keyOf(driveutil.JoinPath(pathOf(ui.Info.ParentFileID), newName))
				candidateExists, _, candidateErr := cc.objectState(ctx, candidate)
				if candidateErr != nil {
					return fmt.Errorf("s3: 检查重命名目标失败: %w", candidateErr)
				}
				if !candidateExists {
					ui.Info.Name = newName
					key = candidate
					exists = false
					break
				}
				if index == 9999 {
					return errors.New("s3: 无法生成不重复的文件名")
				}
			}
		case driveutil.ConflictOverwrite:
			if isDir {
				if err := cc.deleteRecursive(ctx, key); err != nil {
					return fmt.Errorf("s3: 覆盖目录失败: %w", err)
				}
			}
		}
	}

	f, err := os.Open(ui.Info.LocalFilePath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	ui.Info.Size = size
	if size < s3MultipartThreshold {
		pr := driveutil.NewProgressOnlyReader(f, size, func(read int64) {
			updateUploadProgress(ui, read)
		})
		_, err = cc.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:        aws.String(cc.bucket),
			Key:           aws.String(key),
			Body:          pr,
			ContentLength: aws.Int64(size),
		}, func(o *s3.Options) { o.HTTPClient = netx.UploadHTTPClient{Client: o.HTTPClient} })
	} else {
		err = uploadMultipart(ctx, c, cc, key, f, ui)
	}
	if err != nil {
		return fmt.Errorf("s3: upload: %w", err)
	}
	return nil
}

func s3ConflictName(name string, index int) string {
	ext := path.Ext(name)
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), index, ext)
}

func updateUploadProgress(ui *model.UploadingUI, read int64) {
	ui.ReportUploadProgress(read, ui.Info.Size)
}

func uploadMultipart(ctx context.Context, account drive.Context, cc *conn, key string, f *os.File, ui *model.UploadingUI) error {
	partSize := multipartPartSize(ui.Info.Size)
	capacity := int((ui.Info.Size + partSize - 1) / partSize)
	if capacity < 1 {
		capacity = 1
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	sessionName := fmt.Sprintf("%s|%x|%d", key, hasher.Sum(nil), partSize)
	sessionKey := drive.UploadSessionKey(account.UserID, account.DriveID, cc.bucket, sessionName, ui.Info.Size)
	uploadID, _ := drive.LoadUploadSessionState(sessionKey)
	remoteParts := map[int32]types.Part{}
	var err error
	if uploadID != "" {
		remoteParts, err = listS3UploadParts(ctx, cc, key, uploadID)
		if err != nil {
			if !isMissingS3Upload(err) {
				return fmt.Errorf("multipart upload: 查询已上传分片失败: %w", err)
			}
			drive.ClearUploadSession(sessionKey)
			uploadID = ""
		}
	}
	if uploadID == "" {
		created, createErr := cc.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(cc.bucket), Key: aws.String(key)})
		if createErr != nil {
			return createErr
		}
		if created.UploadId == nil || *created.UploadId == "" {
			return errors.New("multipart upload: 服务端未返回 upload id")
		}
		uploadID = *created.UploadId
		if err := drive.SaveUploadSessionState(sessionKey, uploadID, nil); err != nil {
			_, _ = cc.client.AbortMultipartUpload(context.Background(), &s3.AbortMultipartUploadInput{Bucket: aws.String(cc.bucket), Key: aws.String(key), UploadId: aws.String(uploadID)})
			return err
		}
	}
	parts := make([]types.CompletedPart, 0, capacity)
	buf := make([]byte, int(partSize))
	var total int64
	var partNumber int32 = 1
	for {
		n, readErr := io.ReadFull(f, buf)
		if readErr == io.EOF && n == 0 {
			break
		}
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			return readErr
		}
		if n == 0 {
			break
		}
		if remote, ok := remoteParts[partNumber]; ok && remote.Size != nil && *remote.Size == int64(n) && remote.ETag != nil && *remote.ETag != "" {
			parts = append(parts, types.CompletedPart{PartNumber: aws.Int32(partNumber), ETag: remote.ETag})
			total += int64(n)
			updateUploadProgress(ui, total)
			partNumber++
			continue
		}
		out, err := cc.client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:     aws.String(cc.bucket),
			Key:        aws.String(key),
			UploadId:   aws.String(uploadID),
			PartNumber: aws.Int32(partNumber),
			// UploadPart consumes the reader before it returns, so the reusable
			// buffer can be passed directly without a second 16 MiB allocation.
			Body:          bytes.NewReader(buf[:n]),
			ContentLength: aws.Int64(int64(n)),
		}, func(o *s3.Options) { o.HTTPClient = netx.UploadHTTPClient{Client: o.HTTPClient} })
		if err != nil {
			return err
		}
		if out.ETag == nil || *out.ETag == "" {
			return fmt.Errorf("multipart upload: part %d 未返回 etag", partNumber)
		}
		parts = append(parts, types.CompletedPart{PartNumber: aws.Int32(partNumber), ETag: out.ETag})
		total += int64(n)
		updateUploadProgress(ui, total)
		partNumber++
		if readErr == io.ErrUnexpectedEOF || readErr == io.EOF {
			break
		}
	}
	_, err = cc.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(cc.bucket),
		Key:             aws.String(key),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		return err
	}
	drive.ClearUploadSession(sessionKey)
	return nil
}

func listS3UploadParts(ctx context.Context, cc *conn, key, uploadID string) (map[int32]types.Part, error) {
	parts := map[int32]types.Part{}
	var marker *string
	seenMarkers := map[string]bool{}
	for {
		out, err := cc.client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(cc.bucket), Key: aws.String(key), UploadId: aws.String(uploadID), PartNumberMarker: marker})
		if err != nil {
			return nil, err
		}
		for _, part := range out.Parts {
			if part.PartNumber != nil && *part.PartNumber > 0 {
				parts[*part.PartNumber] = part
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			return parts, nil
		}
		next := aws.ToString(out.NextPartNumberMarker)
		if next == "" || seenMarkers[next] {
			return nil, errors.New("multipart upload: 分片列表游标无效")
		}
		seenMarkers[next] = true
		marker = aws.String(next)
	}
}

func isMissingS3Upload(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && strings.EqualFold(apiErr.ErrorCode(), "NoSuchUpload") || isNotFoundError(err)
}

func multipartPartSize(size int64) int64 {
	partSize := int64(s3MultipartPartSize)
	const maxParts = int64(10000)
	if size > partSize*maxParts {
		partSize = (size + maxParts - 1) / maxParts
		const minPart = int64(5 * 1024 * 1024)
		partSize = ((partSize + minPart - 1) / minPart) * minPart
	}
	return partSize
}

func baseOf(key string) string {
	key = strings.TrimSuffix(key, "/")
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}

func parentOf(p string) string {
	p = strings.TrimSuffix(p, "/")
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "/"
}

func idsToRefs(ids []string) []drive.FileRef {
	refs := make([]drive.FileRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, drive.FileRef{ID: id})
	}
	return refs
}

var _ = awshttp.NewBuildableClient // keep transport import if needed later
