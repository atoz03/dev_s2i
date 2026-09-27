package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripOpenAILegacyResponsesBeta(t *testing.T) {
	headers := make(http.Header)
	headers.Add("OpenAI-Beta", "responses_multi_agent=v1, responses=experimental")
	headers.Add("OpenAI-Beta", "future_feature=v2")

	stripOpenAILegacyResponsesBeta(headers)

	require.Equal(t, []string{"responses_multi_agent=v1", "future_feature=v2"}, headers.Values("OpenAI-Beta"))
}
