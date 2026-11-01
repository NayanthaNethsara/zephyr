package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

const (
	maxMediaBytes       = 100 << 20
	multipartOverhead   = 1 << 20
	mediaDeadline       = 6 * time.Minute
	operationUpload     = "upload"
	operationDownload   = "download"
	resultOK            = "ok"
	resultInterrupted   = "interrupted"
	uploadFilePart      = "file"
	mediaSHA256Header   = "X-Media-Sha256"
	defaultDownloadType = "application/octet-stream"
)

type MediaProvider interface {
	Upload(ctx context.Context, upload core.MediaUpload) (core.MediaUploadResult, error)
	Download(ctx context.Context, mediaID string) (core.MediaDownload, error)
}

type uploadResponse struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type"`
}

type countingReader struct {
	reader io.Reader
	count  atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.count.Add(int64(n))
	return n, err
}

func (h *handler) uploadMedia(c *gin.Context) {
	started := time.Now()
	logger := requestLogger(h.deps.Logger, c)
	extendDeadlines(c)

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxMediaBytes+multipartOverhead)
	part, problem := firstFilePart(c.Request)
	if problem != "" {
		h.rejectMedia(c, logger, operationUpload, started, http.StatusBadRequest, errorBody{Code: core.CodeInvalidRequest, Message: problem})
		return
	}

	file := &countingReader{reader: part}
	result, err := h.deps.Media.Upload(c.Request.Context(), core.MediaUpload{
		Content:  file,
		MimeType: part.Header.Get("Content-Type"),
		Filename: part.FileName(),
	})
	if err != nil {
		h.failMedia(c, logger, operationUpload, started, file.count.Load(), uploadFailure(err))
		return
	}

	took := time.Since(started)
	h.deps.Recorder.MediaResult(operationUpload, resultOK, file.count.Load(), took)
	logger.Info("media uploaded",
		"media_id", result.MediaID,
		"mime_type", result.MimeType,
		"bytes", file.count.Load(),
		"duration_ms", took.Milliseconds(),
	)
	c.JSON(http.StatusCreated, uploadResponse{ID: result.MediaID, MimeType: result.MimeType})
}

func firstFilePart(request *http.Request) (*multipart.Part, string) {
	reader, err := request.MultipartReader()
	if err != nil {
		return nil, "the request must be multipart/form-data with a file part"
	}
	for {
		part, err := reader.NextPart()
		if err != nil {
			return nil, "the request has no file part"
		}
		if part.FormName() == uploadFilePart {
			return part, ""
		}
	}
}

func uploadFailure(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &core.RequestError{Code: codePayloadTooLarge, Message: "the file is larger than the 100 MB limit"}
	}
	var requestErr *core.RequestError
	if !errors.As(err, &requestErr) && errors.Is(err, io.ErrUnexpectedEOF) {
		return &core.RequestError{Code: core.CodeInvalidRequest, Message: "the upload was interrupted or malformed"}
	}
	return err
}

func (h *handler) downloadMedia(c *gin.Context) {
	started := time.Now()
	logger := requestLogger(h.deps.Logger, c)
	extendDeadlines(c)
	mediaID := c.Param("id")

	download, err := h.deps.Media.Download(c.Request.Context(), mediaID)
	if err != nil {
		h.failMedia(c, logger, operationDownload, started, 0, err)
		return
	}
	defer func() { _ = download.Content.Close() }()

	setDownloadHeaders(c, download)
	c.Status(http.StatusOK)
	written, copyErr := io.Copy(c.Writer, download.Content)

	took := time.Since(started)
	if copyErr != nil {
		h.deps.Recorder.MediaResult(operationDownload, resultInterrupted, written, took)
		logger.Error("media stream interrupted", "media_id", mediaID, "bytes", written, "error", copyErr)
		return
	}
	h.deps.Recorder.MediaResult(operationDownload, resultOK, written, took)
	logger.Info("media downloaded",
		"media_id", mediaID,
		"mime_type", download.MimeType,
		"bytes", written,
		"duration_ms", took.Milliseconds(),
	)
}

func setDownloadHeaders(c *gin.Context, download core.MediaDownload) {
	contentType := download.MimeType
	if contentType == "" {
		contentType = defaultDownloadType
	}
	c.Header("Content-Type", contentType)
	c.Header("X-Content-Type-Options", "nosniff")
	if download.SHA256 != "" {
		c.Header(mediaSHA256Header, download.SHA256)
	}
	if download.Size >= 0 {
		c.Header("Content-Length", strconv.FormatInt(download.Size, 10))
	}
}

func (h *handler) failMedia(c *gin.Context, logger *slog.Logger, operation string, started time.Time, size int64, err error) {
	status, body, requestErr := describeError(err)
	took := time.Since(started)
	h.deps.Recorder.MediaResult(operation, body.Code, size, took)

	if requestErr == nil {
		logger.Error("media request failed unexpectedly", "operation", operation, "error", err)
		c.JSON(status, body)
		return
	}

	log := logger.Warn
	if status >= http.StatusInternalServerError {
		log = logger.Error
	}
	log("media request failed",
		"operation", operation,
		"code", requestErr.Code,
		"retryable", requestErr.IsRetryable,
		"meta_code", requestErr.MetaCode,
		"trace_id", requestErr.TraceID,
		"duration_ms", took.Milliseconds(),
	)
	if status == http.StatusRequestEntityTooLarge {
		c.Header("Connection", "close")
	}
	c.JSON(status, body)
}

func (h *handler) rejectMedia(c *gin.Context, logger *slog.Logger, operation string, started time.Time, status int, body errorBody) {
	h.deps.Recorder.MediaResult(operation, body.Code, 0, time.Since(started))
	logger.Warn("media request rejected", "operation", operation, "code", body.Code, "reason", body.Message)
	c.JSON(status, body)
}

func extendDeadlines(c *gin.Context) {
	controller := http.NewResponseController(c.Writer)
	deadline := time.Now().Add(mediaDeadline)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
}
