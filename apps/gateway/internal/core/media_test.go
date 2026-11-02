package core

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type cappedBuffer struct {
	max   int
	data  []byte
	total int64
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.total += int64(len(p))
	if room := c.max - len(c.data); room > 0 {
		c.data = append(c.data, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type fakeMediaBackend struct {
	uploadedTo    string
	uploadedType  string
	uploadedBody  string
	uploadedBytes int64
	uploadID      string
	uploadErr     error
	downloadedID  string
	download      MediaDownload
	downloadErr   error
	deletedID     string
	deleteErr     error
	uploads       int
	downloads     int
	deletes       int
}

func (f *fakeMediaBackend) UploadMedia(_ context.Context, phoneNumberID string, upload MediaUpload) (string, error) {
	f.uploads++
	f.uploadedTo = phoneNumberID
	f.uploadedType = upload.MimeType
	captured := &cappedBuffer{max: 1 << 10}
	_, err := io.Copy(captured, upload.Content)
	f.uploadedBody = string(captured.data)
	f.uploadedBytes = captured.total
	if err != nil {
		return "", err
	}
	return f.uploadID, f.uploadErr
}

func (f *fakeMediaBackend) DeleteMedia(_ context.Context, mediaID string) error {
	f.deletes++
	f.deletedID = mediaID
	return f.deleteErr
}

func (f *fakeMediaBackend) DownloadMedia(_ context.Context, mediaID string) (MediaDownload, error) {
	f.downloads++
	f.downloadedID = mediaID
	return f.download, f.downloadErr
}

func TestMediaServiceUpload(t *testing.T) {
	upstream := &RequestError{Code: CodeInvalidMedia, Message: "bad file", MetaCode: 131053}

	tests := []struct {
		name         string
		mimeType     string
		backendErr   error
		wantUploads  int
		wantMimeType string
		wantCode     string
	}{
		{name: "png", mimeType: "image/png", wantUploads: 1, wantMimeType: "image/png"},
		{name: "pdf document", mimeType: "application/pdf", wantUploads: 1, wantMimeType: "application/pdf"},
		{name: "mime type with parameters is passed through", mimeType: "audio/ogg; codecs=opus", wantUploads: 1, wantMimeType: "audio/ogg; codecs=opus"},
		{name: "surrounding whitespace is trimmed", mimeType: "  video/mp4 ", wantUploads: 1, wantMimeType: "video/mp4"},
		{name: "unlisted type is left for Meta to judge", mimeType: "application/x-something", wantUploads: 1, wantMimeType: "application/x-something"},
		{name: "empty mime type", mimeType: "", wantUploads: 0, wantCode: CodeInvalidRequest},
		{name: "no subtype", mimeType: "image", wantUploads: 0, wantCode: CodeInvalidRequest},
		{name: "empty subtype", mimeType: "image/", wantUploads: 0, wantCode: CodeInvalidRequest},
		{name: "extra slash", mimeType: "image/png/x", wantUploads: 0, wantCode: CodeInvalidRequest},
		{name: "garbage", mimeType: "not a mime type", wantUploads: 0, wantCode: CodeInvalidRequest},
		{name: "meta rejection is returned", mimeType: "image/png", backendErr: upstream, wantUploads: 1, wantCode: CodeInvalidMedia},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeMediaBackend{uploadID: "5551234", uploadErr: tt.backendErr}
			service := NewMediaService(backend, backend, backend, "111")

			result, err := service.Upload(context.Background(), MediaUpload{
				Content: strings.NewReader("file-bytes"), MimeType: tt.mimeType, Filename: "a.png",
			})

			if backend.uploads != tt.wantUploads {
				t.Fatalf("uploads = %d, want %d", backend.uploads, tt.wantUploads)
			}
			if tt.wantCode != "" {
				var requestErr *RequestError
				if !errors.As(err, &requestErr) || requestErr.Code != tt.wantCode {
					t.Fatalf("error = %#v, want code %s", err, tt.wantCode)
				}
				return
			}
			if err != nil || result.MediaID != "5551234" || result.MimeType != tt.wantMimeType {
				t.Fatalf("Upload() = %+v, %v", result, err)
			}
			if backend.uploadedTo != "111" || backend.uploadedType != tt.wantMimeType || backend.uploadedBody != "file-bytes" {
				t.Errorf("backend saw number %q, type %q, body %q", backend.uploadedTo, backend.uploadedType, backend.uploadedBody)
			}
		})
	}
}

func TestMediaServiceDownload(t *testing.T) {
	notFound := &RequestError{Code: CodeMediaNotFound, Message: "gone"}

	tests := []struct {
		name          string
		mediaID       string
		backendErr    error
		wantDownloads int
		wantCode      string
	}{
		{name: "numeric id", mediaID: "1234567890123456", wantDownloads: 1},
		{name: "id with hyphen and underscore", mediaID: "abc-DEF_123", wantDownloads: 1},
		{name: "longest allowed id", mediaID: strings.Repeat("1", 128), wantDownloads: 1},
		{name: "empty id", mediaID: "", wantDownloads: 0, wantCode: CodeInvalidRequest},
		{name: "id that is too long", mediaID: strings.Repeat("1", 129), wantDownloads: 0, wantCode: CodeInvalidRequest},
		{name: "path traversal attempt", mediaID: "..%2f..", wantDownloads: 0, wantCode: CodeInvalidRequest},
		{name: "slash in id", mediaID: "12/34", wantDownloads: 0, wantCode: CodeInvalidRequest},
		{name: "query characters in id", mediaID: "12?x=1", wantDownloads: 0, wantCode: CodeInvalidRequest},
		{name: "space in id", mediaID: "12 34", wantDownloads: 0, wantCode: CodeInvalidRequest},
		{name: "non ascii digits", mediaID: "١٢٣", wantDownloads: 0, wantCode: CodeInvalidRequest},
		{name: "meta cannot find the media", mediaID: "42", backendErr: notFound, wantDownloads: 1, wantCode: CodeMediaNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeMediaBackend{
				download:    MediaDownload{MimeType: "image/png", SHA256: "abc", Size: 10},
				downloadErr: tt.backendErr,
			}

			got, err := NewMediaService(backend, backend, backend, "111").Download(context.Background(), tt.mediaID)

			if backend.downloads != tt.wantDownloads {
				t.Fatalf("downloads = %d, want %d", backend.downloads, tt.wantDownloads)
			}
			if tt.wantCode != "" {
				var requestErr *RequestError
				if !errors.As(err, &requestErr) || requestErr.Code != tt.wantCode {
					t.Fatalf("error = %#v, want code %s", err, tt.wantCode)
				}
				return
			}
			if err != nil || got.MimeType != "image/png" || got.SHA256 != "abc" || got.Size != 10 || backend.downloadedID != tt.mediaID {
				t.Fatalf("Download() = %+v, %v (backend saw %q)", got, err, backend.downloadedID)
			}
		})
	}
}

