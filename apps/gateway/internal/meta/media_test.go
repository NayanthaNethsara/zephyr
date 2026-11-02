package meta

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

type uploadedForm struct {
	path          string
	authorization string
	product       string
	mimeField     string
	filename      string
	fileType      string
	fileBytes     []byte
}

func newTLSClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client := NewClient(server.URL, "v24.0", "secret-token")
	client.mediaClient = server.Client()
	return client, server
}

func readUploadedForm(t *testing.T, r *http.Request) uploadedForm {
	t.Helper()
	form := uploadedForm{path: r.URL.Path, authorization: r.Header.Get("Authorization")}

	reader, err := r.MultipartReader()
	if err != nil {
		t.Errorf("request is not multipart: %v", err)
		return form
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return form
		}
		if err != nil {
			t.Errorf("reading multipart: %v", err)
			return form
		}
		content, _ := io.ReadAll(part)
		switch part.FormName() {
		case "messaging_product":
			form.product = string(content)
		case "type":
			form.mimeField = string(content)
		case "file":
			form.filename = part.FileName()
			form.fileType = part.Header.Get("Content-Type")
			form.fileBytes = content
		}
	}
}

func TestUploadMedia(t *testing.T) {
	large := bytes.Repeat([]byte("0123456789abcdef"), 3<<16)

	tests := []struct {
		name         string
		upload       core.MediaUpload
		wantFilename string
		wantBytes    []byte
	}{
		{
			name:         "png upload",
			upload:       core.MediaUpload{Content: strings.NewReader("png-bytes"), MimeType: "image/png", Filename: "photo.png"},
			wantFilename: "photo.png",
			wantBytes:    []byte("png-bytes"),
		},
		{
			name:         "directory components are stripped from the filename",
			upload:       core.MediaUpload{Content: strings.NewReader("x"), MimeType: "image/png", Filename: "../../etc/passwd.png"},
			wantFilename: "passwd.png",
			wantBytes:    []byte("x"),
		},
		{
			name:         "windows path keeps only the file name",
			upload:       core.MediaUpload{Content: strings.NewReader("x"), MimeType: "application/pdf", Filename: `C:\docs\invoice.pdf`},
			wantFilename: "invoice.pdf",
			wantBytes:    []byte("x"),
		},
		{
			name:         "quotes and control characters are removed",
			upload:       core.MediaUpload{Content: strings.NewReader("x"), MimeType: "image/png", Filename: "a\"b\r\n.png"},
			wantFilename: "ab.png",
			wantBytes:    []byte("x"),
		},
		{
			name:         "missing filename gets a default",
			upload:       core.MediaUpload{Content: strings.NewReader("x"), MimeType: "image/png", Filename: ""},
			wantFilename: "upload",
			wantBytes:    []byte("x"),
		},
		{
			name:         "mime parameters survive",
			upload:       core.MediaUpload{Content: strings.NewReader("ogg"), MimeType: "audio/ogg; codecs=opus", Filename: "v.ogg"},
			wantFilename: "v.ogg",
			wantBytes:    []byte("ogg"),
		},
		{
			name:         "multi megabyte body arrives intact",
			upload:       core.MediaUpload{Content: bytes.NewReader(large), MimeType: "video/mp4", Filename: "clip.mp4"},
			wantFilename: "clip.mp4",
			wantBytes:    large,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var received uploadedForm
			client, _ := newTLSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received = readUploadedForm(t, r)
				_, _ = w.Write([]byte(`{"id":"987654321"}`))
			}))

			mediaID, err := client.UploadMedia(context.Background(), "111", tt.upload)
			if err != nil || mediaID != "987654321" {
				t.Fatalf("UploadMedia() = %q, %v", mediaID, err)
			}

			if received.path != "/v24.0/111/media" || received.authorization != "Bearer secret-token" {
				t.Errorf("path/authorization = %q/%q", received.path, received.authorization)
			}
			if received.product != "whatsapp" || received.mimeField != tt.upload.MimeType || received.fileType != tt.upload.MimeType {
				t.Errorf("product/type/file type = %q/%q/%q", received.product, received.mimeField, received.fileType)
			}
			if received.filename != tt.wantFilename {
				t.Errorf("filename = %q, want %q", received.filename, tt.wantFilename)
			}
			if !bytes.Equal(received.fileBytes, tt.wantBytes) {
				t.Errorf("file bytes differ: got %d, want %d", len(received.fileBytes), len(tt.wantBytes))
			}
		})
	}
}

