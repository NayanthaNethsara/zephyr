package meta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

const (
	mediaTimeout       = 5 * time.Minute
	mediaHeaderTimeout = 30 * time.Second
	maxFilenameLength  = 255
	defaultFilename    = "upload"
)

type mediaInfo struct {
	URL      string `json:"url"`
	MimeType string `json:"mime_type"`
	SHA256   string `json:"sha256"`
	FileSize int64  `json:"file_size"`
}

type uploadResponse struct {
	ID string `json:"id"`
}

type deleteResponse struct {
	Success bool `json:"success"`
}

type sourceTracker struct {
	reader io.Reader
	mu     sync.Mutex
	err    error
}

func (s *sourceTracker) Read(p []byte) (int, error) {
	n, err := s.reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
	}
	return n, err
}

func (s *sourceTracker) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

func newMediaClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = mediaHeaderTimeout
	return &http.Client{Transport: transport}
}

func (c *Client) UploadMedia(ctx context.Context, phoneNumberID string, upload core.MediaUpload) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, mediaTimeout)
	defer cancel()

	source := &sourceTracker{reader: upload.Content}
	upload.Content = source
	form, contentType := streamUploadForm(upload)
	defer func() { _ = form.Close() }()

	endpoint := fmt.Sprintf("%s/%s/%s/media", c.baseURL, c.apiVersion, url.PathEscape(phoneNumberID))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, form)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.accessToken)
	request.Header.Set("Content-Type", contentType)

	response, err := c.mediaClient.Do(request)
	if err != nil {
		if failure := source.failure(); failure != nil {
			return "", fmt.Errorf("read upload: %w", failure)
		}
		return "", transportError(err)
	}
	defer func() { _ = response.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return "", unreadableResponse()
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return "", apiError(response.StatusCode, payload)
	}

	var parsed uploadResponse
	if err := json.Unmarshal(payload, &parsed); err != nil || parsed.ID == "" {
		return "", unexpectedResponse()
	}
	return parsed.ID, nil
}

func streamUploadForm(upload core.MediaUpload) (*io.PipeReader, string) {
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() { writer.CloseWithError(writeUploadForm(form, upload)) }()
	return reader, form.FormDataContentType()
}

func writeUploadForm(form *multipart.Writer, upload core.MediaUpload) error {
	if err := form.WriteField("messaging_product", "whatsapp"); err != nil {
		return err
	}
	if err := form.WriteField("type", upload.MimeType); err != nil {
		return err
	}

	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, safeFilename(upload.Filename)))
	header.Set("Content-Type", upload.MimeType)
	file, err := form.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, upload.Content); err != nil {
		return err
	}
	return form.Close()
}

func safeFilename(filename string) string {
	name := path.Base(strings.ReplaceAll(filename, `\`, "/"))
	name = strings.Map(func(character rune) rune {
		if character < ' ' || character == 0x7f || character == '"' {
			return -1
		}
		return character
	}, name)
	if name == "." || name == "/" || name == "" {
		return defaultFilename
	}
	if len(name) > maxFilenameLength {
		return name[:maxFilenameLength]
	}
	return name
}

func (c *Client) DownloadMedia(ctx context.Context, mediaID string) (core.MediaDownload, error) {
	ctx, cancel := context.WithTimeout(ctx, mediaTimeout)

	info, err := c.fetchMediaInfo(ctx, mediaID)
	if err != nil {
		cancel()
		return core.MediaDownload{}, err
	}
	download, err := c.fetchMediaContent(ctx, info)
	if err != nil {
		cancel()
		return core.MediaDownload{}, err
	}
	download.Content = &cancelOnClose{ReadCloser: download.Content, cancel: cancel}
	return download, nil
}

func (c *Client) fetchMediaInfo(ctx context.Context, mediaID string) (mediaInfo, error) {
	endpoint := fmt.Sprintf("%s/%s/%s", c.baseURL, c.apiVersion, url.PathEscape(mediaID))
	response, err := c.getWithToken(ctx, endpoint)
	if err != nil {
		return mediaInfo{}, transportError(err)
	}
	defer func() { _ = response.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return mediaInfo{}, unreadableResponse()
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return mediaInfo{}, mediaLookupError(response.StatusCode, payload)
	}

	var info mediaInfo
	if err := json.Unmarshal(payload, &info); err != nil || !isSecureURL(info.URL) {
		return mediaInfo{}, &core.RequestError{
			Code:        core.CodeUpstreamUnavailable,
			Message:     "Meta returned an unusable media location",
			IsRetryable: true,
		}
	}
	return info, nil
}

func (c *Client) fetchMediaContent(ctx context.Context, info mediaInfo) (core.MediaDownload, error) {
	response, err := c.getWithToken(ctx, info.URL)
	if err != nil {
		return core.MediaDownload{}, transportError(err)
	}
	if response.StatusCode != http.StatusOK {
		defer func() { _ = response.Body.Close() }()
		return core.MediaDownload{}, mediaContentError(response)
	}

	return core.MediaDownload{
		Content:  response.Body,
		MimeType: firstNonEmpty(info.MimeType, response.Header.Get("Content-Type")),
		SHA256:   info.SHA256,
		Size:     contentSize(response.ContentLength, info.FileSize),
	}, nil
}

func (c *Client) DeleteMedia(ctx context.Context, mediaID string) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	endpoint := fmt.Sprintf("%s/%s/%s", c.baseURL, c.apiVersion, url.PathEscape(mediaID))
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.accessToken)

	response, err := c.mediaClient.Do(request)
	if err != nil {
		return transportError(err)
	}
	defer func() { _ = response.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return unreadableResponse()
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return mediaLookupError(response.StatusCode, payload)
	}

	var parsed deleteResponse
	if err := json.Unmarshal(payload, &parsed); err != nil || !parsed.Success {
		return unexpectedResponse()
	}
	return nil
}

func (c *Client) getWithToken(ctx context.Context, target string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.accessToken)
	return c.mediaClient.Do(request)
}

func mediaLookupError(httpStatus int, payload []byte) *core.RequestError {
	requestErr := apiError(httpStatus, payload)
	if httpStatus == http.StatusNotFound || requestErr.MetaCode == metaCodeInvalidParameter {
		requestErr.Code = core.CodeMediaNotFound
		requestErr.IsRetryable = false
	}
	return requestErr
}

func mediaContentError(response *http.Response) *core.RequestError {
	if response.StatusCode == http.StatusNotFound {
		return &core.RequestError{Code: core.CodeMediaNotFound, Message: "the media file is no longer available"}
	}
	classified := classifyByStatus(response.StatusCode)
	return &core.RequestError{
		Code:        classified.code,
		Message:     fmt.Sprintf("Meta responded with status %d while downloading the media", response.StatusCode),
		IsRetryable: classified.isRetryable,
	}
}

func isSecureURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

func contentSize(contentLength, declaredSize int64) int64 {
	if contentLength >= 0 {
		return contentLength
	}
	if declaredSize > 0 {
		return declaredSize
	}
	return -1
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func unreadableResponse() *core.RequestError {
	return &core.RequestError{Code: core.CodeUpstreamUnavailable, Message: "could not read the response from Meta", IsRetryable: true}
}

func unexpectedResponse() *core.RequestError {
	return &core.RequestError{Code: core.CodeUpstreamUnavailable, Message: "Meta returned an unexpected response", IsRetryable: true}
}
