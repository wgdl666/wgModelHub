package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
)

// DownloadPublicURL 下载无鉴权输出 URL 到内存；仅适合图片等小媒体，视频结果应走 OpenPublicURL 流式读取。
func DownloadPublicURL(ctx context.Context, client *http.Client, providerName, url string, maxBytes int) ([]byte, error) {
	response, err := OpenPublicURL(ctx, client, providerName, url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	// 多读 1 字节区分恰好上限与超限，避免静默截断。
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, Wrap(ErrorUnavailable, providerName+" read media failed", err)
	}
	if len(data) > maxBytes {
		return nil, Errorf(ErrorInvalidResponse, "%s media exceeds %d bytes", providerName, maxBytes)
	}
	return data, nil
}

// OpenPublicURL 发起 GET 并在 2xx 时返回 Body 仍打开的响应；调用方负责 Close，供视频流式分块而不 ReadAll。
func OpenPublicURL(ctx context.Context, client *http.Client, providerName, url string) (*http.Response, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil, New(ErrorInvalidResponse, providerName+" media url is empty")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, Wrap(ErrorInvalidArgument, "create download request", err)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, Wrap(ErrorUnavailable, providerName+" download failed", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, FromHTTP(providerName, response.StatusCode)
	}
	return response, nil
}