type failingReader struct {
	data []byte
	err  error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

func TestUploadMediaFailures(t *testing.T) {
	sourceFailure := errors.New("client went away")

	tests := []struct {
		name          string
		status        int
		body          string
		source        io.Reader
		closeServer   bool
		wantCode      string
		wantRetryable bool
		wantSource    bool
	}{
		{name: "unsupported file", status: 400, body: `{"error":{"message":"Param file must be a file","code":131053}}`, wantCode: core.CodeInvalidMedia},
		{name: "invalid parameter", status: 400, body: `{"error":{"message":"bad","code":100}}`, wantCode: core.CodeInvalidRequest},
		{name: "expired token", status: 401, body: `{"error":{"message":"expired","code":190}}`, wantCode: core.CodeUpstreamAuthFailed},
		{name: "throttled", status: 429, body: `{"error":{"message":"slow","code":130429}}`, wantCode: core.CodeRateLimited, wantRetryable: true},
		{name: "html error page", status: 502, body: `<html>bad gateway</html>`, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "success without an id", status: 200, body: `{}`, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "success with unreadable body", status: 200, body: `nope`, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "unreachable server", status: 200, body: `{}`, closeServer: true, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{
			name:       "source failing midway is reported as the caller's error, not Meta's",
			status:     200,
			body:       `{"id":"1"}`,
			source:     &failingReader{data: bytes.Repeat([]byte("a"), 1000), err: sourceFailure},
			wantSource: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, server := newTLSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			if tt.closeServer {
				server.Close()
			}
			source := tt.source
			if source == nil {
				source = strings.NewReader("data")
			}

			_, err := client.UploadMedia(context.Background(), "111", core.MediaUpload{Content: source, MimeType: "image/png", Filename: "a.png"})

			var requestErr *core.RequestError
			if tt.wantSource {
				if !errors.Is(err, sourceFailure) || errors.As(err, &requestErr) {
					t.Fatalf("error = %#v, want the source failure and no RequestError", err)
				}
				return
			}
			if !errors.As(err, &requestErr) || requestErr.Code != tt.wantCode || requestErr.IsRetryable != tt.wantRetryable {
				t.Fatalf("error = %#v, want %s retryable=%v", err, tt.wantCode, tt.wantRetryable)
			}
			if strings.Contains(requestErr.Message, "secret-token") || strings.Contains(requestErr.Message, "127.0.0.1") {
				t.Errorf("message leaks internals: %q", requestErr.Message)
			}
		})
	}
}

func mediaHandler(t *testing.T, infoStatus int, infoBody func(fileURL string) string, serve http.HandlerFunc) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v24.0/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("media lookup authorization = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(infoStatus)
		_, _ = w.Write([]byte(infoBody("https://" + r.Host + "/files/media-1")))
	})
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("media content authorization = %q", r.Header.Get("Authorization"))
		}
		serve(w, r)
	})
	return mux
}

