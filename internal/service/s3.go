package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// objectStore keeps the web tools' downloads (web_tools.go). The server uses
// an S3-compatible bucket (Backblaze B2); tests use memory.
type objectStore interface {
	put(ctx context.Context, key, contentType string, body []byte) error
	// get returns an object; a missing one is an error.
	get(ctx context.Context, key string) ([]byte, error)
}

// s3Store speaks just enough of the S3 API (PUT and GET of one object, path
// style, AWS Signature Version 4) for the downloads bucket.
type s3Store struct {
	endpoint, region, bucket, keyID, secret string
	http                                    *http.Client
	// now is the signing clock; tests fix it.
	now func() time.Time
}

func s3HTTPClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (s *s3Store) objectURL(key string) string {
	return s.endpoint + "/" + s.bucket + "/" + s3Escape(key)
}

func (s *s3Store) put(ctx context.Context, key, contentType string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.objectURL(key), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	s.sign(req, body)
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("downloads bucket unreachable")
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloads bucket refused the upload (%d)", resp.StatusCode)
	}
	return nil
}

func (s *s3Store) get(ctx context.Context, key string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL(key), nil)
	if err != nil {
		return nil, err
	}
	s.sign(req, nil)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloads bucket unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("downloads bucket answered %d", resp.StatusCode)
	}
	limit := max(maxWebBytes, maxVirtualBytes)
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil || len(b) > limit {
		return nil, fmt.Errorf("downloads bucket object could not be read")
	}
	return b, nil
}

// sign adds the Signature Version 4 headers, signing host, the payload hash,
// the date and any Content-Type or Range.
func (s *s3Store) sign(req *http.Request, body []byte) {
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	t := now().UTC()
	date, stamp := t.Format("20060102"), t.Format("20060102T150405Z")
	sum := sha256.Sum256(body)
	payload := hex.EncodeToString(sum[:])
	req.Header.Set("x-amz-date", stamp)
	req.Header.Set("x-amz-content-sha256", payload)
	headers := map[string]string{"host": req.URL.Host}
	for _, name := range []string{"Content-Type", "Range", "x-amz-content-sha256", "x-amz-date"} {
		if v := req.Header.Get(name); v != "" {
			headers[strings.ToLower(name)] = strings.TrimSpace(v)
		}
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonical strings.Builder
	for _, name := range names {
		canonical.WriteString(name + ":" + headers[name] + "\n")
	}
	signed := strings.Join(names, ";")
	request := strings.Join([]string{req.Method, req.URL.EscapedPath(), s3Query(req.URL.Query()), canonical.String(), signed, payload}, "\n")
	scope := date + "/" + s.region + "/s3/aws4_request"
	hashed := sha256.Sum256([]byte(request))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(hashed[:])
	key := hmacSHA256([]byte("AWS4"+s.secret), date)
	key = hmacSHA256(key, s.region)
	key = hmacSHA256(key, "s3")
	key = hmacSHA256(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.keyID+"/"+scope+", SignedHeaders="+signed+", Signature="+signature)
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// s3Escape escapes an object key for the path, keeping its slashes.
func s3Escape(key string) string {
	parts := strings.Split(key, "/")
	for i, part := range parts {
		parts[i] = strings.ReplaceAll(url.PathEscape(part), "+", "%2B")
	}
	return strings.Join(parts, "/")
}

func s3Query(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.ReplaceAll(strings.Join(parts, "&"), "+", "%20")
}
