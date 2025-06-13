package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
	"github.com/sashabaranov/go-openai"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type ChatClient struct {
	openaiClient *openai.Client
	model        string
	messages     []openai.ChatCompletionMessage // 用于存储历史消息，实现多轮对话
	retainNum    int                            // 超过n条就合并
}

func main() {
	_ = godotenv.Load()
	apiKey := os.Getenv("OPENAI_API_KEY")
	baseURL := os.Getenv("OPENAI_API_BASE")
	model := os.Getenv("OPENAI_API_MODEL")
	if apiKey == "" || baseURL == "" || model == "" {
		fmt.Println("检查环境变量设置")
		return
	}

	config := openai.DefaultConfig(apiKey)
	config.BaseURL = baseURL
	openaiClient := openai.NewClientWithConfig(config)

	cc := &ChatClient{
		openaiClient: openaiClient,
		model:        model,
		messages:     make([]openai.ChatCompletionMessage, 0),
		retainNum:    10,
	}

	http.HandleFunc("/ws", cc.ChatLoop)
	log.Println("Server started on :8080")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}

func (cc *ChatClient) ChatLoop(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer ws.Close()

	for {
		_, msgBytes, err := ws.ReadMessage()
		if err != nil {
			log.Printf("error: %v", err)
			break
		}

		if err := cc.ProcessQuery(ws, string(msgBytes)); err != nil {
			fmt.Printf("请求失败: %v\n", err)
			continue
		}
	}
}

func (cc *ChatClient) ProcessQuery(ws *websocket.Conn, userInput string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 合并上下文
	if err := cc.Merge(); err != nil {
		return fmt.Errorf("合并上下文失败: %v", err)
	}

	// 添加问题到历史消息
	cc.messages = append(cc.messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userInput,
	})

	stream, err := cc.openaiClient.CreateChatCompletionStream(ctx, openai.ChatCompletionRequest{
		Model:    cc.model,
		Messages: cc.messages,
		Stream:   true, // 开启流式响应
	})
	if err != nil {
		return err
	}
	defer stream.Close()

	//	构建一个字符串，用于存储回答
	var build strings.Builder

	for {
		resp, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				log.Println("stream finished")

				// 添加回答到历史消息
				cc.messages = append(cc.messages, openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: build.String(),
				})

				// 回答结速了告诉前端要换行
				ws.WriteMessage(websocket.BinaryMessage, []byte("\n"))

				break
			}
			log.Printf("stream receive error: %v", err)
			break
		}

		// OpenAI的API设计上支持一次请求返回多个候选回答（choices）默认为1
		for _, choice := range resp.Choices {
			content := choice.Delta.Content
			if content != "" {
				build.WriteString(content)
				if err := ws.WriteMessage(websocket.BinaryMessage, []byte(content)); err != nil {
					log.Printf("websocket write error: %v", err)
					break
				}
			}
		}
	}

	return nil
}

func (cc *ChatClient) Merge() error {
	if len(cc.messages) <= cc.retainNum {
		return nil
	}

	// 让大模型总结成一条摘要信息
	summary, err := cc.Summarize(cc.messages)
	if err != nil {
		return nil
	}

	// 重写messages
	cc.messages = []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "以下是之前对话的总结：" + summary},
	}

	return nil
}

func (cc *ChatClient) Summarize(history []openai.ChatCompletionMessage) (string, error) {
	summaryPrompt := "以下是用户与助手之间的对话，请总结用户的提问意图和助手的关键回答，简洁准确，不要遗漏重要信息：\n\n"
	for _, msg := range history {
		summaryPrompt += fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content)
	}
	log.Println("summaryPrompt=", summaryPrompt)
	return cc.CallOpenAI([]openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: summaryPrompt},
	})
}

func (cc *ChatClient) CallOpenAI(messages []openai.ChatCompletionMessage) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := cc.openaiClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:       cc.model,
		Messages:    messages,
		Temperature: 0.7, // 控制回答的随机性，范围是 0 到 2（默认 1）
		MaxTokens:   512, // 限制返回的最大 token 数
	})
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("未从API接收到任何响应")
	}

	return resp.Choices[0].Message.Content, nil
}
