package mogenius

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
)

const userAgent = "mogenius-sandbox-go"

// apiClient is the HTTP layer under the SDK: one place for the platform's
// headers, JSON handling and the error shape. Routes are the platform's
// /sandbox/... and /resource/session/... endpoints.
type apiClient struct {
	cfg *resolvedConfig
}

func (a *apiClient) get(ctx context.Context, path string, query url.Values, out any) error {
	return a.do(ctx, http.MethodGet, path, query, nil, out)
}

func (a *apiClient) post(ctx context.Context, path string, query url.Values, body, out any) error {
	return a.do(ctx, http.MethodPost, path, query, body, out)
}

func (a *apiClient) patch(ctx context.Context, path string, body, out any) error {
	return a.do(ctx, http.MethodPatch, path, nil, body, out)
}

func (a *apiClient) delete(ctx context.Context, path string, query url.Values, out any) error {
	return a.do(ctx, http.MethodDelete, path, query, nil, out)
}

// do sends a request and decodes the JSON answer into out; a *[]byte out
// takes the body as it is. body is sent as JSON unless it is a *formBody.
func (a *apiClient) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var reader io.Reader
	var contentType string
	length := int64(-1)
	switch b := body.(type) {
	case nil:
	case *formBody:
		reader, contentType, length = b.reader, b.contentType, b.length
	default:
		data, err := json.Marshal(b)
		if err != nil {
			return sdkerrors.NewMogeniusError(fmt.Sprintf("%s %s: the request body is not JSON: %v", method, path, err), 0, nil)
		}
		reader, contentType, length = bytes.NewReader(data), "application/json", int64(len(data))
	}

	target := a.cfg.apiURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return sdkerrors.NewMogeniusError(fmt.Sprintf("%s %s: %v", method, path, err), 0, nil)
	}
	if reader != nil {
		req.ContentLength = length
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if a.cfg.organizationID != "" {
		req.Header.Set("organization-id", a.cfg.organizationID)
	}
	if a.cfg.clusterID != "" {
		req.Header.Set("cluster-id", a.cfg.clusterID)
	}
	if a.cfg.workspaceName != "" {
		req.Header.Set("workspace-name", a.cfg.workspaceName)
	}

	resp, err := a.cfg.httpClient.Do(req)
	if err != nil {
		return transportError(ctx, method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return transportError(ctx, method, path, err)
	}
	if resp.StatusCode >= 400 {
		return sdkerrors.NewMogeniusErrorFromResponse(data, resp.StatusCode, resp.Header,
			fmt.Sprintf("%s %s → HTTP %d", method, path, resp.StatusCode))
	}
	if raw, ok := out.(*[]byte); ok {
		*raw = data
		return nil
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		base := sdkerrors.NewMogeniusError(fmt.Sprintf("%s %s answered with unexpected JSON: %v", method, path, err), resp.StatusCode, resp.Header)
		base.Source = sdkerrors.SourceAPI
		return base
	}
	return nil
}

// byteRange asks a download link to continue at offset, but only while the
// file is still the version ifRange (its ETag) names.
type byteRange struct {
	offset  int64
	ifRange string
}

// fetchDownload GETs a URL the platform handed out (a download link). No auth
// headers: the token in the URL is the credential, the way a browser opens
// the same link. The caller closes the body.
func (a *apiClient) fetchDownload(ctx context.Context, link string, resume *byteRange) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, sdkerrors.NewMogeniusError(fmt.Sprintf("GET download link: %v", err), 0, nil)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", userAgent)
	// byte offsets for a resume count the file as stored, not a compressed transfer
	req.Header.Set("Accept-Encoding", "identity")
	if resume != nil {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resume.offset))
		if resume.ifRange != "" {
			req.Header.Set("If-Range", resume.ifRange)
		}
	}
	resp, err := a.cfg.httpClient.Do(req)
	if err != nil {
		return nil, transportError(ctx, http.MethodGet, "download link", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return nil, sdkerrors.NewMogeniusErrorFromResponse(data, resp.StatusCode, resp.Header,
			fmt.Sprintf("GET download link → HTTP %d", resp.StatusCode))
	}
	return resp, nil
}

// transportError is what a request that got no answer stands for. A done
// context is handed back as is, so errors.Is(err, context.Canceled) holds.
func transportError(ctx context.Context, method, path string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s %s: %w", method, path, ctx.Err())
	}
	return sdkerrors.NewMogeniusError(fmt.Sprintf("%s %s failed: %v", method, path, err), 0, nil)
}

// formBody is a multipart body with a single file part, streamed: the part's
// content is read only when the request is sent.
type formBody struct {
	reader      io.Reader
	contentType string
	// length is -1 when the content's size is unknown; the upload is then chunked.
	length int64
}

func newFormBody(field, filename string, content io.Reader, size int64) (*formBody, error) {
	var head bytes.Buffer
	writer := multipart.NewWriter(&head)
	if _, err := writer.CreateFormFile(field, filename); err != nil {
		return nil, err
	}
	header := append([]byte(nil), head.Bytes()...)
	head.Reset()
	// closing the writer only writes the final boundary
	if err := writer.Close(); err != nil {
		return nil, err
	}
	footer := head.Bytes()
	length := int64(-1)
	if size >= 0 {
		length = int64(len(header)) + size + int64(len(footer))
	}
	return &formBody{
		reader:      io.MultiReader(bytes.NewReader(header), content, bytes.NewReader(footer)),
		contentType: writer.FormDataContentType(),
		length:      length,
	}, nil
}

// query builds a query string from key/value pairs, leaving out empty values.
func query(pairs ...string) url.Values {
	values := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			values.Set(pairs[i], pairs[i+1])
		}
	}
	return values
}

// segment escapes one path segment of a route.
func segment(value string) string {
	return url.PathEscape(value)
}