func TestMediaServiceUploadSizeLimits(t *testing.T) {
	tests := []struct {
		name     string
		mimeType string
		size     int64
		wantSent bool
		wantText string
	}{
		{name: "png at the limit", mimeType: "image/png", size: 5 << 20, wantSent: true},
		{name: "png over the limit", mimeType: "image/png", size: 5<<20 + 1, wantText: "images are limited to 5 MB"},
		{name: "jpeg over the limit", mimeType: "image/jpeg", size: 6 << 20, wantText: "images are limited to 5 MB"},
		{name: "sticker at the limit", mimeType: "image/webp", size: 500 << 10, wantSent: true},
		{name: "sticker over the limit", mimeType: "image/webp", size: 500<<10 + 1, wantText: "stickers are limited to 500 KB"},
		{name: "audio at the limit", mimeType: "audio/mpeg", size: 16 << 20, wantSent: true},
		{name: "audio over the limit", mimeType: "audio/ogg; codecs=opus", size: 16<<20 + 1, wantText: "audio files are limited to 16 MB"},
		{name: "video over the limit", mimeType: "video/mp4", size: 17 << 20, wantText: "videos are limited to 16 MB"},
		{name: "pdf at the limit", mimeType: "application/pdf", size: 100 << 20, wantSent: true},
		{name: "pdf over the limit", mimeType: "application/pdf", size: 100<<20 + 1, wantText: "documents are limited to 100 MB"},
		{name: "unlisted type uses the document limit", mimeType: "application/x-something", size: 100<<20 + 1, wantText: "documents are limited to 100 MB"},
		{name: "empty file", mimeType: "image/png", size: 0, wantSent: true},
		{name: "mime type casing does not change the limit", mimeType: "IMAGE/PNG", size: 5<<20 + 1, wantText: "images are limited to 5 MB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeMediaBackend{uploadID: "9"}

			result, err := NewMediaService(backend, backend, backend, "111").Upload(context.Background(), MediaUpload{
				Content: io.LimitReader(zeroReader{}, tt.size), MimeType: tt.mimeType, Filename: "f",
			})

			if tt.wantSent {
				if err != nil || result.MediaID != "9" || backend.uploadedBytes != tt.size {
					t.Fatalf("Upload() = %+v, %v, backend read %d bytes, want %d", result, err, backend.uploadedBytes, tt.size)
				}
				return
			}
			var requestErr *RequestError
			if !errors.As(err, &requestErr) || requestErr.Code != CodePayloadTooLarge || requestErr.Message != tt.wantText {
				t.Fatalf("error = %#v, want payload_too_large %q", err, tt.wantText)
			}
			if backend.uploadedBytes > tt.size-1 {
				t.Errorf("backend received %d bytes of a %d byte file that should have been cut off", backend.uploadedBytes, tt.size)
			}
		})
	}
}

