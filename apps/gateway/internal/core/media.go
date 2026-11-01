package core

import (
	"context"
	"io"
	"mime"
	"strings"
)

const maxMediaIDLength = 128

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

type MediaService struct {
	uploader      MediaUploader
	downloader    MediaDownloader
	phoneNumberID string
}

func NewMediaService(uploader MediaUploader, downloader MediaDownloader, phoneNumberID string) *MediaService {
	return &MediaService{uploader: uploader, downloader: downloader, phoneNumberID: phoneNumberID}
}

func (s *MediaService) Upload(ctx context.Context, upload MediaUpload) (MediaUploadResult, error) {
	mimeType := strings.TrimSpace(upload.MimeType)
	if !isValidMimeType(mimeType) {
		return MediaUploadResult{}, invalidRequest("the file part needs a Content-Type such as image/png")
	}
	upload.MimeType = mimeType

	mediaID, err := s.uploader.UploadMedia(ctx, s.phoneNumberID, upload)
	if err != nil {
		return MediaUploadResult{}, err
	}
	return MediaUploadResult{MediaID: mediaID, MimeType: mimeType}, nil
}

func (s *MediaService) Download(ctx context.Context, mediaID string) (MediaDownload, error) {
	if !isValidMediaID(mediaID) {
		return MediaDownload{}, invalidRequest("media id must be 1 to 128 letters, digits, hyphens, or underscores")
	}
	return s.downloader.DownloadMedia(ctx, mediaID)
}

func isValidMimeType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	kind, subtype, found := strings.Cut(mediaType, "/")
	return found && kind != "" && subtype != "" && !strings.Contains(subtype, "/")
}

func isValidMediaID(value string) bool {
	if value == "" || len(value) > maxMediaIDLength {
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
