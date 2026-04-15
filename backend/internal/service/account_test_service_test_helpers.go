//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
)

func newSoraTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	return ctx, recorder
}

func newJSONResponse(status int, body string) *http.Response {
	respBody := body
	if strings.TrimSpace(respBody) == "" {
		respBody = "{}"
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(respBody)),
	}
}

type queuedHTTPUpstream struct {
	responses []*http.Response
}

func (q *queuedHTTPUpstream) next() *http.Response {
	if len(q.responses) == 0 {
		return newJSONResponse(http.StatusOK, "{}")
	}
	resp := q.responses[0]
	q.responses = q.responses[1:]
	return resp
}

func (q *queuedHTTPUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return q.next(), nil
}

func (q *queuedHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return q.Do(req, proxyURL, accountID, accountConcurrency)
}

var _ HTTPUpstream = (*queuedHTTPUpstream)(nil)
