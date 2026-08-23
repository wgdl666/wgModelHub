package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
)

// DownloadPublicURL 下载无鉴权输出 URL；DashScope/OminiLink 成功任务返回的临时链接共用此逻辑。
func DownloadPublicURL(ctx context.Context, client *http.Client, providerName, url string, maxBytes int) ([]byte, error) {
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
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, FromHTTP(providerName, response.StatusCode)
	}
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