func TestDownloadMedia(t *testing.T) {
	tests := []struct {
		name         string
		info         func(fileURL string) string
		serve        http.HandlerFunc
		wantMime     string
		wantSHA      string
		wantSize     int64
		wantBody     string
		wantInfoPath string
	}{
		{
			name: "full metadata and known length",
			info: func(fileURL string) string {
				return `{"url":"` + fileURL + `","mime_type":"image/jpeg","sha256":"abc123","file_size":7}`
			},
			serve:    func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("jpegdat")) },
			wantMime: "image/jpeg", wantSHA: "abc123", wantSize: 7, wantBody: "jpegdat",
		},
		{
			name: "mime type falls back to the response header",
			info: func(fileURL string) string { return `{"url":"` + fileURL + `","sha256":"abc"}` },
			serve: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write([]byte("pngdata"))
			},
			wantMime: "image/png", wantSHA: "abc", wantSize: 7, wantBody: "pngdata",
		},
		{
			name: "declared size is used when the length is unknown",
			info: func(fileURL string) string {
				return `{"url":"` + fileURL + `","mime_type":"audio/ogg","file_size":8}`
			},
			serve: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("oggd"))
				w.(http.Flusher).Flush()
				_, _ = w.Write([]byte("ata!"))
			},
			wantMime: "audio/ogg", wantSize: 8, wantBody: "oggdata!",
		},
		{
			name: "unknown length and no declared size",
			info: func(fileURL string) string { return `{"url":"` + fileURL + `","mime_type":"audio/ogg"}` },
			serve: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("a"))
				w.(http.Flusher).Flush()
				_, _ = w.Write([]byte("b"))
			},
			wantMime: "audio/ogg", wantSize: -1, wantBody: "ab",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newTLSClient(t, mediaHandler(t, http.StatusOK, tt.info, tt.serve))

			download, err := client.DownloadMedia(context.Background(), "media-1")
			if err != nil {
				t.Fatalf("DownloadMedia() error = %v", err)
			}
			body, readErr := io.ReadAll(download.Content)
			if closeErr := download.Content.Close(); closeErr != nil || readErr != nil {
				t.Fatalf("read/close errors = %v/%v", readErr, closeErr)
			}

			if string(body) != tt.wantBody || download.MimeType != tt.wantMime || download.SHA256 != tt.wantSHA || download.Size != tt.wantSize {
				t.Errorf("download = %+v, body %q", download, body)
			}
		})
	}
}

func TestDownloadMediaFailures(t *testing.T) {
	validInfo := func(fileURL string) string { return `{"url":"` + fileURL + `","mime_type":"image/png"}` }
	unreachable := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }

	tests := []struct {
		name          string
		infoStatus    int
		info          func(fileURL string) string
		serve         http.HandlerFunc
		closeServer   bool
		wantCode      string
		wantRetryable bool
	}{
		{name: "unreachable server", infoStatus: 200, info: validInfo, closeServer: true, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "unknown media id", infoStatus: 400, info: func(string) string { return `{"error":{"message":"Object does not exist","code":100}}` }, wantCode: core.CodeMediaNotFound},
		{name: "lookup answers not found", infoStatus: 404, info: func(string) string { return `{}` }, wantCode: core.CodeMediaNotFound},
		{name: "expired token", infoStatus: 401, info: func(string) string { return `{"error":{"message":"expired","code":190}}` }, wantCode: core.CodeUpstreamAuthFailed},
		{name: "html error page", infoStatus: 502, info: func(string) string { return `<html></html>` }, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "plain http location is refused", infoStatus: 200, info: func(string) string { return `{"url":"http://example.com/file"}` }, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "location missing", infoStatus: 200, info: func(string) string { return `{"mime_type":"image/png"}` }, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "location that is not a url", infoStatus: 200, info: func(string) string { return `{"url":"://nonsense"}` }, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "file expired between lookup and download", infoStatus: 200, info: validInfo, serve: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }, wantCode: core.CodeMediaNotFound},
		{name: "file server error", infoStatus: 200, info: validInfo, serve: unreachable, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "file server throttles", infoStatus: 200, info: validInfo, serve: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }, wantCode: core.CodeRateLimited, wantRetryable: true},
		{name: "file server rejects the token", infoStatus: 200, info: validInfo, serve: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }, wantCode: core.CodeUpstreamAuthFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serve := tt.serve
			if serve == nil {
				serve = unreachable
			}
			client, server := newTLSClient(t, mediaHandler(t, tt.infoStatus, tt.info, serve))
			if tt.closeServer {
				server.Close()
			}

			download, err := client.DownloadMedia(context.Background(), "media-1")

			var requestErr *core.RequestError
			if !errors.As(err, &requestErr) || requestErr.Code != tt.wantCode || requestErr.IsRetryable != tt.wantRetryable {
				t.Fatalf("error = %#v, want %s retryable=%v", err, tt.wantCode, tt.wantRetryable)
			}
			if download.Content != nil {
				t.Error("a failed download must not return content")
			}
			if strings.Contains(requestErr.Message, "secret-token") {
				t.Errorf("message leaks the token: %q", requestErr.Message)
			}
		})
	}
}

