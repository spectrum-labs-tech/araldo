// SPDX-License-Identifier: AGPL-3.0-or-later

package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Streaming large files, such as video (ADR 0027), without holding more
// than one part in memory.

// PartSize is every part of a multipart upload but the last; S3's minimum
// is 5 MiB.
const PartSize = 8 << 20

// ErrTooLarge is returned by PutStream for a stream over its limit.
var ErrTooLarge = errors.New("blob: the file is larger than allowed")

// PutStream stores what r yields at key: in one request when it is under
// a part's size, or as a multipart upload, aborted on any failure. It refuses
// more than maxBytes, and returns the size and SHA-256 of what it stored.
func (s *S3) PutStream(ctx context.Context, key string, r io.Reader, contentType string, maxBytes int64) (int64, []byte, error) {
	h := sha256.New()
	size := s.PartSize
	if size <= 0 {
		size = PartSize
	}
	buf := make([]byte, size)
	n, err := io.ReadFull(r, buf)
	switch {
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		if int64(n) > maxBytes {
			return 0, nil, ErrTooLarge
		}
		h.Write(buf[:n])
		return int64(n), h.Sum(nil), s.Put(ctx, key, buf[:n], contentType)
	case err != nil:
		return 0, nil, err
	}

	upload, err := s.createMultipart(ctx, key, contentType)
	if err != nil {
		return 0, nil, err
	}
	abort := func(cause error) (int64, []byte, error) {
		// A fresh context: the request's may be why we are aborting.
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), PartTimeout)
		defer cancel()
		if resp, err := s.do(actx, http.MethodDelete, key, url.Values{"uploadId": {upload}}, nil, "", nil); err == nil {
			drain(resp)
		}
		return 0, nil, cause
	}
	var (
		total int64
		etags []string
	)
	for last := false; ; {
		total += int64(n)
		if total > maxBytes {
			return abort(ErrTooLarge)
		}
		h.Write(buf[:n])
		etag, err := s.uploadPart(ctx, key, upload, len(etags)+1, buf[:n])
		if err != nil {
			return abort(err)
		}
		etags = append(etags, etag)
		if last {
			break
		}
		n, err = io.ReadFull(r, buf)
		switch {
		case errors.Is(err, io.EOF):
			last, n = true, 0
		case errors.Is(err, io.ErrUnexpectedEOF):
			last = true
		case err != nil:
			return abort(err)
		}
		if n == 0 {
			break
		}
	}
	if err := s.completeMultipart(ctx, key, upload, etags); err != nil {
		return abort(err)
	}
	return total, h.Sum(nil), nil
}

// PartTimeout bounds the abort of a failed upload.
const PartTimeout = 30 * time.Second

func (s *S3) createMultipart(ctx context.Context, key, contentType string) (string, error) {
	resp, err := s.do(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, nil, contentType, nil)
	if err != nil {
		return "", err
	}
	defer drain(resp)
	if resp.StatusCode/100 != 2 {
		return "", s.failure(resp, "start upload", key)
	}
	var out struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil || out.UploadID == "" {
		return "", fmt.Errorf("blob: start upload %s: no upload ID", key)
	}
	return out.UploadID, nil
}

func (s *S3) uploadPart(ctx context.Context, key, upload string, part int, data []byte) (string, error) {
	resp, err := s.do(ctx, http.MethodPut, key, url.Values{"partNumber": {strconv.Itoa(part)}, "uploadId": {upload}}, data, "", nil)
	if err != nil {
		return "", err
	}
	defer drain(resp)
	if resp.StatusCode/100 != 2 {
		return "", s.failure(resp, "upload part "+strconv.Itoa(part)+" of", key)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		return "", fmt.Errorf("blob: upload part %d of %s: no ETag", part, key)
	}
	return etag, nil
}

type completePart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

func (s *S3) completeMultipart(ctx context.Context, key, upload string, etags []string) error {
	body := struct {
		XMLName xml.Name       `xml:"CompleteMultipartUpload"`
		Parts   []completePart `xml:"Part"`
	}{}
	for i, e := range etags {
		body.Parts = append(body.Parts, completePart{PartNumber: i + 1, ETag: e})
	}
	data, err := xml.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodPost, key, url.Values{"uploadId": {upload}}, data, "application/xml", nil)
	if err != nil {
		return err
	}
	defer drain(resp)
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	// S3 can answer 200 and still fail: the error is in the body.
	if resp.StatusCode/100 != 2 || bytes.Contains(raw, []byte("<Error>")) {
		return fmt.Errorf("blob: complete upload %s: HTTP %d: %s", key, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// GetRange returns n bytes of the object at key from off, or fewer at its
// end.
func (s *S3) GetRange(ctx context.Context, key string, off, n int64) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	resp, err := s.do(ctx, http.MethodGet, key, nil, nil, "", map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", off, off+n-1)})
	if err != nil {
		return nil, err
	}
	defer drain(resp)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		return nil, io.EOF
	case resp.StatusCode/100 != 2:
		return nil, s.failure(resp, "get range of", key)
	}
	return io.ReadAll(io.LimitReader(resp.Body, n))
}
