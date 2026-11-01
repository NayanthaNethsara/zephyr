package core

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type fakeMediaBackend struct {
	uploadedTo   string
	uploadedType string
	uploadedBody string
	uploadID     string
	uploadErr    error
	downloadedID string
	download     MediaDownload
	downloadErr  error
	uploads      int
	downloads    int
}

func (f *fakeMediaBackend) UploadMedia(_ context.Context, phoneNumberID string, upload MediaUpload) (string, error) {
	f.uploads++
	f.uploadedTo = phoneNumberID
	f.uploadedType = upload.MimeType
	body, _ := io.ReadAll(upload.Content)
	f.uploadedBody = string(body)
	return f.uploadID, f.uploadErr
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
			service := NewMediaService(backend, backend, "111")

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

			got, err := NewMediaService(backend, backend, "111").Download(context.Background(), tt.mediaID)

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