type closeRecorder struct {
	io.Reader
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

func TestCancelOnClose(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "closing the body releases the request context"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			inner := &closeRecorder{Reader: strings.NewReader("x")}
			body := &cancelOnClose{ReadCloser: inner, cancel: cancel}

			if ctx.Err() != nil {
				t.Fatal("context canceled before close")
			}
			if err := body.Close(); err != nil {
				t.Fatal(err)
			}
			if !inner.closed || ctx.Err() == nil {
				t.Fatalf("inner closed = %v, context error = %v", inner.closed, ctx.Err())
			}
		})
	}
}

func TestSafeFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{name: "plain", filename: "a.png", want: "a.png"},
		{name: "unix directories", filename: "/var/tmp/a.png", want: "a.png"},
		{name: "parent traversal", filename: "../../a.png", want: "a.png"},
		{name: "dot only", filename: ".", want: "upload"},
		{name: "slash only", filename: "/", want: "upload"},
		{name: "empty", filename: "", want: "upload"},
		{name: "unicode name is kept", filename: "r\u00e9sum\u00e9.pdf", want: "r\u00e9sum\u00e9.pdf"},
		{name: "overlong name is truncated", filename: strings.Repeat("a", 400), want: strings.Repeat("a", 255)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := safeFilename(tt.filename); got != tt.want {
				t.Errorf("safeFilename(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestDeleteMedia(t *testing.T) {
	tests := []struct {
		name           string
		status         int
		body           string
		isServerClosed bool
		wantCode       string
		wantRetryable  bool
	}{
		{name: "deleted", status: http.StatusOK, body: `{"success":true}`},
		{name: "meta reports no success", status: http.StatusOK, body: `{"success":false}`, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "body is not json", status: http.StatusOK, body: `ok`, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "unknown media id", status: http.StatusBadRequest, body: `{"error":{"message":"Object does not exist","code":100}}`, wantCode: core.CodeMediaNotFound},
		{name: "answers not found", status: http.StatusNotFound, body: `{}`, wantCode: core.CodeMediaNotFound},
		{name: "expired token", status: http.StatusUnauthorized, body: `{"error":{"message":"expired","code":190}}`, wantCode: core.CodeUpstreamAuthFailed},
		{name: "html error page", status: http.StatusBadGateway, body: `<html></html>`, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "unreachable server", status: http.StatusOK, body: `{"success":true}`, isServerClosed: true, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotPath, gotAuthorization string
			client, server := newTLSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath, gotAuthorization = r.Method, r.URL.Path, r.Header.Get("Authorization")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			if tt.isServerClosed {
				server.Close()
			}

			err := client.DeleteMedia(context.Background(), "media-1")

			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("DeleteMedia() error = %v", err)
				}
				if gotMethod != http.MethodDelete || gotPath != "/v24.0/media-1" || gotAuthorization != "Bearer secret-token" {
					t.Errorf("request = %s %s with %q", gotMethod, gotPath, gotAuthorization)
				}
				return
			}
			var requestErr *core.RequestError
			if !errors.As(err, &requestErr) || requestErr.Code != tt.wantCode || requestErr.IsRetryable != tt.wantRetryable {
				t.Fatalf("error = %#v, want %s retryable=%v", err, tt.wantCode, tt.wantRetryable)
			}
			if strings.Contains(requestErr.Message, "secret-token") {
				t.Errorf("message leaks the token: %q", requestErr.Message)
			}
		})
	}
}
