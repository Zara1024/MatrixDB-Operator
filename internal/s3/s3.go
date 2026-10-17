package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Client handles S3 compatible storage operations (AWS S3, MinIO, Ceph, etc.)
type Client struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	client    *http.Client
}

// NewClient creates a new S3 client
func NewClient(endpoint, bucket, accessKey, secretKey, region string) *Client {
	if endpoint == "" {
		endpoint = "http://minio:9000"
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}
	if region == "" {
		region = "us-east-1"
	}
	return &Client{
		Endpoint:  strings.TrimRight(endpoint, "/"),
		Bucket:    bucket,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Region:    region,
		client:    &http.Client{Timeout: 30 * time.Minute},
	}
}

// EnsureBucket checks if bucket exists, creating it if not
func (c *Client) EnsureBucket(ctx context.Context) error {
	u := fmt.Sprintf("%s/%s", c.Endpoint, c.Bucket)
	req, err := http.NewRequestWithContext(ctx, "PUT", u, nil)
	if err != nil {
		return err
	}
	c.signRequest(req, []byte{})
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 200 OK or 409 Conflict (BucketAlreadyOwnedByYou) or 204
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusConflict {
		return nil
	}
	return nil
}

// UploadFile uploads a local file to S3
func (c *Client) UploadFile(ctx context.Context, key string, filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed reading file to upload: %w", err)
	}

	u := fmt.Sprintf("%s/%s/%s", c.Endpoint, c.Bucket, strings.TrimLeft(key, "/"))
	req, err := http.NewRequestWithContext(ctx, "PUT", u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	c.signRequest(req, data)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed sending S3 PUT request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("S3 upload failed with status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// DownloadFile downloads an object from S3 to a local file
func (c *Client) DownloadFile(ctx context.Context, key string, targetPath string) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return err
	}

	u := fmt.Sprintf("%s/%s/%s", c.Endpoint, c.Bucket, strings.TrimLeft(key, "/"))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	c.signRequest(req, []byte{})

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed sending S3 GET request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("S3 download failed with status %d: %s", resp.StatusCode, string(body))
	}

	out, err := os.Create(targetPath)
	if err != nil {
		return fmt.Errorf("failed creating target file %s: %w", targetPath, err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// DeleteObject deletes an object from S3
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	u := fmt.Sprintf("%s/%s/%s", c.Endpoint, c.Bucket, strings.TrimLeft(key, "/"))
	req, err := http.NewRequestWithContext(ctx, "DELETE", u, nil)
	if err != nil {
		return err
	}
	c.signRequest(req, []byte{})
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// ListObjects lists object keys in the bucket matching the given prefix
func (c *Client) ListObjects(ctx context.Context, prefix string) ([]string, error) {
	u := fmt.Sprintf("%s/%s?prefix=%s", c.Endpoint, c.Bucket, prefix)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	c.signRequest(req, []byte{})
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("S3 ListObjects failed: %s", string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	if err := xml.Unmarshal(body, &result); err != nil {
		// Fallback simple parsing if XML namespace issues
		lines := strings.Split(string(body), "<Key>")
		var keys []string
		for _, l := range lines[1:] {
			parts := strings.SplitN(l, "</Key>", 2)
			if len(parts) == 2 {
				keys = append(keys, parts[0])
			}
		}
		sort.Strings(keys)
		return keys, nil
	}

	var keys []string
	for _, item := range result.Contents {
		keys = append(keys, item.Key)
	}
	sort.Strings(keys)
	return keys, nil
}

// signRequest signs the HTTP request using AWS Signature Version 4
func (c *Client) signRequest(req *http.Request, payload []byte) {
	if c.AccessKey == "" || c.SecretKey == "" {
		return // unauthenticated if keys not provided
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("x-amz-date", amzDate)
	h := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(h[:])
	req.Header.Set("x-amz-content-sha256", payloadHash)

	// Host header
	host := req.URL.Host
	req.Header.Set("Host", host)

	// Canonical headers
	headers := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	sort.Strings(headers)
	var canonicalHeaders strings.Builder
	for _, h := range headers {
		canonicalHeaders.WriteString(fmt.Sprintf("%s:%s\n", h, strings.TrimSpace(req.Header.Get(h))))
	}
	signedHeaders := strings.Join(headers, ";")

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := req.URL.Query().Encode()

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	)

	crHash := sha256.Sum256([]byte(canonicalRequest))
	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, c.Region)
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		amzDate,
		credentialScope,
		hex.EncodeToString(crHash[:]),
	)

	signingKey := getSignatureKey(c.SecretKey, dateStamp, c.Region, "s3")
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.AccessKey,
		credentialScope,
		signedHeaders,
		signature,
	)
	req.Header.Set("Authorization", authHeader)
}

func hmacSHA256(key []byte, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func getSignatureKey(key string, dateStamp, regionName, serviceName string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+key), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(regionName))
	kService := hmacSHA256(kRegion, []byte(serviceName))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}
