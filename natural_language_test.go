package jigsawstack_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/yudaprama/egent-jigsawstack"
	"github.com/yudaprama/egent-jigsawstack/internal/test"
)

func TestJigsawStack_Sentiment(t *testing.T) {
	if !test.IsIntegrationTest() {
		t.Skip("Skipping integration test")
	}
	a := assert.New(t)
	apiKey, err := test.GetAPIKey("JIGSAWSTACK_API_KEY")
	a.NoError(err)
	j, err := jigsawstack.NewJigsawStack(apiKey)
	a.NoError(err)
	resp, err := j.Sentiment(context.Background(), "I am a happy person")
	a.NoError(err)
	a.True(resp.Success)
	a.Equal(jigsawstack.EmotionHappiness, resp.Sentiment.Emotion)
	a.Equal("positive", resp.Sentiment.Sentiment)
}
