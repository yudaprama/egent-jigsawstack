// Package agent wires the JigsawStack SDK into an Eino ChatModelAgent.
//
// Only read, transform, and generation APIs are exposed as tools. Stateful
// storage, KV, and prompt-management endpoints are intentionally excluded.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	jigsawstack "github.com/yudaprama/egent-jigsawstack"
)

// SystemPrompt is the default instruction for the JigsawStack agent.
const SystemPrompt = `You are egent_jigsawstack, a multimodal utility agent.

You route user requests to the right JigsawStack tool. Pick exactly one tool per
turn unless the user clearly needs more. If a request does not fit any tool,
answer from general knowledge instead of inventing tool calls.

Tool families available to you:
- vocr: visual OCR and image understanding
- object_detection: detect objects in an image
- image_generation: generate an image from a text prompt
- web_search: web search (returns results and an AI overview)
- web_search_suggest: search autocomplete suggestions
- translate: translate text between languages
- summarize: summarize text (max 5000 chars)
- sentiment: sentiment and emotion analysis
- tts: text-to-speech (returns an MP3 payload)
- text_to_sql: convert a natural-language question to SQL given a schema
- geography_search: forward search for places and points of interest

Tool-use rules:
- For image tasks, require a public image URL from the user.
- For tts, prefer an explicit accent; fall back to a neutral English accent.
- Always surface tool results as concise summaries, not raw blobs.
- Do not invent missing arguments. Ask the user when required inputs are absent.
`

// NewAgent builds the JigsawStack Eino agent. apiKey, baseURL, model, and
// modelAPIKey are resolved from the environment if empty.
func NewAgent(ctx context.Context, apiKey, baseURL, modelName, modelAPIKey string) (adk.Agent, error) {
	if apiKey == "" {
		apiKey = os.Getenv("JIGSAWSTACK_API_KEY")
	}
	if apiKey == "" {
		return nil, errors.New("JIGSAWSTACK_API_KEY is required")
	}

	gateway := os.Getenv("PLANO_LLM_GATEWAY")
	if baseURL == "" {
		baseURL = gateway
	}
	if baseURL == "" {
		baseURL = os.Getenv("MODEL_BASE_URL")
		if baseURL == "" {
			baseURL = "https://openrouter.ai/api/v1"
		}
	}
	if modelAPIKey == "" {
		modelAPIKey = os.Getenv("MODEL_API_KEY")
	}
	if modelAPIKey == "" && gateway == "" {
		modelAPIKey = os.Getenv("OPENROUTER_API_KEY")
	}
	if modelAPIKey == "" {
		// Plano gateway validates the real key via Talos.
		modelAPIKey = "EMPTY"
	}
	if modelName == "" {
		modelName = os.Getenv("MODEL_NAME")
		if modelName == "" {
			modelName = "nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free"
		}
	}

	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL: baseURL,
		Model:   modelName,
		APIKey:  modelAPIKey,
	})
	if err != nil {
		return nil, fmt.Errorf("create chat model: %w", err)
	}

	tools, err := BuildTools(ctx, apiKey)
	if err != nil {
		return nil, fmt.Errorf("build jigsawstack tools: %w", err)
	}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "JigsawStackAgent",
		Description: "Multimodal utility agent backed by JigsawStack read/transform/generate APIs",
		Instruction: SystemPrompt,
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: tools,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}
	return agent, nil
}

// NewRunner wraps the agent in an Eino Runner.
func NewRunner(ctx context.Context, agent adk.Agent) *adk.Runner {
	return adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
}

