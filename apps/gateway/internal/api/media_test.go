package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

type cappedBuffer struct {
	max  int
	data []byte
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - len(c.data); room > 0 {
		c.data = append(c.data, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

type uploadedFile struct {
	phoneNumberID string
	mimeType      string
	filename      string
	body          []byte
}

type fakeMediaBackend struct {
	uploads      []uploadedFile
	uploadID     string
	uploadErr    error
	downloadedID []string
	download     func() (core.MediaDownload, error)
	deletedID    []string
	deleteErr    error
}

func (f *fakeMediaBackend) UploadMedia(_ context.Context, phoneNumberID string, upload core.MediaUpload) (string, error) {
	captured := &cappedBuffer{max: 1 << 20}
	if _, err := io.Copy(captured, upload.Content); err != nil {
		return "", fmt.Errorf("read upload: %w", err)
	}
	f.uploads = append(f.uploads, uploadedFile{phoneNumberID, upload.MimeType, upload.Filename, captured.data})
	return f.uploadID, f.uploadErr
}

func (f *fakeMediaBackend) DownloadMedia(_ context.Context, mediaID string) (core.MediaDownload, error) {
	f.downloadedID = append(f.downloadedID, mediaID)
	return f.download()
}

func (f *fakeMediaBackend) DeleteMedia(_ context.Context, mediaID string) error {
	f.deletedID = append(f.deletedID, mediaID)
	return f.deleteErr
}

func newMediaRouter(backend *fakeMediaBackend, recorder Recorder) http.Handler {
	return NewRouter(Deps{
		Ingestor: &fakeIngestor{},
		Recorder: recorder,
		Media:    core.NewMediaService(backend, backend, backend, "111"),
		APIKeys:  []string{testAPIKey},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

type formPart struct {
	field       string
	filename    string
	contentType string
	content     string
}

func buildMultipart(t *testing.T, parts ...formPart) (io.Reader, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, p := range parts {
		header := textproto.MIMEHeader{}
		disposition := fmt.Sprintf(`form-data; name="%s"`, p.field)
		if p.filename != "" {
			disposition += fmt.Sprintf(`; filename="%s"`, p.filename)
		}
		header.Set("Content-Disposition", disposition)
		if p.contentType != "" {
			header.Set("Content-Type", p.contentType)
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte(p.content))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

func doRequest(router http.Handler, method, target, contentType string, body io.Reader) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, body)
	request.Header.Set("Authorization", "Bearer "+testAPIKey)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, request)
	return rec
}

func TestMediaRoutesRequireAPIKey(t *testing.T) {
	routes := []struct {
		name   string
		method string
		target string
	}{
		{name: "upload", method: http.MethodPost, target: "/v1/media"},
		{name: "download", method: http.MethodGet, target: "/v1/media/123"},
		{name: "delete", method: http.MethodDelete, target: "/v1/media/123"},
	}
	credentials := []struct {
		name          string
		authorization string
		wantStatus    int
	}{
		{name: "no header", authorization: "", wantStatus: http.StatusUnauthorized},
		{name: "wrong key", authorization: "Bearer nope", wantStatus: http.StatusUnauthorized},
		{name: "valid key reaches the handler", authorization: "Bearer " + testAPIKey, wantStatus: http.StatusBadRequest},
	}

	for _, route := range routes {
		for _, credential := range credentials {
			t.Run(route.name+" with "+credential.name, func(t *testing.T) {
				stop := &core.RequestError{Code: core.CodeInvalidRequest, Message: "stop"}
				backend := &fakeMediaBackend{
					download:  func() (core.MediaDownload, error) { return core.MediaDownload{}, stop },
					deleteErr: stop,
				}
				request := httptest.NewRequest(route.method, route.target, strings.NewReader("not multipart"))
				if credential.authorization != "" {
					request.Header.Set("Authorization", credential.authorization)
				}
				rec := httptest.NewRecorder()
				newMediaRouter(backend, &fakeRecorder{}).ServeHTTP(rec, request)

				if rec.Code != credential.wantStatus {
					t.Fatalf("status = %d, want %d", rec.Code, credential.wantStatus)
				}
			})
		}
	}
}

func TestMediaRoutesAreAbsentWithoutAProvider(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
	}{
		{name: "upload", method: http.MethodPost, target: "/v1/media"},
		{name: "download", method: http.MethodGet, target: "/v1/media/123"},
		{name: "delete", method: http.MethodDelete, target: "/v1/media/123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doRequest(newTestRouter(&fakeIngestor{}), tt.method, tt.target, "", nil)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
		})
	}
}

func TestUploadMedia(t *testing.T) {
	tests := []struct {
		name         string
		parts        []formPart
		rawBody      string
		rawType      string
		uploadErr    error
		wantStatus   int
		wantCode     string
		wantUploads  int
		wantMime     string
		wantFilename string
		wantBody     string
		wantMedia    string
	}{
		{
			name:       "png file",
			parts:      []formPart{{field: "file", filename: "photo.png", contentType: "image/png", content: "png-bytes"}},
			wantStatus: http.StatusCreated, wantUploads: 1, wantMime: "image/png", wantFilename: "photo.png", wantBody: "png-bytes", wantMedia: "upload:ok:9",
		},
		{
			name: "other form fields before the file are ignored",
			parts: []formPart{
				{field: "note", content: "hello"},
				{field: "file", filename: "doc.pdf", contentType: "application/pdf", content: "pdf"},
			},
			wantStatus: http.StatusCreated, wantUploads: 1, wantMime: "application/pdf", wantFilename: "doc.pdf", wantBody: "pdf", wantMedia: "upload:ok:3",
		},
		{
			name:       "empty file is passed to Meta to judge",
			parts:      []formPart{{field: "file", filename: "empty.png", contentType: "image/png", content: ""}},
			wantStatus: http.StatusCreated, wantUploads: 1, wantMime: "image/png", wantFilename: "empty.png", wantMedia: "upload:ok:0",
		},
		{
			name:       "file part without a content type",
			parts:      []formPart{{field: "file", filename: "mystery", content: "bytes"}},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request", wantMedia: "upload:invalid_request:0",
		},
		{
			name:       "only unrelated form fields",
			parts:      []formPart{{field: "note", content: "hello"}},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request", wantMedia: "upload:invalid_request:0",
		},
		{
			name:       "file under a different field name",
			parts:      []formPart{{field: "upload", filename: "a.png", contentType: "image/png", content: "x"}},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request", wantMedia: "upload:invalid_request:0",
		},
		{
			name:       "json body instead of multipart",
			rawBody:    `{"file":"x"}`,
			rawType:    "application/json",
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request", wantMedia: "upload:invalid_request:0",
		},
		{
			name:       "no content type at all",
			rawBody:    "bytes",
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request", wantMedia: "upload:invalid_request:0",
		},
		{
			name:       "meta rejects the file",
			parts:      []formPart{{field: "file", filename: "a.bin", contentType: "application/x-bin", content: "x"}},
			uploadErr:  &core.RequestError{Code: core.CodeInvalidMedia, Message: "unsupported", MetaCode: 131053},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_media", wantUploads: 1, wantMedia: "upload:invalid_media:1",
		},
		{
			name:       "meta throttles",
			parts:      []formPart{{field: "file", filename: "a.png", contentType: "image/png", content: "x"}},
			uploadErr:  &core.RequestError{Code: core.CodeRateLimited, Message: "slow", IsRetryable: true},
			wantStatus: http.StatusTooManyRequests, wantCode: "rate_limited", wantUploads: 1, wantMedia: "upload:rate_limited:1",
		},
		{
			name:       "token rejected",
			parts:      []formPart{{field: "file", filename: "a.png", contentType: "image/png", content: "x"}},
			uploadErr:  &core.RequestError{Code: core.CodeUpstreamAuthFailed, Message: "token", MetaCode: 190},
			wantStatus: http.StatusBadGateway, wantCode: "upstream_auth_failed", wantUploads: 1, wantMedia: "upload:upstream_auth_failed:1",
		},
		{
			name:       "meta times out",
			parts:      []formPart{{field: "file", filename: "a.png", contentType: "image/png", content: "x"}},
			uploadErr:  &core.RequestError{Code: core.CodeUpstreamTimeout, Message: "slow", IsRetryable: true},
			wantStatus: http.StatusGatewayTimeout, wantCode: "upstream_timeout", wantUploads: 1, wantMedia: "upload:upstream_timeout:1",
		},
		{
			name:       "unexpected failure is not leaked",
			parts:      []formPart{{field: "file", filename: "a.png", contentType: "image/png", content: "x"}},
			uploadErr:  errors.New("secret internal detail"),
			wantStatus: http.StatusInternalServerError, wantCode: "internal_error", wantUploads: 1, wantMedia: "upload:internal_error:1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeMediaBackend{uploadID: "5551234", uploadErr: tt.uploadErr}
			recorder := &fakeRecorder{}

			var body io.Reader
			var contentType string
			if tt.parts != nil {
				body, contentType = buildMultipart(t, tt.parts...)
			} else {
				body, contentType = strings.NewReader(tt.rawBody), tt.rawType
			}

			rec := doRequest(newMediaRouter(backend, recorder), http.MethodPost, "/v1/media", contentType, body)

			if rec.Code != tt.wantStatus || len(backend.uploads) != tt.wantUploads {
				t.Fatalf("status = %d, uploads = %d, want %d and %d (body %s)", rec.Code, len(backend.uploads), tt.wantStatus, tt.wantUploads, rec.Body.String())
			}
			decoded := decodeBody(t, rec)
			if tt.wantCode != "" && decoded["code"] != tt.wantCode {
				t.Errorf("body = %v, want code %s", decoded, tt.wantCode)
			}
			if strings.Contains(rec.Body.String(), "secret internal detail") {
				t.Errorf("internal error leaked: %s", rec.Body.String())
			}
			if tt.wantStatus == http.StatusCreated {
				if decoded["id"] != "5551234" || decoded["mime_type"] != tt.wantMime {
					t.Errorf("response = %v", decoded)
				}
				got := backend.uploads[0]
				if got.phoneNumberID != "111" || got.mimeType != tt.wantMime || got.filename != tt.wantFilename || string(got.body) != tt.wantBody {
					t.Errorf("backend received %+v", got)
				}
			}
			if len(recorder.media) != 1 || recorder.media[0] != tt.wantMedia {
				t.Errorf("recorded media = %v, want [%s]", recorder.media, tt.wantMedia)
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestUploadMediaSizeAndTruncation(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		fileBytes   int64
		truncateAt  int64
		wantStatus  int
		wantCode    string
		wantMedia   string
		wantUploads int
	}{
		{name: "document at the limit is accepted", contentType: "application/pdf", fileBytes: maxMediaBytes, wantStatus: http.StatusCreated, wantMedia: fmt.Sprintf("upload:ok:%d", maxMediaBytes), wantUploads: 1},
		{name: "document over the request limit is refused", contentType: "application/pdf", fileBytes: maxMediaBytes + multipartOverhead + 1, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "payload_too_large", wantUploads: 0},
		{name: "document just over the document limit is refused", contentType: "application/pdf", fileBytes: maxMediaBytes + 1, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "payload_too_large", wantMedia: fmt.Sprintf("upload:payload_too_large:%d", maxMediaBytes+1), wantUploads: 0},
		{name: "video at its limit is accepted", contentType: "video/mp4", fileBytes: 16 << 20, wantStatus: http.StatusCreated, wantMedia: fmt.Sprintf("upload:ok:%d", 16<<20), wantUploads: 1},
		{name: "video over its limit is refused", contentType: "video/mp4", fileBytes: 16<<20 + 1, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "payload_too_large", wantMedia: fmt.Sprintf("upload:payload_too_large:%d", 16<<20+1), wantUploads: 0},
		{name: "image over its limit is refused", contentType: "image/png", fileBytes: 5<<20 + 1, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "payload_too_large", wantMedia: fmt.Sprintf("upload:payload_too_large:%d", 5<<20+1), wantUploads: 0},
		{name: "sticker over its limit is refused", contentType: "image/webp", fileBytes: 500<<10 + 1, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "payload_too_large", wantMedia: fmt.Sprintf("upload:payload_too_large:%d", 500<<10+1), wantUploads: 0},
		{name: "upload cut off midway is a bad request", contentType: "image/png", fileBytes: 5000, truncateAt: 2000, wantStatus: http.StatusBadRequest, wantCode: "invalid_request", wantUploads: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeMediaBackend{uploadID: "1"}
			recorder := &fakeRecorder{}

			multipartHeader := textproto.MIMEHeader{}
			multipartHeader.Set("Content-Disposition", `form-data; name="file"; filename="big.bin"`)
			multipartHeader.Set("Content-Type", tt.contentType)

			reader, writer := io.Pipe()
			form := multipart.NewWriter(writer)
			go func() {
				part, err := form.CreatePart(multipartHeader)
				if err != nil {
					writer.CloseWithError(err)
					return
				}
				if tt.truncateAt > 0 {
					_, _ = io.CopyN(part, zeroReader{}, tt.truncateAt)
					writer.Close()
					return
				}
				_, err = io.CopyN(part, zeroReader{}, tt.fileBytes)
				if err == nil {
					err = form.Close()
				}
				writer.CloseWithError(err)
			}()
			defer func() { _ = reader.Close() }()

			rec := doRequest(newMediaRouter(backend, recorder), http.MethodPost, "/v1/media", form.FormDataContentType(), reader)

			if rec.Code != tt.wantStatus || len(backend.uploads) != tt.wantUploads {
				t.Fatalf("status = %d, uploads = %d, want %d and %d (body %s)", rec.Code, len(backend.uploads), tt.wantStatus, tt.wantUploads, rec.Body.String())
			}
			if tt.wantCode != "" {
				if decoded := decodeBody(t, rec); decoded["code"] != tt.wantCode {
					t.Errorf("body = %v, want code %s", decoded, tt.wantCode)
				}
			}
			if tt.wantMedia != "" && (len(recorder.media) != 1 || recorder.media[0] != tt.wantMedia) {
				t.Errorf("recorded media = %v, want [%s]", recorder.media, tt.wantMedia)
			}
		})
	}
}

type interruptedContent struct {
	data []byte
	err  error
}

func (i *interruptedContent) Read(p []byte) (int, error) {
	if len(i.data) == 0 {
		return 0, i.err
	}
	n := copy(p, i.data)
	i.data = i.data[n:]
	return n, nil
}

func (i *interruptedContent) Close() error { return nil }

func TestDownloadMedia(t *testing.T) {
	notFound := &core.RequestError{Code: core.CodeMediaNotFound, Message: "no longer available"}

	tests := []struct {
		name         string
		mediaID      string
		download     func() (core.MediaDownload, error)
		wantStatus   int
		wantCode     string
		wantBody     string
		wantType     string
		wantLength   string
		wantSHA      string
		wantMedia    string
		wantDownload int
	}{
		{
			name:    "complete metadata",
			mediaID: "1234567890",
			download: func() (core.MediaDownload, error) {
				return core.MediaDownload{Content: io.NopCloser(strings.NewReader("jpeg-bytes")), MimeType: "image/jpeg", SHA256: "abc123", Size: 10}, nil
			},
			wantStatus: http.StatusOK, wantBody: "jpeg-bytes", wantType: "image/jpeg", wantLength: "10", wantSHA: "abc123", wantMedia: "download:ok:10", wantDownload: 1,
		},
		{
			name:    "missing mime type falls back to octet-stream",
			mediaID: "77",
			download: func() (core.MediaDownload, error) {
				return core.MediaDownload{Content: io.NopCloser(strings.NewReader("abc")), Size: 3}, nil
			},
			wantStatus: http.StatusOK, wantBody: "abc", wantType: "application/octet-stream", wantLength: "3", wantMedia: "download:ok:3", wantDownload: 1,
		},
		{
			name:    "unknown size sends no length",
			mediaID: "88",
			download: func() (core.MediaDownload, error) {
				return core.MediaDownload{Content: io.NopCloser(strings.NewReader("abcd")), MimeType: "audio/ogg", Size: -1}, nil
			},
			wantStatus: http.StatusOK, wantBody: "abcd", wantType: "audio/ogg", wantMedia: "download:ok:4", wantDownload: 1,
		},
		{
			name:       "media no longer available",
			mediaID:    "99",
			download:   func() (core.MediaDownload, error) { return core.MediaDownload{}, notFound },
			wantStatus: http.StatusNotFound, wantCode: "media_not_found", wantMedia: "download:media_not_found:0", wantDownload: 1,
		},
		{
			name:    "token problem",
			mediaID: "99",
			download: func() (core.MediaDownload, error) {
				return core.MediaDownload{}, &core.RequestError{Code: core.CodeUpstreamAuthFailed, Message: "x", MetaCode: 190}
			},
			wantStatus: http.StatusBadGateway, wantCode: "upstream_auth_failed", wantMedia: "download:upstream_auth_failed:0", wantDownload: 1,
		},
		{
			name:    "meta unavailable",
			mediaID: "99",
			download: func() (core.MediaDownload, error) {
				return core.MediaDownload{}, &core.RequestError{Code: core.CodeUpstreamUnavailable, Message: "x", IsRetryable: true}
			},
			wantStatus: http.StatusBadGateway, wantCode: "upstream_unavailable", wantMedia: "download:upstream_unavailable:0", wantDownload: 1,
		},
		{
			name:       "invalid media id never reaches Meta",
			mediaID:    "bad.id",
			download:   func() (core.MediaDownload, error) { return core.MediaDownload{}, nil },
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request", wantMedia: "download:invalid_request:0", wantDownload: 0,
		},
		{
			name:    "stream that breaks midway is recorded as interrupted",
			mediaID: "55",
			download: func() (core.MediaDownload, error) {
				return core.MediaDownload{Content: &interruptedContent{data: []byte("part"), err: errors.New("connection reset")}, MimeType: "video/mp4", Size: 100}, nil
			},
			wantStatus: http.StatusOK, wantBody: "part", wantType: "video/mp4", wantLength: "100", wantMedia: "download:interrupted:4", wantDownload: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeMediaBackend{download: tt.download}
			recorder := &fakeRecorder{}

			rec := doRequest(newMediaRouter(backend, recorder), http.MethodGet, "/v1/media/"+tt.mediaID, "", nil)

			if rec.Code != tt.wantStatus || len(backend.downloadedID) != tt.wantDownload {
				t.Fatalf("status = %d, downloads = %d, want %d and %d (body %s)", rec.Code, len(backend.downloadedID), tt.wantStatus, tt.wantDownload, rec.Body.String())
			}
			if tt.wantCode != "" {
				if decoded := decodeBody(t, rec); decoded["code"] != tt.wantCode {
					t.Errorf("body = %v, want code %s", decoded, tt.wantCode)
				}
			}
			if tt.wantStatus == http.StatusOK {
				if rec.Body.String() != tt.wantBody {
					t.Errorf("body = %q, want %q", rec.Body.String(), tt.wantBody)
				}
				if got := rec.Header().Get("Content-Type"); got != tt.wantType {
					t.Errorf("Content-Type = %q, want %q", got, tt.wantType)
				}
				if got := rec.Header().Get("Content-Length"); got != tt.wantLength {
					t.Errorf("Content-Length = %q, want %q", got, tt.wantLength)
				}
				if got := rec.Header().Get("X-Media-Sha256"); got != tt.wantSHA {
					t.Errorf("X-Media-Sha256 = %q, want %q", got, tt.wantSHA)
				}
				if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Error("nosniff header missing")
				}
			}
			if len(recorder.media) != 1 || recorder.media[0] != tt.wantMedia {
				t.Errorf("recorded media = %v, want [%s]", recorder.media, tt.wantMedia)
			}
		})
	}
}

func TestDeleteMedia(t *testing.T) {
	tests := []struct {
		name       string
		mediaID    string
		deleteErr  error
		wantStatus int
		wantCode   string
		wantDelete int
		wantMedia  string
	}{
		{name: "deleted", mediaID: "1234567890", wantStatus: http.StatusNoContent, wantDelete: 1, wantMedia: "delete:ok:0"},
		{
			name: "media already gone", mediaID: "99", deleteErr: &core.RequestError{Code: core.CodeMediaNotFound, Message: "gone"},
			wantStatus: http.StatusNotFound, wantCode: "media_not_found", wantDelete: 1, wantMedia: "delete:media_not_found:0",
		},
		{
			name: "token rejected", mediaID: "99", deleteErr: &core.RequestError{Code: core.CodeUpstreamAuthFailed, Message: "x", MetaCode: 190},
			wantStatus: http.StatusBadGateway, wantCode: "upstream_auth_failed", wantDelete: 1, wantMedia: "delete:upstream_auth_failed:0",
		},
		{
			name: "meta unavailable", mediaID: "99", deleteErr: &core.RequestError{Code: core.CodeUpstreamUnavailable, Message: "x", IsRetryable: true},
			wantStatus: http.StatusBadGateway, wantCode: "upstream_unavailable", wantDelete: 1, wantMedia: "delete:upstream_unavailable:0",
		},
		{name: "invalid media id never reaches Meta", mediaID: "bad.id", wantStatus: http.StatusBadRequest, wantCode: "invalid_request", wantDelete: 0, wantMedia: "delete:invalid_request:0"},
		{name: "unexpected failure is not leaked", mediaID: "5", deleteErr: errors.New("secret internal detail"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error", wantDelete: 1, wantMedia: "delete:internal_error:0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeMediaBackend{deleteErr: tt.deleteErr}
			recorder := &fakeRecorder{}

			rec := doRequest(newMediaRouter(backend, recorder), http.MethodDelete, "/v1/media/"+tt.mediaID, "", nil)

			if rec.Code != tt.wantStatus || len(backend.deletedID) != tt.wantDelete {
				t.Fatalf("status = %d, deletes = %d, want %d and %d (body %s)", rec.Code, len(backend.deletedID), tt.wantStatus, tt.wantDelete, rec.Body.String())
			}
			if tt.wantCode != "" {
				if decoded := decodeBody(t, rec); decoded["code"] != tt.wantCode {
					t.Errorf("body = %v, want code %s", decoded, tt.wantCode)
				}
			}
			if tt.wantStatus == http.StatusNoContent && rec.Body.Len() != 0 {
				t.Errorf("a 204 must have no body, got %q", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "secret internal detail") {
				t.Errorf("internal error leaked: %s", rec.Body.String())
			}
			if len(recorder.media) != 1 || recorder.media[0] != tt.wantMedia {
				t.Errorf("recorded media = %v, want [%s]", recorder.media, tt.wantMedia)
			}
		})
	}
}

type slowContent struct {
	chunks int
	delay  time.Duration
	sent   int
}

func (s *slowContent) Read(p []byte) (int, error) {
	if s.sent == s.chunks {
		return 0, io.EOF
	}
	time.Sleep(s.delay)
	s.sent++
	return copy(p, "0123456789"), nil
}

func (s *slowContent) Close() error { return nil }

func TestMediaTransfersOutliveTheServerDeadlines(t *testing.T) {
	const serverLimit = 250 * time.Millisecond

	tests := []struct {
		name string
		run  func(t *testing.T, serverURL string)
	}{
		{
			name: "download slower than the write timeout completes",
			run: func(t *testing.T, serverURL string) {
				request, _ := http.NewRequest(http.MethodGet, serverURL+"/v1/media/42", nil)
				request.Header.Set("Authorization", "Bearer "+testAPIKey)

				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatalf("request error = %v", err)
				}
				defer func() { _ = response.Body.Close() }()
				body, err := io.ReadAll(response.Body)

				if err != nil || len(body) != 60 {
					t.Fatalf("read %d bytes, error = %v, want 60 bytes", len(body), err)
				}
			},
		},
		{
			name: "upload slower than the read timeout completes",
			run: func(t *testing.T, serverURL string) {
				reader, writer := io.Pipe()
				form := multipart.NewWriter(writer)
				go func() {
					header := textproto.MIMEHeader{}
					header.Set("Content-Disposition", `form-data; name="file"; filename="slow.bin"`)
					header.Set("Content-Type", "video/mp4")
					part, _ := form.CreatePart(header)
					for range 6 {
						_, _ = part.Write([]byte("0123456789"))
						time.Sleep(100 * time.Millisecond)
					}
					writer.CloseWithError(form.Close())
				}()

				request, _ := http.NewRequest(http.MethodPost, serverURL+"/v1/media", reader)
				request.Header.Set("Authorization", "Bearer "+testAPIKey)
				request.Header.Set("Content-Type", form.FormDataContentType())

				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatalf("request error = %v", err)
				}
				defer func() { _ = response.Body.Close() }()
				if response.StatusCode != http.StatusCreated {
					body, _ := io.ReadAll(response.Body)
					t.Fatalf("status = %d, body %s", response.StatusCode, body)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeMediaBackend{
				uploadID: "7",
				download: func() (core.MediaDownload, error) {
					return core.MediaDownload{Content: &slowContent{chunks: 6, delay: 100 * time.Millisecond}, MimeType: "video/mp4", Size: 60}, nil
				},
			}
			server := httptest.NewUnstartedServer(newMediaRouter(backend, &fakeRecorder{}))
			server.Config.ReadTimeout = serverLimit
			server.Config.WriteTimeout = serverLimit
			server.Start()
			defer server.Close()

			tt.run(t, server.URL)
		})
	}
}
