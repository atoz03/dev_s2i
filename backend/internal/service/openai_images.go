package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	openAIImagesGenerationsEndpoint = "/v1/images/generations"
	openAIImagesEditsEndpoint       = "/v1/images/edits"
	// /v1/images 转 /v1/responses image_generation 时使用的主模型。
	openAIImagesResponsesMainModel = "gpt-5.4-mini"
)

type OpenAIImagesCapability string

const (
	OpenAIImagesCapabilityBasic  OpenAIImagesCapability = "images-basic"
	OpenAIImagesCapabilityNative OpenAIImagesCapability = "images-native"
)

type OpenAIImagesUpload struct {
	FieldName   string
	FileName    string
	ContentType string
	Data        []byte
	Width       int
	Height      int
}

type OpenAIImagesRequest struct {
	Endpoint           string
	ContentType        string
	Multipart          bool
	Model              string
	ExplicitModel      bool
	Prompt             string
	Stream             bool
	N                  int
	Size               string
	Quality            string
	Background         string
	OutputFormat       string
	Moderation         string
	Style              string
	OutputCompression  *int
	PartialImages      *int
	InputImageURLs     []string
	MaskImageURL       string
	MaskUpload         *OpenAIImagesUpload
	ExplicitSize       bool
	SizeTier           string
	ResponseFormat     string
	HasMask            bool
	HasNativeOptions   bool
	RequiredCapability OpenAIImagesCapability
	Uploads            []OpenAIImagesUpload
	Body               []byte
	bodyHash           string
}