func TestSizeLimitedReaderStaysFailed(t *testing.T) {
	tests := []struct {
		name        string
		sourceBytes int64
		limit       int64
		wantFirst   int
		wantFailed  bool
	}{
		{name: "source over the limit fails and keeps failing", sourceBytes: 100, limit: 10, wantFirst: 10, wantFailed: true},
		{name: "source exactly at the limit ends cleanly", sourceBytes: 10, limit: 10, wantFirst: 10},
		{name: "source under the limit ends cleanly", sourceBytes: 5, limit: 10, wantFirst: 5},
		{name: "empty source ends cleanly", sourceBytes: 0, limit: 10, wantFirst: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := newSizeLimitedReader(io.LimitReader(zeroReader{}, tt.sourceBytes), sizeLimit{bytes: tt.limit, label: "files"})
			buffer := make([]byte, 64)

			total := 0
			var lastErr error
			for range 4 {
				n, err := reader.Read(buffer)
				total += n
				lastErr = err
			}

			if total != tt.wantFirst {
				t.Errorf("read %d bytes, want %d", total, tt.wantFirst)
			}
			var requestErr *RequestError
			if gotFailed := errors.As(lastErr, &requestErr); gotFailed != tt.wantFailed {
				t.Errorf("final error = %v, want failed=%v", lastErr, tt.wantFailed)
			}
		})
	}
}

func TestMediaServiceDelete(t *testing.T) {
	notFound := &RequestError{Code: CodeMediaNotFound, Message: "gone"}

	tests := []struct {
		name        string
		mediaID     string
		backendErr  error
		wantDeletes int
		wantCode    string
	}{
		{name: "numeric id", mediaID: "1234567890", wantDeletes: 1},
		{name: "empty id", mediaID: "", wantDeletes: 0, wantCode: CodeInvalidRequest},
		{name: "path traversal attempt", mediaID: "../me", wantDeletes: 0, wantCode: CodeInvalidRequest},
		{name: "id that is too long", mediaID: strings.Repeat("1", 129), wantDeletes: 0, wantCode: CodeInvalidRequest},
		{name: "meta cannot find the media", mediaID: "42", backendErr: notFound, wantDeletes: 1, wantCode: CodeMediaNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeMediaBackend{deleteErr: tt.backendErr}

			err := NewMediaService(backend, backend, backend, "111").Delete(context.Background(), tt.mediaID)

			if backend.deletes != tt.wantDeletes {
				t.Fatalf("deletes = %d, want %d", backend.deletes, tt.wantDeletes)
			}
			if tt.wantCode == "" {
				if err != nil || backend.deletedID != tt.mediaID {
					t.Fatalf("Delete() = %v, backend saw %q", err, backend.deletedID)
				}
				return
			}
			var requestErr *RequestError
			if !errors.As(err, &requestErr) || requestErr.Code != tt.wantCode {
				t.Fatalf("error = %#v, want code %s", err, tt.wantCode)
			}
		})
	}
}
