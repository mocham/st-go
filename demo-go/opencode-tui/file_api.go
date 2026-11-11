package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

func (client *apiClient) readMessageFile(ctx context.Context, directory string, file messageFile) ([]byte, string, error) {
	if file.Text != nil {
		return []byte(*file.Text), "text/plain", nil
	}
	if file.Inline {
		if base64.StdEncoding.DecodedLen(len(file.Data)) > maxAPIResponse {
			return nil, "", fmt.Errorf("attachment exceeds %d bytes", maxAPIResponse)
		}
		data, err := base64.StdEncoding.DecodeString(file.Data)
		return data, file.MIME, err
	}
	if strings.HasPrefix(file.URI, "data:") {
		header, body, ok := strings.Cut(strings.TrimPrefix(file.URI, "data:"), ",")
		if !ok || len(body) > maxAPIResponse*3 {
			return nil, "", fmt.Errorf("invalid or oversized data attachment")
		}
		var data []byte
		var err error
		if strings.HasSuffix(header, ";base64") {
			data, err = base64.StdEncoding.DecodeString(body)
		} else {
			var decoded string
			decoded, err = url.PathUnescape(body)
			data = []byte(decoded)
		}
		if len(data) > maxAPIResponse {
			return nil, "", fmt.Errorf("attachment exceeds %d bytes", maxAPIResponse)
		}
		return data, strings.Split(header, ";")[0], err
	}
	if client == nil {
		return nil, "", fmt.Errorf("no server connection")
	}
	if file.Path == "" {
		return nil, "", fmt.Errorf("this attachment has no readable server file path")
	}
	name := file.Path
	if !path.IsAbs(name) {
		name = path.Join(directory, name)
	}
	// fs/read is confined to its requested location. For absolute paths (including
	// saved tool outputs), explicitly request that file's parent on the server.
	directory, name = path.Dir(name), path.Base(name)
	query := url.Values{"location[directory]": {directory}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint("/api/fs/read/"+url.PathEscape(name)+"?"+query.Encode()), nil)
	if err != nil {
		return nil, "", err
	}
	if client.authorization != "" {
		request.Header.Set("Authorization", client.authorization)
	}
	request.Header.Set("Accept", "*/*")
	transport := *client.requests
	transport.CheckRedirect = func(next *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	response, err := transport.Do(request)
	if err != nil {
		return nil, "", connectionError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, "", fmt.Errorf("OpenCode file: %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponse+1))
	if len(data) > maxAPIResponse {
		return nil, "", fmt.Errorf("file exceeds %d bytes", maxAPIResponse)
	}
	return data, response.Header.Get("Content-Type"), err
}

func filePreview(name, mediaType string, data []byte) (string, *fileMedia) {
	mediaType, _, _ = mime.ParseMediaType(mediaType)
	if mediaType == "" || mediaType == "application/octet-stream" {
		mediaType = http.DetectContentType(data)
	}
	if bytes.HasPrefix(data, []byte("%PDF-")) {
		mediaType = "application/pdf"
	}
	extensions := map[string]string{"application/pdf": ".pdf", "image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}
	if extension := extensions[mediaType]; extension != "" {
		return fmt.Sprintf("%s\n\n%s · %d bytes\n\nUse the rich preview pane (F2) to view this file.", safeText(name), mediaType, len(data)), &fileMedia{data: data, extension: extension}
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return fmt.Sprintf("%s\n\nBinary file · %d bytes · %s", safeText(name), len(data), mediaType), nil
	}
	truncated := ""
	const maxTextPreview = 256 << 10
	if len(data) > maxTextPreview {
		data = data[:maxTextPreview]
		for !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
		truncated = "\n\n[Preview truncated at 256 KiB.]"
	}
	text := string(data)
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return text + truncated, nil
	default:
		return codeBlock(safeText(name), "") + "\n\n" + codeBlock(text, "") + truncated, nil
	}
}
