package service

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_JSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","size":"1024x1024","quality":"high","stream":true}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "/v1/images/generations", parsed.Endpoint)
	require.Equal(t, "gpt-image-2", parsed.Model)
	require.Equal(t, "draw a cat", parsed.Prompt)
	require.True(t, parsed.Stream)
	require.Equal(t, "1024x1024", parsed.Size)
	require.Equal(t, "1K", parsed.SizeTier)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
	require.False(t, parsed.Multipart)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_MultipartEdit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "replace background"))
	require.NoError(t, writer.WriteField("size", "1536x1024"))
	part, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("fake-image-bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body.Bytes())
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "/v1/images/edits", parsed.Endpoint)
	require.True(t, parsed.Multipart)
	require.Equal(t, "gpt-image-2", parsed.Model)
	require.Equal(t, "replace background", parsed.Prompt)
	require.Equal(t, "1536x1024", parsed.Size)
	require.Equal(t, "2K", parsed.SizeTier)
	require.Len(t, parsed.Uploads, 1)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_PromptOnlyDefaultsRemainBasic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"prompt":"draw a cat"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "gpt-image-2", parsed.Model)
	require.Equal(t, OpenAIImagesCapabilityBasic, parsed.RequiredCapability)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_ExplicitSizeRequiresNativeCapability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"prompt":"draw a cat","size":"1024x1024"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
}

func TestShouldUseOpenAIResponsesForImageGeneration(t *testing.T) {
	require.True(t, shouldUseOpenAIResponsesForImageGeneration(&OpenAIImagesRequest{
		Endpoint:           openAIImagesGenerationsEndpoint,
		RequiredCapability: OpenAIImagesCapabilityBasic,
	}))
	require.True(t, shouldUseOpenAIResponsesForImageGeneration(&OpenAIImagesRequest{
		Endpoint:           openAIImagesEditsEndpoint,
		Uploads:            []OpenAIImagesUpload{{FileName: "edit.png", Data: []byte("abc")}},
		RequiredCapability: OpenAIImagesCapabilityBasic,
	}))
	require.False(t, shouldUseOpenAIResponsesForImageGeneration(&OpenAIImagesRequest{
		Endpoint:           openAIImagesEditsEndpoint,
		RequiredCapability: OpenAIImagesCapabilityBasic,
	}))
	require.False(t, shouldUseOpenAIResponsesForImageGeneration(&OpenAIImagesRequest{
		Endpoint:           openAIImagesGenerationsEndpoint,
		RequiredCapability: OpenAIImagesCapabilityNative,
	}))
}

func TestBuildOpenAIImageGenerationResponsesBody(t *testing.T) {
	body, err := buildOpenAIImageGenerationResponsesBody(&OpenAIImagesRequest{
		Model:  "gpt-image-2",
		Prompt: "画一只猫",
	}, "gpt-5.4")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4", gjson.GetBytes(body, "model").String())
	require.Equal(t, "画一只猫", gjson.GetBytes(body, "input.0.content").String())
	require.Equal(t, "image_generation", gjson.GetBytes(body, "tools.0.type").String())
	require.Equal(t, "png", gjson.GetBytes(body, "tools.0.output_format").String())
	require.True(t, gjson.GetBytes(body, "stream").Bool())
	require.False(t, gjson.GetBytes(body, "store").Bool())
}

func TestBuildOpenAIImageGenerationResponsesBody_EditsInputImages(t *testing.T) {
	body, err := buildOpenAIImageGenerationResponsesBody(&OpenAIImagesRequest{
		Endpoint:  openAIImagesEditsEndpoint,
		Multipart: true,
		Prompt:    "把背景改成雪山",
		Uploads: []OpenAIImagesUpload{
			{
				FileName:    "source.jpg",
				ContentType: "image/jpeg",
				Data:        []byte("ABC"),
			},
			{
				FileName: "secondary.png",
				Data:     []byte("DEF"),
			},
		},
	}, "gpt-5.4")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4", gjson.GetBytes(body, "model").String())
	require.Equal(t, "input_text", gjson.GetBytes(body, "input.0.content.0.type").String())
	require.Equal(t, "把背景改成雪山", gjson.GetBytes(body, "input.0.content.0.text").String())
	require.Equal(t, "input_image", gjson.GetBytes(body, "input.0.content.1.type").String())
	require.Equal(t, "data:image/jpeg;base64,QUJD", gjson.GetBytes(body, "input.0.content.1.image_url").String())
	require.Equal(t, "data:image/png;base64,REVG", gjson.GetBytes(body, "input.0.content.2.image_url").String())
}

