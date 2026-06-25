package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/joho/godotenv"
	"github.com/yudaprama/egent-jigsawstack/agent"
)

type ChatCompletionRequest struct {
	Model       string                  `json:"model"`
	Messages    []ChatCompletionMessage `json:"messages"`
	Stream      bool                    `json:"stream,omitempty"`
	Temperature float64                 `json:"temperature,omitempty"`
	MaxTokens   int                     `json:"max_tokens,omitempty"`
}

type ChatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
}

type ChatCompletionChoice struct {
	Index        int                   `json:"index"`
	Message      ChatCompletionMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

type ChatCompletionChunk struct {
	ID      string                      `json:"id"`
	Object  string                      `json:"object"`
	Created int64                       `json:"created"`
	Model   string                      `json:"model"`
	Choices []ChatCompletionChunkChoice `json:"choices"`
}

type ChatCompletionChunkChoice struct {
	Index        int                   `json:"index"`
	Delta        ChatCompletionMessage `json:"delta"`
	FinishReason *string               `json:"finish_reason"`
}

// version is injected at build time via -ldflags "-X main.version=<tag>".
var version = "dev"

var runner *adk.Runner

func main() {
	if exe, err := os.Executable(); err == nil {
		godotenv.Load(filepath.Join(filepath.Dir(exe), "..", ".env"))
	}
	godotenv.Load()

	port := flag.String("port", "10513", "HTTP server port")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	ctx := context.Background()
	ag, err := agent.NewAgent(ctx, "", "", "", "")
	if err != nil {
		log.Fatalf("create agent: %v", err)
	}
	runner = agent.NewRunner(ctx, ag)

	http.HandleFunc("/v1/chat/completions", chatCompletionsHandler)
	http.HandleFunc("/health", healthHandler)

	addr := "0.0.0.0:" + *port
	log.Printf("JigsawStack Eino Agent server %s starting on %s", version, addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func chatCompletionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, "messages cannot be empty", http.StatusBadRequest)
		return
	}

	query := buildConversationQuery(req.Messages)
	if req.Stream {
		streamChatCompletion(w, r, req, query)
		return
	}

	iter := runner.Query(r.Context(), query)
	var finalContent strings.Builder
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			http.Error(w, fmt.Sprintf("agent error: %v", event.Err), http.StatusInternalServerError)
			return
		}
		text, err := extractEventContent(event)
		if err != nil {
			http.Error(w, fmt.Sprintf("agent error: %v", err), http.StatusInternalServerError)
			return
		}
		finalContent.WriteString(text)
	}

	resp := ChatCompletionResponse{
		ID:      generateID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []ChatCompletionChoice{{
			Index: 0,
			Message: ChatCompletionMessage{
				Role:    "assistant",
				Content: finalContent.String(),
			},
			FinishReason: "stop",
		}},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func streamChatCompletion(w http.ResponseWriter, r *http.Request, req ChatCompletionRequest, query string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	writer := bufio.NewWriter(w)
	requestID := generateID()
	iter := runner.Query(r.Context(), query)

	for {
		event, ok := iter.Next()
		if !ok {
			finishReason := "stop"
			chunk := ChatCompletionChunk{
				ID:      requestID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   req.Model,
				Choices: []ChatCompletionChunkChoice{{
					Index:        0,
					Delta:        ChatCompletionMessage{},
					FinishReason: &finishReason,
				}},
			}
			writeSSE(writer, chunk)
			fmt.Fprintf(writer, "data: [DONE]\n\n")
			writer.Flush()
			flusher.Flush()
			break
		}
		if event.Err != nil {
			chunk := ChatCompletionChunk{
				ID:      requestID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   req.Model,
				Choices: []ChatCompletionChunkChoice{{
					Index: 0,
					Delta: ChatCompletionMessage{
						Role:    "assistant",
						Content: fmt.Sprintf("\n[Error: %v]", event.Err),
					},
				}},
			}
			writeSSE(writer, chunk)
			writer.Flush()
			flusher.Flush()
			break
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			text, err := extractEventContent(event)
			if err != nil {
				chunk := ChatCompletionChunk{
					ID:      requestID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []ChatCompletionChunkChoice{{
						Index: 0,
						Delta: ChatCompletionMessage{
							Role:    "assistant",
							Content: fmt.Sprintf("\n[Error: %v]", err),
						},
					}},
				}
				writeSSE(writer, chunk)
				writer.Flush()
				flusher.Flush()
				break
			}
			if text != "" {
				chunk := ChatCompletionChunk{
					ID:      requestID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []ChatCompletionChunkChoice{{
						Index: 0,
						Delta: ChatCompletionMessage{
							Role:    "assistant",
							Content: text,
						},
					}},
				}
				writeSSE(writer, chunk)
				writer.Flush()
				flusher.Flush()
			}
		}
	}
}

func writeSSE(writer *bufio.Writer, chunk ChatCompletionChunk) {
	data, _ := json.Marshal(chunk)
	fmt.Fprintf(writer, "data: %s\n\n", data)
}

func buildConversationQuery(messages []ChatCompletionMessage) string {
	if len(messages) == 1 {
		return messages[0].Content
	}
	var b strings.Builder
	for _, m := range messages {
		switch m.Role {
		case "user":
			fmt.Fprintf(&b, "User: %s\n", m.Content)
		case "assistant":
			fmt.Fprintf(&b, "Assistant: %s\n", m.Content)
		}
	}
	return b.String()
}

func generateID() string {
	return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
}

// extractEventContent extracts text content from an AgentEvent, handling both
// streaming and non-streaming MessageOutput variants.
func extractEventContent(event *adk.AgentEvent) (string, error) {
	if event.Output == nil || event.Output.MessageOutput == nil {
		return "", nil
	}
	mv := event.Output.MessageOutput
	if mv.IsStreaming {
		var sb strings.Builder
		for {
			chunk, err := mv.MessageStream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return sb.String(), fmt.Errorf("recv stream: %w", err)
			}
			if chunk.Role == schema.Assistant && chunk.Content != "" {
				sb.WriteString(chunk.Content)
			}
		}
		return sb.String(), nil
	}
	msg, err := mv.GetMessage()
	if err != nil {
		return "", err
	}
	if msg.Role == schema.Assistant && msg.Content != "" {
		return msg.Content, nil
	}
	return "", nil
}
