package core

import (
	"context"
	"fmt"
	"io"
	"mime"
	"strings"
)

const (
	maxIdentifierLength = 128

	stickerLimitBytes  = 500 << 10
	imageLimitBytes    = 5 << 20
	streamLimitBytes   = 16 << 20
	documentLimitBytes = 100 << 20
)

type MediaUpload struct {
	Content  io.Reader
	MimeType string
	Filename string
}

type MediaUploadResult struct {
	MediaID  string
	MimeType string
}

type MediaDownload struct {
	Content  io.ReadCloser
	MimeType string
	SHA256   string
	Size     int64
}

type MediaUploader interface {
	UploadMedia(ctx context.Context, phoneNumberID string, upload MediaUpload) (string, error)
}

type MediaDownloader interface {
	DownloadMedia(ctx context.Context, mediaID string) (MediaDownload, error)
}

type MediaDeleter interface {
	DeleteMedia(ctx context.Context, mediaID string) error
}

type MediaService struct {
	uploader      MediaUploader
	downloader    MediaDownloader
	deleter       MediaDeleter
	phoneNumberID string
}

func NewMediaService(uploader MediaUploader, downloader MediaDownloader, deleter MediaDeleter, phoneNumberID string) *MediaService {
	return &MediaService{uploader: uploader, downloader: downloader, deleter: deleter, phoneNumberID: phoneNumberID}
}

func (s *MediaService) Upload(ctx context.Context, upload MediaUpload) (MediaUploadResult, error) {
	mimeType := strings.TrimSpace(upload.MimeType)
	if !isValidMimeType(mimeType) {
		return MediaUploadResult{}, invalidRequest("the file part needs a Content-Type such as image/png")
	}
	upload.MimeType = mimeType
	upload.Content = newSizeLimitedReader(upload.Content, sizeLimitFor(mimeType))

	mediaID, err := s.uploader.UploadMedia(ctx, s.phoneNumberID, upload)
	if err != nil {
		return MediaUploadResult{}, err
	}
	return MediaUploadResult{MediaID: mediaID, MimeType: mimeType}, nil
}

func (s *MediaService) Download(ctx context.Context, mediaID string) (MediaDownload, error) {
	if !isSafeIdentifier(mediaID) {
		return MediaDownload{}, invalidMediaID()
	}
	return s.downloader.DownloadMedia(ctx, mediaID)
}

func (s *MediaService) Delete(ctx context.Context, mediaID string) error {
	if !isSafeIdentifier(mediaID) {
		return invalidMediaID()
	}
	return s.deleter.DeleteMedia(ctx, mediaID)
}

func invalidMediaID() *RequestError {
	return invalidRequest("media id must be 1 to 128 letters, digits, hyphens, or underscores")
}

type sizeLimit struct {
	bytes int64
	label string
}

func sizeLimitFor(mimeType string) sizeLimit {
	mediaType, _, err := mime.ParseMediaType(mimeType)
	if err != nil {
		return sizeLimit{bytes: documentLimitBytes, label: "files"}
	}
	switch {
	case mediaType == "image/webp":
		return sizeLimit{bytes: stickerLimitBytes, label: "stickers"}
	case strings.HasPrefix(mediaType, "image/"):
		return sizeLimit{bytes: imageLimitBytes, label: "images"}
	case strings.HasPrefix(mediaType, "audio/"):
		return sizeLimit{bytes: streamLimitBytes, label: "audio files"}
	case strings.HasPrefix(mediaType, "video/"):
		return sizeLimit{bytes: streamLimitBytes, label: "videos"}
	default:
		return sizeLimit{bytes: documentLimitBytes, label: "documents"}
	}
}

type sizeLimitedReader struct {
	reader    io.Reader
	remaining int64
	err       error
}

func newSizeLimitedReader(reader io.Reader, limit sizeLimit) *sizeLimitedReader {
	return &sizeLimitedReader{
		reader:    reader,
		remaining: limit.bytes,
		err: &RequestError{
			Code:    CodePayloadTooLarge,
			Message: fmt.Sprintf("%s are limited to %s", limit.label, limit.size()),
		},
	}
}

func (l sizeLimit) size() string {
	if l.bytes >= 1<<20 {
		return fmt.Sprintf("%d MB", l.bytes>>20)
	}
	return fmt.Sprintf("%d KB", l.bytes>>10)
}

func (r *sizeLimitedReader) Read(p []byte) (int, error) {
	if r.remaining < 0 {
		return 0, r.err
	}
	if int64(len(p)) > r.remaining+1 {
		p = p[:r.remaining+1]
	}
	n, err := r.reader.Read(p)
	if int64(n) > r.remaining {
		allowed := int(r.remaining)
		r.remaining = -1
		return allowed, r.err
	}
	r.remaining -= int64(n)
	return n, err
}

func isValidMimeType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	kind, subtype, found := strings.Cut(mediaType, "/")
	return found && kind != "" && subtype != "" && !strings.Contains(subtype, "/")
}

func isSafeIdentifier(value string) bool {
	if value == "" || len(value) > maxIdentifierLength {
		return false
	}
	for _, character := range value {
		isLetter := (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')
		isDigit := character >= '0' && character <= '9'
		if !isLetter && !isDigit && character != '-' && character != '_' {
			return false
		}
	}
	return true
}