func TestParseOpenAIImagesFromResponsesBody_SSE(t *testing.T) {
	body := []byte(
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"QUJD\",\"revised_prompt\":\"cat\"}}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":5,\"output_tokens_details\":{\"image_tokens\":8}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"QUJD\"}]}}\n\n" +
			"data: [DONE]\n\n",
	)
	items, usage, firstTokenMs, err := parseOpenAIImagesFromResponsesBody(body, time.Now().Add(-time.Second))
	require.NoError(t, err)
	require.NotNil(t, firstTokenMs)
	require.Len(t, items, 1)
	require.Equal(t, "QUJD", items[0].B64JSON)
	require.Equal(t, "cat", items[0].RevisedPrompt)
	require.Equal(t, 3, usage.InputTokens)
	require.Equal(t, 5, usage.OutputTokens)
	require.Equal(t, 8, usage.ImageOutputTokens)
}

func TestParseOpenAIImagesFromResponsesBody_DataURL(t *testing.T) {
	body := []byte(`{
		"response": {
			"usage": {
				"input_tokens": 1,
				"output_tokens": 2
			},
			"output": [
				{
					"type": "message",
					"content": [
						{
							"type": "output_image",
							"image_url": "data:image/png;base64,REVG",
							"revised_prompt": "otter"
						}
					]
				}
			]
		}
	}`)
	items, usage, firstTokenMs, err := parseOpenAIImagesFromResponsesBody(body, time.Now())
	require.NoError(t, err)
	require.Nil(t, firstTokenMs)
	require.Len(t, items, 1)
	require.Equal(t, "REVG", items[0].B64JSON)
	require.Equal(t, "otter", items[0].RevisedPrompt)
	require.Equal(t, 1, usage.InputTokens)
	require.Equal(t, 2, usage.OutputTokens)
}

func TestOpenAIGatewayServiceParseOpenAIImagesRequest_RejectsNonImageModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","prompt":"draw a cat"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.Nil(t, parsed)
	require.ErrorContains(t, err, `images endpoint requires an image model, got "gpt-5.4"`)
}

func TestCollectOpenAIImagePointers_RecognizesDirectAssets(t *testing.T) {
	items := collectOpenAIImagePointers([]byte(`{
		"revised_prompt": "cat astronaut",
		"parts": [
			{"b64_json":"QUJD"},
			{"download_url":"https://files.example.com/image.png?sig=1"},
			{"asset_pointer":"file-service://file_123"}
		]
	}`))

	require.Len(t, items, 3)
	var sawBase64, sawURL, sawPointer bool
	for _, item := range items {
		if item.B64JSON == "QUJD" {
			sawBase64 = true
			require.Equal(t, "cat astronaut", item.Prompt)
		}
		if item.DownloadURL == "https://files.example.com/image.png?sig=1" {
			sawURL = true
		}
		if item.Pointer == "file-service://file_123" {
			sawPointer = true
		}
	}
	require.True(t, sawBase64)
	require.True(t, sawURL)
	require.True(t, sawPointer)
}

func TestResolveOpenAIImageBytes_PrefersInlineBase64(t *testing.T) {
	data, err := resolveOpenAIImageBytes(context.Background(), nil, nil, "", openAIImagePointerInfo{
		B64JSON: "data:image/png;base64,QUJD",
	})
	require.NoError(t, err)
	require.Equal(t, []byte("ABC"), data)
}

func TestMergeOpenAIImagePointerInfos_Base64UsesFullHashIdentity(t *testing.T) {
	prefix := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	a := prefix + "BBBB"
	b := prefix + "CCCC"

	merged := mergeOpenAIImagePointerInfos(nil, []openAIImagePointerInfo{
		{B64JSON: a, Prompt: "first"},
		{B64JSON: b, Prompt: "second"},
	})

	require.Len(t, merged, 2)
}