// BuildTools constructs the allowlisted JigsawStack Eino tools.
func BuildTools(ctx context.Context, apiKey string) ([]tool.BaseTool, error) {
	client, err := jigsawstack.NewJigsawStack(apiKey)
	if err != nil {
		return nil, err
	}

	tools := make([]tool.BaseTool, 0, 11)

	type vocrIn struct {
		Prompt  string `json:"prompt" jsonschema_description:"What to extract or answer about the image"`
		ImageURL string `json:"image_url" jsonschema_description:"Public URL of the image to analyze"`
	}
	vocr, err := utils.InferTool("vocr", "Visual OCR and image understanding. Provide an image URL and a prompt describing what to extract.",
		func(ctx context.Context, in *vocrIn) (string, error) {
			if in == nil || in.ImageURL == "" {
				return "", errors.New("image_url is required")
			}
			out, err := client.VOCR(ctx, in.Prompt, jigsawstack.WithURL(in.ImageURL))
			if err != nil {
				return "", err
			}
			return marshalJSON(map[string]any{"success": true, "context": out})
		})
	if err != nil {
		return nil, fmt.Errorf("build vocr tool: %w", err)
	}
	tools = append(tools, vocr)

	type objectIn struct {
		ImageURL string `json:"image_url" jsonschema_description:"Public URL of the image to analyze"`
	}
	objDet, err := utils.InferTool("object_detection", "Detect objects in an image. Returns detected object names with confidence scores.",
		func(ctx context.Context, in *objectIn) (string, error) {
			if in == nil || in.ImageURL == "" {
				return "", errors.New("image_url is required")
			}
			out, err := client.VisionObjectDetection(ctx, jigsawstack.NewVisionRequest(in.ImageURL))
			if err != nil {
				return "", err
			}
			return out, nil
		})
	if err != nil {
		return nil, fmt.Errorf("build object_detection tool: %w", err)
	}
	tools = append(tools, objDet)

	type imageGenIn struct {
		Prompt string `json:"prompt" jsonschema_description:"Text prompt describing the image to generate"`
		Width  int    `json:"width,omitempty" jsonschema_description:"Image width in pixels (optional, defaults to provider default)"`
		Height int    `json:"height,omitempty" jsonschema_description:"Image height in pixels (optional, defaults to provider default)"`
	}
	imageGen, err := utils.InferTool("image_generation", "Generate an image from a text prompt using Stable Diffusion variants.",
		func(ctx context.Context, in *imageGenIn) (string, error) {
			if in == nil || strings.TrimSpace(in.Prompt) == "" {
				return "", errors.New("prompt is required")
			}
			req := jigsawstack.ImageGenerationRequest{
				Prompt: in.Prompt,
				Width:  in.Width,
				Height: in.Height,
			}
			resp, err := client.ImageGeneration(ctx, req)
			if err != nil {
				return "", err
			}
			return marshalJSON(resp)
		})
	if err != nil {
		return nil, fmt.Errorf("build image_generation tool: %w", err)
	}
	tools = append(tools, imageGen)

	type webSearchIn struct {
		Query string `json:"query" jsonschema_description:"Search query"`
	}
	webSearch, err := utils.InferTool("web_search", "Web search. Returns ranked results and an AI overview.",
		func(ctx context.Context, in *webSearchIn) (string, error) {
			if in == nil || strings.TrimSpace(in.Query) == "" {
				return "", errors.New("query is required")
			}
			resp, err := client.WebSearch(ctx, in.Query)
			if err != nil {
				return "", err
			}
			return marshalJSON(resp)
		})
	if err != nil {
		return nil, fmt.Errorf("build web_search tool: %w", err)
	}
	tools = append(tools, webSearch)

	webSuggest, err := utils.InferTool("web_search_suggest", "Search autocomplete suggestions for a query prefix.",
		func(ctx context.Context, in *webSearchIn) (string, error) {
			if in == nil || strings.TrimSpace(in.Query) == "" {
				return "", errors.New("query is required")
			}
			resp, err := client.WebSearchSuggestions(ctx, in.Query)
			if err != nil {
				return "", err
			}
			return marshalJSON(resp)
		})
	if err != nil {
		return nil, fmt.Errorf("build web_search_suggest tool: %w", err)
	}
	tools = append(tools, webSuggest)

	type translateIn struct {
		Text            string `json:"text" jsonschema_description:"Text to translate (max 5000 chars)"`
		CurrentLanguage string `json:"current_language" jsonschema_description:"ISO code of the source language, e.g. en"`
		TargetLanguage  string `json:"target_language" jsonschema_description:"ISO code of the target language, e.g. id"`
	}
	translate, err := utils.InferTool("translate", "Translate text between languages (max 5000 chars).",
		func(ctx context.Context, in *translateIn) (string, error) {
			if in == nil || in.Text == "" {
				return "", errors.New("text is required")
			}
			if in.TargetLanguage == "" {
				return "", errors.New("target_language is required")
			}
			resp, err := client.Translate(ctx, jigsawstack.TranslateRequest{
				Text:            in.Text,
				CurrentLanguage: jigsawstack.Language(in.CurrentLanguage),
				TargetLanguage:  jigsawstack.Language(in.TargetLanguage),
			})
			if err != nil {
				return "", err
			}
			return marshalJSON(resp)
		})
	if err != nil {
		return nil, fmt.Errorf("build translate tool: %w", err)
	}
	tools = append(tools, translate)

	type summarizeIn struct {
		Text string `json:"text" jsonschema_description:"Text to summarize (max 5000 chars)"`
	}
	summarize, err := utils.InferTool("summarize", "Summarize text (max 5000 chars).",
		func(ctx context.Context, in *summarizeIn) (string, error) {
			if in == nil || strings.TrimSpace(in.Text) == "" {
				return "", errors.New("text is required")
			}
			resp, err := client.Summarize(ctx, jigsawstack.SummaryRequest{Text: in.Text})
			if err != nil {
				return "", err
			}
			return marshalJSON(resp)
		})
	if err != nil {
		return nil, fmt.Errorf("build summarize tool: %w", err)
	}
	tools = append(tools, summarize)

	type sentimentIn struct {
		Text string `json:"text" jsonschema_description:"Text to analyze for sentiment and emotion"`
	}
	sentiment, err := utils.InferTool("sentiment", "Sentiment and emotion analysis for text.",
		func(ctx context.Context, in *sentimentIn) (string, error) {
			if in == nil || strings.TrimSpace(in.Text) == "" {
				return "", errors.New("text is required")
			}
			resp, err := client.Sentiment(ctx, in.Text)
			if err != nil {
				return "", err
			}
			return marshalJSON(resp)
		})
	if err != nil {
		return nil, fmt.Errorf("build sentiment tool: %w", err)
	}
	tools = append(tools, sentiment)

	type ttsIn struct {
		Text   string `json:"text" jsonschema_description:"Text to convert to speech"`
		Accent string `json:"accent,omitempty" jsonschema_description:"Optional speaker accent (e.g. en-US-male-1)"`
	}
	tts, err := utils.InferTool("tts", "Convert text to speech and return an MP3 payload reference.",
		func(ctx context.Context, in *ttsIn) (string, error) {
			if in == nil || strings.TrimSpace(in.Text) == "" {
				return "", errors.New("text is required")
			}
			opts := []jigsawstack.TTSOption{}
			if in.Accent != "" {
				opts = append(opts, jigsawstack.WithAccent(in.Accent))
			}
			resp, err := client.AudioTTS(ctx, in.Text, opts...)
			if err != nil {
				return "", err
			}
			return marshalJSON(map[string]any{
				"success":  true,
				"bytes":    len(resp),
				"payload":  "mp3",
			})
		})
	if err != nil {
		return nil, fmt.Errorf("build tts tool: %w", err)
	}
	tools = append(tools, tts)

	type sqlIn struct {
		Prompt string `json:"prompt" jsonschema_description:"Natural-language description of the desired query"`
		Schema string `json:"sql_schema" jsonschema_description:"DDL or schema description for the target database"`
	}
	textToSQL, err := utils.InferTool("text_to_sql", "Convert a natural-language prompt into SQL using a provided schema.",
		func(ctx context.Context, in *sqlIn) (string, error) {
			if in == nil || strings.TrimSpace(in.Prompt) == "" {
				return "", errors.New("prompt is required")
			}
			if strings.TrimSpace(in.Schema) == "" {
				return "", errors.New("sql_schema is required")
			}
			resp, err := client.TextToSQL(ctx, in.Prompt, in.Schema)
			if err != nil {
				return "", err
			}
			return marshalJSON(resp)
		})
	if err != nil {
		return nil, fmt.Errorf("build text_to_sql tool: %w", err)
	}
	tools = append(tools, textToSQL)

	type geoIn struct {
		Query   string `json:"query" jsonschema_description:"Place name or point-of-interest query"`
		Country string `json:"country,omitempty" jsonschema_description:"Optional ISO country code to scope the search"`
	}
	geo, err := utils.InferTool("geography_search", "Forward geography search for places and points of interest.",
		func(ctx context.Context, in *geoIn) (string, error) {
			if in == nil || strings.TrimSpace(in.Query) == "" {
				return "", errors.New("query is required")
			}
			req := jigsawstack.GeographyRequest{
				Query:   in.Query,
				Country: in.Country,
			}
			resp, err := client.GeographySearch(ctx, req)
			if err != nil {
				return "", err
			}
			return marshalJSON(resp)
		})
	if err != nil {
		return nil, fmt.Errorf("build geography_search tool: %w", err)
	}
	tools = append(tools, geo)

	return tools, nil
}

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}
	return string(b), nil
}