type openAIImageResponseItem struct {
	B64JSON       string `json:"b64_json"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

func (r *OpenAIImagesRequest) IsEdits() bool {
	return r != nil && r.Endpoint == openAIImagesEditsEndpoint
}

func (r *OpenAIImagesRequest) StickySessionSeed() string {
	if r == nil {
		return ""
	}
	parts := []string{
		"openai-images",
		strings.TrimSpace(r.Endpoint),
		strings.TrimSpace(r.Model),
		strings.TrimSpace(r.Size),
		strings.TrimSpace(r.Prompt),
	}
	seed := strings.Join(parts, "|")
	if strings.TrimSpace(r.Prompt) == "" && r.bodyHash != "" {
		seed += "|body=" + r.bodyHash
	}
	return seed
}

func (s *OpenAIGatewayService) ParseOpenAIImagesRequest(c *gin.Context, body []byte) (*OpenAIImagesRequest, error) {
	if c == nil || c.Request == nil {
		return nil, fmt.Errorf("missing request context")
	}
	endpoint := normalizeOpenAIImagesEndpointPath(c.Request.URL.Path)
	if endpoint == "" {
		return nil, fmt.Errorf("unsupported images endpoint")
	}

	contentType := strings.TrimSpace(c.GetHeader("Content-Type"))
	req := &OpenAIImagesRequest{
		Endpoint:    endpoint,
		ContentType: contentType,
		N:           1,
		Body:        body,
	}
	if len(body) > 0 {
		sum := sha256.Sum256(body)
		req.bodyHash = hex.EncodeToString(sum[:8])
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil && strings.EqualFold(mediaType, "multipart/form-data") {
		req.Multipart = true
		if parseErr := parseOpenAIImagesMultipartRequest(body, contentType, req); parseErr != nil {
			return nil, parseErr
		}
	} else {
		if len(body) == 0 {
			return nil, fmt.Errorf("request body is empty")
		}
		if !gjson.ValidBytes(body) {
			return nil, fmt.Errorf("failed to parse request body")
		}
		if parseErr := parseOpenAIImagesJSONRequest(body, req); parseErr != nil {
			return nil, parseErr
		}
	}

	applyOpenAIImagesDefaults(req)
	if err := validateOpenAIImagesModel(req.Model); err != nil {
		return nil, err
	}
	req.SizeTier = normalizeOpenAIImageSizeTier(req.Size)
	req.RequiredCapability = classifyOpenAIImagesCapability(req)
	return req, nil
}

func parseOpenAIImagesJSONRequest(body []byte, req *OpenAIImagesRequest) error {
	if modelResult := gjson.GetBytes(body, "model"); modelResult.Exists() {
		req.Model = strings.TrimSpace(modelResult.String())
		req.ExplicitModel = req.Model != ""
	}
	req.Prompt = strings.TrimSpace(gjson.GetBytes(body, "prompt").String())

	if streamResult := gjson.GetBytes(body, "stream"); streamResult.Exists() {
		if streamResult.Type != gjson.True && streamResult.Type != gjson.False {
			return fmt.Errorf("invalid stream field type")
		}
		req.Stream = streamResult.Bool()
	}

	if nResult := gjson.GetBytes(body, "n"); nResult.Exists() {
		if nResult.Type != gjson.Number {
			return fmt.Errorf("invalid n field type")
		}
		req.N = int(nResult.Int())
		if req.N <= 0 {
			return fmt.Errorf("n must be greater than 0")
		}
	}

	if sizeResult := gjson.GetBytes(body, "size"); sizeResult.Exists() {
		req.Size = strings.TrimSpace(sizeResult.String())
		req.ExplicitSize = req.Size != ""
	}
	req.Quality = strings.TrimSpace(gjson.GetBytes(body, "quality").String())
	req.Background = strings.TrimSpace(gjson.GetBytes(body, "background").String())
	req.OutputFormat = strings.TrimSpace(gjson.GetBytes(body, "output_format").String())
	req.Moderation = strings.TrimSpace(gjson.GetBytes(body, "moderation").String())
	req.Style = strings.TrimSpace(gjson.GetBytes(body, "style").String())
	if outputCompression := gjson.GetBytes(body, "output_compression"); outputCompression.Exists() && outputCompression.Type == gjson.Number {
		v := int(outputCompression.Int())
		req.OutputCompression = &v
	}
	if partialImages := gjson.GetBytes(body, "partial_images"); partialImages.Exists() && partialImages.Type == gjson.Number {
		v := int(partialImages.Int())
		req.PartialImages = &v
	}
	if image := gjson.GetBytes(body, "image"); image.Exists() {
		switch image.Type {
		case gjson.String:
			if trimmed := strings.TrimSpace(image.String()); trimmed != "" {
				req.InputImageURLs = append(req.InputImageURLs, trimmed)
			}
		case gjson.JSON:
			if image.IsArray() {
				for _, item := range image.Array() {
					if trimmed := strings.TrimSpace(item.String()); trimmed != "" {
						req.InputImageURLs = append(req.InputImageURLs, trimmed)
					}
				}
			}
		}
	}
	if mask := gjson.GetBytes(body, "mask"); mask.Exists() && mask.Type == gjson.String {
		req.MaskImageURL = strings.TrimSpace(mask.String())
	}
	req.ResponseFormat = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "response_format").String()))
	req.HasMask = gjson.GetBytes(body, "mask").Exists()
	req.HasNativeOptions = hasOpenAINativeImageOptions(func(path string) bool {
		return gjson.GetBytes(body, path).Exists()
	})
	return nil
}

func parseOpenAIImagesMultipartRequest(body []byte, contentType string, req *OpenAIImagesRequest) error {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return fmt.Errorf("invalid multipart content-type: %w", err)
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return fmt.Errorf("multipart boundary is required")
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read multipart body: %w", err)
		}
		name := strings.TrimSpace(part.FormName())
		if name == "" {
			_ = part.Close()
			continue
		}

		data, err := io.ReadAll(part)
		_ = part.Close()
		if err != nil {
			return fmt.Errorf("read multipart field %s: %w", name, err)
		}

		fileName := strings.TrimSpace(part.FileName())
		if fileName != "" {
			partContentType := strings.TrimSpace(part.Header.Get("Content-Type"))
			if name == "mask" && len(data) > 0 {
				req.HasMask = true
				maskUpload := OpenAIImagesUpload{
					FieldName:   name,
					FileName:    fileName,
					ContentType: partContentType,
					Data:        data,
				}
				req.MaskUpload = &maskUpload
			}
			if name == "image" || strings.HasPrefix(name, "image[") {
				width, height := parseOpenAIImageDimensions(part.Header)
				req.Uploads = append(req.Uploads, OpenAIImagesUpload{
					FieldName:   name,
					FileName:    fileName,
					ContentType: partContentType,
					Data:        data,
					Width:       width,
					Height:      height,
				})
			}
			continue
		}

		value := strings.TrimSpace(string(data))
		switch name {
		case "model":
			req.Model = value
			req.ExplicitModel = value != ""
		case "prompt":
			req.Prompt = value
		case "image":
			if value != "" {
				req.InputImageURLs = append(req.InputImageURLs, value)
			}
		case "mask":
			req.MaskImageURL = value
			if value != "" {
				req.HasMask = true
			}
		case "size":
			req.Size = value
			req.ExplicitSize = value != ""
		case "quality":
			req.Quality = value
		case "background":
			req.Background = value
		case "output_format":
			req.OutputFormat = value
		case "moderation":
			req.Moderation = value
		case "style":
			req.Style = value
		case "output_compression":
			if parsed, err := strconv.Atoi(value); err == nil {
				req.OutputCompression = &parsed
			}
		case "partial_images":
			if parsed, err := strconv.Atoi(value); err == nil {
				req.PartialImages = &parsed
			}
		case "response_format":
			req.ResponseFormat = strings.ToLower(value)
		case "stream":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid stream field value")
			}
			req.Stream = parsed
		case "n":
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return fmt.Errorf("n must be a positive integer")
			}
			req.N = n
		default:
			if isOpenAINativeImageOption(name) && value != "" {
				req.HasNativeOptions = true
			}
		}
	}

	if len(req.Uploads) == 0 && req.IsEdits() {
		return fmt.Errorf("image file is required")
	}
	return nil
}

func parseOpenAIImageDimensions(_ textproto.MIMEHeader) (int, int) {
	return 0, 0
}

func applyOpenAIImagesDefaults(req *OpenAIImagesRequest) {
	if req == nil {
		return
	}
	if req.N <= 0 {
		req.N = 1
	}
	if strings.TrimSpace(req.Model) != "" {
		req.Model = strings.TrimSpace(req.Model)
		return
	}
	req.Model = "gpt-image-2"
}

func isOpenAIImageGenerationModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-image-")
}

func validateOpenAIImagesModel(model string) error {
	model = strings.TrimSpace(model)
	if isOpenAIImageGenerationModel(model) {
		return nil
	}
	if model == "" {
		return fmt.Errorf("images endpoint requires an image model")
	}
	return fmt.Errorf("images endpoint requires an image model, got %q", model)
}

func normalizeOpenAIImagesEndpointPath(path string) string {
	trimmed := strings.TrimSpace(path)
	switch {
	case strings.Contains(trimmed, "/images/generations"):
		return openAIImagesGenerationsEndpoint
	case strings.Contains(trimmed, "/images/edits"):
		return openAIImagesEditsEndpoint
	default:
		return ""
	}
}

func classifyOpenAIImagesCapability(req *OpenAIImagesRequest) OpenAIImagesCapability {
	if req == nil {
		return OpenAIImagesCapabilityNative
	}
	if req.ExplicitModel || req.ExplicitSize {
		return OpenAIImagesCapabilityNative
	}
	model := strings.ToLower(strings.TrimSpace(req.Model))
	if !strings.HasPrefix(model, "gpt-image-") {
		return OpenAIImagesCapabilityNative
	}
	if req.Stream || req.N != 1 || req.HasMask || req.HasNativeOptions {
		return OpenAIImagesCapabilityNative
	}
	if req.IsEdits() && !req.Multipart {
		return OpenAIImagesCapabilityNative
	}
	if req.ResponseFormat != "" && req.ResponseFormat != "b64_json" {
		return OpenAIImagesCapabilityNative
	}
	return OpenAIImagesCapabilityBasic
}

func hasOpenAINativeImageOptions(exists func(path string) bool) bool {
	for _, path := range []string{
		"background",
		"quality",
		"style",
		"output_format",
		"output_compression",
		"moderation",
	} {
		if exists(path) {
			return true
		}
	}
	return false
}

func isOpenAINativeImageOption(name string) bool {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "background", "quality", "style", "output_format", "output_compression", "moderation":
		return true
	default:
		return false
	}
}

func normalizeOpenAIImageSizeTier(size string) string {
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "1024x1024":
		return "1K"
	case "3840x2160", "2160x3840":
		return "4K"
	case "1536x1024", "1024x1536", "1792x1024", "1024x1792", "", "auto":
		return "2K"
	default:
		return "2K"
	}
}

func (s *OpenAIGatewayService) ForwardImages(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	_ []byte,
	parsed *OpenAIImagesRequest,
	channelMappedModel string,
) (*OpenAIForwardResult, error) {
	if parsed == nil {
		return nil, fmt.Errorf("parsed images request is required")
	}
	// 合规要求：图片请求统一走 /responses image_generation 链路。
	return s.forwardOpenAIImagesViaResponses(ctx, c, account, parsed, channelMappedModel)
}

func shouldUseOpenAIResponsesForImageGeneration(parsed *OpenAIImagesRequest) bool {
	return parsed != nil
}

func (s *OpenAIGatewayService) forwardOpenAIImagesViaResponses(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *OpenAIImagesRequest,
	channelMappedModel string,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()
	requestModel := strings.TrimSpace(parsed.Model)
	if mapped := strings.TrimSpace(channelMappedModel); mapped != "" {
		requestModel = mapped
	}
	upstreamModel := account.GetMappedModel(requestModel)
	requestBody, err := buildOpenAIImageGenerationResponsesBody(parsed, upstreamModel)
	if err != nil {
		return nil, err
	}
	setOpsUpstreamRequestBody(c, requestBody)

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	upstreamReq, err := s.buildUpstreamRequestOpenAIPassthrough(ctx, c, account, requestBody, token)
	if err != nil {
		return nil, err
	}
	upstreamReq.Header.Set("accept", "text/event-stream")
	upstreamReq.Header.Set("content-type", "application/json")

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	upstreamStart := time.Now()
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
	if err != nil {
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: 0,
			UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
			Kind:               "request_error",
			Message:            safeErr,
		})
		return nil, fmt.Errorf("upstream request failed: %s", safeErr)
	}
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
		if s.shouldFailoverOpenAIUpstreamResponse(resp.StatusCode, upstreamMsg, respBody) {
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
				Kind:               "failover",
				Message:            upstreamMsg,
			})
			s.handleFailoverSideEffects(ctx, resp, account)
			return nil, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				RetryableOnSameAccount: account.IsPoolMode() && isPoolModeRetryableStatus(resp.StatusCode),
			}
		}
		return s.handleErrorResponse(ctx, resp, c, account, requestBody)
	}
	defer func() { _ = resp.Body.Close() }()

	usage, imageCount, firstTokenMs, err := s.handleOpenAIImagesResponsesResult(resp, c, startTime)
	if err != nil {
		return nil, err
	}
	return &OpenAIForwardResult{
		RequestID:       resp.Header.Get("x-request-id"),
		Usage:           usage,
		Model:           requestModel,
		UpstreamModel:   upstreamModel,
		Stream:          false,
		ResponseHeaders: resp.Header.Clone(),
		Duration:        time.Since(startTime),
		FirstTokenMs:    firstTokenMs,
		ImageCount:      imageCount,
		ImageSize:       parsed.SizeTier,
	}, nil
}

func buildOpenAIImageGenerationResponsesBody(parsed *OpenAIImagesRequest, model string) ([]byte, error) {
	if parsed == nil {
		return nil, fmt.Errorf("parsed images request is required")
	}
	prompt := strings.TrimSpace(parsed.Prompt)
	if prompt == "" {
		if parsed.IsEdits() {
			prompt = "Edit this image."
		} else {
			prompt = "Generate an image."
		}
	}
	input := buildOpenAIImageGenerationResponsesInput(parsed, prompt)
	payload := map[string]any{
		"model": model,
		"input": input,
		"tools": []map[string]any{
			{
				"type":          "image_generation",
				"output_format": "png",
			},
		},
		"instructions": "",
		"tool_choice":  "auto",
		"stream":       true,
		"store":        false,
	}
	return json.Marshal(payload)
}

func buildOpenAIImageGenerationResponsesInput(parsed *OpenAIImagesRequest, prompt string) []map[string]any {
	if parsed == nil || !parsed.IsEdits() || len(parsed.Uploads) == 0 {
		return []map[string]any{
			{
				"role":    "user",
				"content": prompt,
			},
		}
	}

	content := make([]map[string]any, 0, len(parsed.Uploads)+1)
	content = append(content, map[string]any{
		"type": "input_text",
		"text": prompt,
	})
	for _, upload := range parsed.Uploads {
		dataURL := buildOpenAIInputImageDataURL(upload)
		if dataURL == "" {
			continue
		}
		content = append(content, map[string]any{
			"type":      "input_image",
			"image_url": dataURL,
		})
	}
	if len(content) == 1 {
		// 兜底：保证至少保留文本，避免构造空图输入导致 400。
		return []map[string]any{
			{
				"role":    "user",
				"content": prompt,
			},
		}
	}
	return []map[string]any{
		{
			"role":    "user",
			"content": content,
		},
	}
}

func buildOpenAIInputImageDataURL(upload OpenAIImagesUpload) string {
	if len(upload.Data) == 0 {
		return ""
	}
	contentType := normalizeOpenAIUploadImageContentType(upload)
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(upload.Data)
}

func normalizeOpenAIUploadImageContentType(upload OpenAIImagesUpload) string {
	contentType := strings.ToLower(strings.TrimSpace(upload.ContentType))
	if strings.HasPrefix(contentType, "image/") {
		return contentType
	}
	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(upload.FileName)))
	if ext != "" {
		if guessed := strings.ToLower(strings.TrimSpace(mime.TypeByExtension(ext))); strings.HasPrefix(guessed, "image/") {
			return guessed
		}
	}
	return "image/png"
}

func (s *OpenAIGatewayService) handleOpenAIImagesResponsesResult(
	resp *http.Response,
	c *gin.Context,
	startTime time.Time,
) (OpenAIUsage, int, *int, error) {
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return OpenAIUsage{}, 0, nil, err
	}

	items, usage, firstTokenMs, err := parseOpenAIImagesFromResponsesBody(body, startTime)
	if err != nil {
		return OpenAIUsage{}, 0, firstTokenMs, err
	}

	responseBody, imageCount, err := buildOpenAIImagesResponsePayload(items)
	if err != nil {
		return OpenAIUsage{}, 0, firstTokenMs, err
	}

	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	c.Data(resp.StatusCode, "application/json; charset=utf-8", responseBody)
	return usage, imageCount, firstTokenMs, nil
}

func parseOpenAIImagesFromResponsesBody(body []byte, startTime time.Time) ([]openAIImageResponseItem, OpenAIUsage, *int, error) {
	if len(body) == 0 {
		return nil, OpenAIUsage{}, nil, fmt.Errorf("upstream responses returned empty body")
	}
	bodyText := string(body)
	looksLikeSSE := strings.Contains(bodyText, "\ndata:") || strings.HasPrefix(bodyText, "data:")

	usage := OpenAIUsage{}
	items := make([]openAIImageResponseItem, 0, 2)
	var firstTokenMs *int

	if looksLikeSSE {
		lines := strings.Split(bodyText, "\n")
		for _, line := range lines {
			data, ok := extractOpenAISSEDataLine(strings.TrimRight(line, "\r"))
			if !ok || data == "" || data == "[DONE]" {
				continue
			}
			if firstTokenMs == nil {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}
			dataBytes := []byte(data)
			mergeOpenAIUsage(&usage, dataBytes)
			items = append(items, collectOpenAIImageResponseItemsFromPayload(dataBytes)...)
		}
		// 对某些上游，仅在 response.completed.response.output 中包含最终图片结果。
		if len(items) == 0 {
			if finalResponse, ok := extractCodexFinalResponse(bodyText); ok {
				mergeOpenAIUsage(&usage, finalResponse)
				items = append(items, collectOpenAIImageResponseItemsFromPayload(finalResponse)...)
			}
		}
	} else {
		mergeOpenAIUsage(&usage, body)
		items = append(items, collectOpenAIImageResponseItemsFromPayload(body)...)
	}

	items = dedupeOpenAIImageResponseItems(items)
	if len(items) == 0 {
		return nil, usage, firstTokenMs, fmt.Errorf("upstream responses returned no image output")
	}
	return items, usage, firstTokenMs, nil
}

func collectOpenAIImageResponseItemsFromPayload(payload []byte) []openAIImageResponseItem {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return nil
	}
	out := make([]openAIImageResponseItem, 0, 2)

	appendFromOutput := func(output gjson.Result) {
		if !output.Exists() || !output.IsArray() {
			return
		}
		for _, item := range output.Array() {
			out = append(out, collectOpenAIImageResponseItemsFromOutputItem(item)...)
		}
	}

	if item := gjson.GetBytes(payload, "item"); item.Exists() {
		out = append(out, collectOpenAIImageResponseItemsFromOutputItem(item)...)
	}
	appendFromOutput(gjson.GetBytes(payload, "response.output"))
	appendFromOutput(gjson.GetBytes(payload, "output"))
	return out
}

func collectOpenAIImageResponseItemsFromOutputItem(item gjson.Result) []openAIImageResponseItem {
	if !item.Exists() {
		return nil
	}
	itemType := strings.TrimSpace(item.Get("type").String())
	revisedPrompt := strings.TrimSpace(item.Get("revised_prompt").String())
	switch itemType {
	case "image_generation_call":
		return collectOpenAIImageResponseItemsFromValue(item.Get("result"), revisedPrompt)
	case "message":
		content := item.Get("content")
		if !content.Exists() || !content.IsArray() {
			return nil
		}
		out := make([]openAIImageResponseItem, 0, len(content.Array()))
		for _, contentItem := range content.Array() {
			contentType := strings.TrimSpace(contentItem.Get("type").String())
			if contentType != "output_image" && contentType != "image" {
				continue
			}
			if revisedPrompt == "" {
				revisedPrompt = strings.TrimSpace(contentItem.Get("revised_prompt").String())
			}
			out = append(out, collectOpenAIImageResponseItemsFromValue(contentItem.Get("b64_json"), revisedPrompt)...)
			out = append(out, collectOpenAIImageResponseItemsFromValue(contentItem.Get("image_url"), revisedPrompt)...)
		}
		return out
	default:
		return nil
	}
}

func collectOpenAIImageResponseItemsFromValue(value gjson.Result, revisedPrompt string) []openAIImageResponseItem {
	if !value.Exists() {
		return nil
	}
	switch value.Type {
	case gjson.String:
		raw := strings.TrimSpace(value.String())
		if raw == "" {
			return nil
		}
		if decoded, ok := decodeOpenAIImageDataURL(raw); ok {
			return []openAIImageResponseItem{{B64JSON: decoded, RevisedPrompt: revisedPrompt}}
		}
		return []openAIImageResponseItem{{B64JSON: raw, RevisedPrompt: revisedPrompt}}
	case gjson.JSON:
		if value.IsArray() {
			out := make([]openAIImageResponseItem, 0, len(value.Array()))
			for _, item := range value.Array() {
				out = append(out, collectOpenAIImageResponseItemsFromValue(item, revisedPrompt)...)
			}
			return out
		}
		out := make([]openAIImageResponseItem, 0, 1)
		for _, path := range []string{"b64_json", "image_base64", "base64"} {
			out = append(out, collectOpenAIImageResponseItemsFromValue(value.Get(path), revisedPrompt)...)
		}
		out = append(out, collectOpenAIImageResponseItemsFromValue(value.Get("url"), revisedPrompt)...)
		out = append(out, collectOpenAIImageResponseItemsFromValue(value.Get("image_url"), revisedPrompt)...)
		out = append(out, collectOpenAIImageResponseItemsFromValue(value.Get("data"), revisedPrompt)...)
		return out
	default:
		return nil
	}
}

func decodeOpenAIImageDataURL(raw string) (string, bool) {
	lowerRaw := strings.ToLower(strings.TrimSpace(raw))
	if !strings.HasPrefix(lowerRaw, "data:image/") {
		return "", false
	}
	idx := strings.Index(raw, ",")
	if idx < 0 || idx+1 >= len(raw) {
		return "", false
	}
	meta := strings.ToLower(raw[:idx])
	if !strings.Contains(meta, ";base64") {
		return "", false
	}
	return raw[idx+1:], true
}

func dedupeOpenAIImageResponseItems(items []openAIImageResponseItem) []openAIImageResponseItem {
	if len(items) <= 1 {
		return items
	}
	seen := make(map[string]int, len(items))
	out := make([]openAIImageResponseItem, 0, len(items))
	for _, item := range items {
		item.B64JSON = strings.TrimSpace(item.B64JSON)
		item.RevisedPrompt = strings.TrimSpace(item.RevisedPrompt)
		if item.B64JSON == "" {
			continue
		}
		if idx, exists := seen[item.B64JSON]; exists {
			// 同一张图重复出现时，优先保留带 revised_prompt 的版本。
			if out[idx].RevisedPrompt == "" && item.RevisedPrompt != "" {
				out[idx].RevisedPrompt = item.RevisedPrompt
			}
			continue
		}
		seen[item.B64JSON] = len(out)
		out = append(out, item)
	}
	return out
}

func buildOpenAIImagesResponsePayload(items []openAIImageResponseItem) ([]byte, int, error) {
	normalized := make([]openAIImageResponseItem, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.B64JSON) == "" {
			continue
		}
		normalized = append(normalized, item)
	}
	if len(normalized) == 0 {
		return nil, 0, fmt.Errorf("no image content found in responses payload")
	}
	payload := map[string]any{
		"created": time.Now().Unix(),
		"data":    normalized,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	return body, len(normalized), nil
}

func mergeOpenAIUsage(dst *OpenAIUsage, body []byte) {
	if dst == nil || len(body) == 0 || !gjson.ValidBytes(body) {
		return
	}
	if v := int(gjson.GetBytes(body, "usage.input_tokens").Int()); v > 0 {
		dst.InputTokens = v
	}
	if v := int(gjson.GetBytes(body, "response.usage.input_tokens").Int()); v > 0 {
		dst.InputTokens = v
	}
	if v := int(gjson.GetBytes(body, "usage.output_tokens").Int()); v > 0 {
		dst.OutputTokens = v
	}
	if v := int(gjson.GetBytes(body, "response.usage.output_tokens").Int()); v > 0 {
		dst.OutputTokens = v
	}
	if v := int(gjson.GetBytes(body, "usage.input_tokens_details.cached_tokens").Int()); v > 0 {
		dst.CacheReadInputTokens = v
	}
	if v := int(gjson.GetBytes(body, "response.usage.input_tokens_details.cached_tokens").Int()); v > 0 {
		dst.CacheReadInputTokens = v
	}
	if v := int(gjson.GetBytes(body, "usage.output_tokens_details.image_tokens").Int()); v > 0 {
		dst.ImageOutputTokens = v
	}
	if v := int(gjson.GetBytes(body, "response.usage.output_tokens_details.image_tokens").Int()); v > 0 {
		dst.ImageOutputTokens = v
	}
}
