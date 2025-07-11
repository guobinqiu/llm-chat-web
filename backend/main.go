package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-playground/validator"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sashabaranov/go-openai"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type MCPConfig struct {
	MCPServers map[string]MCPServer `json:"mcpServers"`
}

type MCPServer struct {
	Type    string   `json:"type" validate:"required"`
	Command string   `json:"command" validate:"required"`
	Args    []string `json:"args,omitempty"`
}

type ChatClient struct {
	openaiClient *openai.Client
	model        string
	messages     []openai.ChatCompletionMessage // 用于存储历史消息，实现多轮对话
	retainNum    int                            // 超过n条就合并
	ctx          context.Context
	cancel       context.CancelFunc
	temperature  float32 // 控制回答的随机性，范围是 0 到 2（默认 1）
	maxTokens    int     // 限制返回的最大 token 数
	mcpClients   []*client.Client
}

type Heartbeat struct {
	lastPongUnix int64 // 存储最后一次收到pong的时间戳
	mu           sync.RWMutex
	pingInterval time.Duration
	pongTimeout  time.Duration
	ws           *websocket.Conn
}

func main() {
	http.HandleFunc("/ws", withCORS(WithAuth(ChatLoop)))
	http.HandleFunc("/stop", withCORS(WithAuth(StopChat)))
	http.HandleFunc("/login", withCORS(LoginHandler))
	http.HandleFunc("/users/sessions", withCORS(WithAuth(GetSessionListHandler)))
	http.HandleFunc("/users/sessions/messages", withCORS(WithAuth(GetMessageListHandler)))
	http.HandleFunc("/users/sessions/delete", withCORS(WithAuth(DeleteSessionHandler)))

	log.Println("Server started on :8080")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}

func ChatLoop(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	user := r.Context().Value("user").(*User)

	cc, err := user.CreateOrGetSession(sessionID)
	if err != nil {
		log.Fatal(err)
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer ws.Close()

	heartbeat := &Heartbeat{
		lastPongUnix: time.Now().Unix(),
		pingInterval: 50 * time.Second,
		pongTimeout:  30 * time.Second,
		ws:           ws,
	}

	ws.SetReadLimit(1 * 1024 * 1024) // 1MB

	// 监听pong消息
	ws.SetPongHandler(func(string) error {
		log.Println("收到客户端pong")
		heartbeat.mu.Lock()
		heartbeat.lastPongUnix = time.Now().Unix()
		heartbeat.mu.Unlock()
		return nil
	})

	// 心跳检测
	go heartbeat.StartHeartbeat()

	for {
		_, msgBytes, err := ws.ReadMessage()
		if err != nil {
			log.Printf("读取消息失败: %v", err)
			break
		}

		if err := cc.ProcessQuery(ws, string(msgBytes)); err != nil {
			log.Printf("处理失败: %v", err)
		}
	}
}

func (cc *ChatClient) ProcessQuery(ws *websocket.Conn, userInput string) error {
	cc.ctx, cc.cancel = context.WithCancel(context.Background())
	defer cc.cancel()

	// 维护toolName到mcpClient的映射
	toolNameMap := make(map[string]*client.Client)

	// 列出所有可用工具
	availableTools := []openai.Tool{}

	for _, mcpClient := range cc.mcpClients {
		toolsResp, err := mcpClient.ListTools(cc.ctx, mcp.ListToolsRequest{})
		if err != nil {
			log.Printf("Failed to list tools: %v", err)
		}
		for _, tool := range toolsResp.Tools {
			// fmt.Println("name:", tool.Name)
			// fmt.Println("description:", tool.Description)
			// fmt.Println("parameters:", tool.InputSchema)
			availableTools = append(availableTools, openai.Tool{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.InputSchema,
				},
			})

			toolNameMap[tool.Name] = mcpClient
		}
	}

	// 合并上下文
	if err := cc.Merge(); err != nil {
		return fmt.Errorf("合并上下文失败: %v", err)
	}

	// 添加问题到历史消息
	cc.messages = append(cc.messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userInput,
	})

	//	构建一个字符串，用于存储回答
	var build strings.Builder

	type ToolCallBuilder struct {
		Index     *int
		ID        string
		Name      string
		Arguments strings.Builder
	}
	toolCalls := make(map[int]*ToolCallBuilder)

OuterLoop:
	for {
		stream, err := cc.openaiClient.CreateChatCompletionStream(cc.ctx, openai.ChatCompletionRequest{
			Model:       cc.model,
			Messages:    cc.messages,
			Stream:      true, // 开启流式响应
			Temperature: cc.temperature,
			MaxTokens:   cc.maxTokens,
			Tools:       availableTools,
		})
		if err != nil {
			return err
		}
		defer stream.Close()

		build.Reset()

		for {
			resp, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					log.Println("stream finished")
					if build.Len() > 0 {
						// 添加回答到历史消息
						cc.messages = append(cc.messages, openai.ChatCompletionMessage{
							Role:    openai.ChatMessageRoleAssistant,
							Content: build.String(),
						})

						// 回答结速了告诉前端要换行
						ws.WriteMessage(websocket.BinaryMessage, []byte("\n\n"))

						break OuterLoop
					} else if len(toolCalls) > 0 {
						toolCallMessages := []openai.ChatCompletionMessage{}
						successfulToolCalls := []openai.ToolCall{}

						for _, builder := range toolCalls {
							fmt.Println("== Tool Call ==")
							fmt.Println("ID:", builder.ID)
							fmt.Println("Name:", builder.Name)
							fmt.Println("Arguments:", builder.Arguments.String())
							toolName := builder.Name
							toolArgsRaw := builder.Arguments.String()
							// fmt.Println("=====toolCall.Function.Arguments:", toolArgsRaw)
							var toolArgs map[string]any
							_ = json.Unmarshal([]byte(toolArgsRaw), &toolArgs)

							// 调用工具
							req := mcp.CallToolRequest{}
							req.Params.Name = toolName
							req.Params.Arguments = toolArgs
							//resp, err := cc.mcpClient.CallTool(ctx, req)
							mcpClient, ok := toolNameMap[toolName]
							if !ok || mcpClient == nil {
								log.Printf("工具 [%s] 没有找到对应的 MCP client", toolName)
								continue
							}
							resp, err := mcpClient.CallTool(cc.ctx, req)
							if err != nil {
								log.Printf("工具调用失败: %v", err)
								continue
							}

							// 构造 tool message
							// 把工具返回的答案记录下来，作为后续模型推理的输入
							toolCallMessages = append(toolCallMessages, openai.ChatCompletionMessage{
								Role:       openai.ChatMessageRoleTool, // 说明是工具的响应
								ToolCallID: builder.ID,                 // 绑定之前模型说要调用的那个 tool_call.id
								Content:    fmt.Sprintf("%s", resp.Content),
							})

							successfulToolCalls = append(successfulToolCalls, openai.ToolCall{
								Index: builder.Index,
								ID:    builder.ID,
								Type:  openai.ToolTypeFunction,
								Function: openai.FunctionCall{
									Name:      builder.Name,
									Arguments: builder.Arguments.String(),
								},
							})
						}

						// 添加 assistant tool call 信息
						if len(successfulToolCalls) > 0 {
							cc.messages = append(cc.messages, openai.ChatCompletionMessage{
								Role:      openai.ChatMessageRoleAssistant,
								Content:   "",
								ToolCalls: successfulToolCalls,
							})
						}

						// 把工具结果作为 observation 添加到上下文
						if len(toolCallMessages) > 0 {
							cc.messages = append(cc.messages, toolCallMessages...)
						}
						break
					}
				}

				if errors.Is(err, context.Canceled) {
					log.Println("流被取消")

					// 添加回答到历史消息
					cc.messages = append(cc.messages, openai.ChatCompletionMessage{
						Role:    openai.ChatMessageRoleAssistant,
						Content: build.String(),
					})

					// 回答结速了告诉前端要换行
					ws.WriteMessage(websocket.BinaryMessage, []byte("\n"))

					return err
				}

				log.Printf("stream receive error: %v", err)
				break OuterLoop
			}

			// OpenAI的API设计上支持一次请求返回多个候选回答（choices）默认为1
			for _, choice := range resp.Choices {
				content := choice.Delta.Content
				if content != "" { // 若直接生成文本
					build.WriteString(content)
					if err := ws.WriteMessage(websocket.BinaryMessage, []byte(content)); err != nil {
						log.Printf("websocket write error: %v", err)
						break
					}
				}

				if len(choice.Delta.ToolCalls) > 0 { // 若调用工具
					for _, tool := range choice.Delta.ToolCalls {
						builder, exists := toolCalls[*tool.Index]
						if !exists {
							builder = &ToolCallBuilder{
								Index: tool.Index,
								ID:    tool.ID,
								Name:  tool.Function.Name,
							}
							toolCalls[*tool.Index] = builder
						}
						builder.Arguments.WriteString(tool.Function.Arguments)
					}
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
	resp, err := cc.openaiClient.CreateChatCompletion(cc.ctx, openai.ChatCompletionRequest{
		Model:       cc.model,
		Messages:    messages,
		Temperature: cc.temperature,
		MaxTokens:   cc.maxTokens,
	})
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("未从API接收到任何响应")
	}

	return resp.Choices[0].Message.Content, nil
}

func StopChat(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "缺少参数session_id", http.StatusBadRequest)
		return
	}
	user := r.Context().Value("user").(*User)
	cc, err := user.CreateOrGetSession(sessionID)
	if err != nil {
		http.Error(w, fmt.Sprintf("获取会话失败: %v", err), http.StatusInternalServerError)
		return
	}
	if cc.cancel != nil {
		cc.cancel()
	}
	fmt.Fprintln(w, "stopped")
}

func (hb *Heartbeat) StartHeartbeat() {
	ticker := time.NewTicker(hb.pingInterval)
	defer ticker.Stop()

	for range ticker.C {
		hb.mu.RLock()
		lastPong := time.Unix(hb.lastPongUnix, 0)
		hb.mu.RUnlock()
		if time.Since(lastPong) > hb.pingInterval+hb.pongTimeout { // 距离上次收到Pong已经超过了8秒就判定客户端断线
			log.Println("未收到客户端pong，断开连接")
			hb.ws.Close()
			return
		}

		log.Println("服务端发送ping")

		// 若客户端断网或关闭连接，WriteMessage 会报错
		if err := hb.ws.WriteMessage(websocket.PingMessage, []byte("")); err != nil {

			// 断网之后连接就作废了需要重开新的连接
			// 连接失效后必须关闭，避免资源泄漏
			// ws.Close() 会触发客户端的 onclose 回调
			hb.ws.Close()

			log.Println("发送ping失败，断开连接:", err)

			// 退出这个协程
			return
		}
	}
}

// ======================== 添加用户相关代码 =======================

// 添加 /login http接口
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	// UserID string `json:"user_id"`
	Token string `json:"token"`
}

// mock 用户
// var users = map[string]string{
// 	"alice": "password123",
// 	"bob":   "password456",
// }

type User struct {
	ID           string         `json:"id"`
	Username     string         `json:"username"`
	Password     string         `json:"password"`
	ChatSessions []*ChatSession `json:"chat_session"`
	mu           sync.Mutex
}

var users = []*User{
	{ID: "1", Username: "alice", Password: "password123", ChatSessions: []*ChatSession{}},
	{ID: "2", Username: "bob", Password: "password456", ChatSessions: []*ChatSession{}},
}

func LoginHandler(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// expectedPassword, ok := users[req.Username]
	// if !ok || expectedPassword != req.Password {
	// 	http.Error(w, "Invalid username or password", http.StatusUnauthorized)
	// 	return
	// }
	// userID := fmt.Sprintf("user=%s", req.Username)
	// resp := LoginResponse{
	// 	UserID: userID,
	// }

	user, err := AuthenticateUser(req.Username, req.Password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
	}
	token, err := GenerateJWT(user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := LoginResponse{
		Token: token,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

var jwtSecret = []byte("孤舟蓑笠问独钓寒江月")

// 生成JWT Token
func GenerateJWT(user *User) (string, error) {
	// 存放 user_id、username、role 等简单字段
	// 放入整个user的话虽然少一次查询，但会造成token过大，更新用户信息也不灵活
	claims := jwt.MapClaims{
		"user_id":  user.ID,
		"username": user.Username,
		"exp":      time.Now().Add(time.Hour * 24).Unix(), // 24小时后过期
		"iat":      time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString(jwtSecret)
}

// 解析JWT Token
func ParseJWT(tokenStr string) (jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		// 验证方法签名
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return jwtSecret, nil
	})

	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid token: %v", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}

func AuthenticateUser(username, password string) (*User, error) {
	for _, u := range users {
		if u.Username == username && u.Password == password {
			return u, nil
		}
	}
	return nil, errors.New("invalid username or password")
}

// ============ 添加 middleware 组件 ==================
func withCORS(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 设置允许跨域的头部
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Allow-Methods", "*")

		// 如果是预检请求，直接返回
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		// 执行原来的处理函数
		handler(w, r)
	}
}

func WithAuth(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := extractToken(r)
		if tokenStr == "" {
			http.Error(w, "Unauthorized: no token provided", http.StatusUnauthorized)
			return
		}

		claims, err := ParseJWT(tokenStr)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		userID := claims["user_id"].(string)

		user, err := GetUserByID(userID)
		if err != nil {
			http.Error(w, "User not found", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), "user", user)
		handler(w, r.WithContext(ctx))
	}
}

func extractToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		// Header 里格式应该是 "Bearer <token>"
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
			return parts[1]
		}
		return ""
	}

	// 如果 Header 没拿到，尝试从 URL 查询参数拿
	token := r.URL.Query().Get("token")
	return token
}

func GetUserByID(id string) (*User, error) {
	for _, user := range users {
		if user.ID == id {
			return user, nil
		}
	}
	return nil, errors.New("user not found")
}

// ======================== 加入多 session 支持 =======================

type ChatSession struct {
	SessionID   string    `json:"session_id"`
	SessionName string    `json:"session_name"`
	CreatedAt   time.Time `json:"created_at"`
	*ChatClient
}

func (u *User) CreateOrGetSession(sessionID string) (*ChatSession, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	session, _ := u.getSession(sessionID)
	if session != nil {
		return session, nil
	}

	cc, err := NewChatClient()
	if err != nil {
		return nil, err
	}

	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	newSession := &ChatSession{
		SessionID:   sessionID,
		SessionName: "New chat",
		CreatedAt:   time.Now(),
		ChatClient:  cc,
	}
	u.ChatSessions = append(u.ChatSessions, newSession)

	return newSession, nil
}

func (u *User) getSession(sessionID string) (*ChatSession, int) {
	for i, session := range u.ChatSessions {
		if session.SessionID == sessionID {
			return session, i
		}
	}
	return nil, -1
}

func (u *User) GetSessionList() []*ChatSession {
	// 最近的排前面
	// 如果less(i, j)返回true表示i应该排在j前面
	sort.Slice(u.ChatSessions, func(i, j int) bool {
		return u.ChatSessions[i].CreatedAt.After(u.ChatSessions[j].CreatedAt)
	})
	return u.ChatSessions
}

func NewChatClient() (*ChatClient, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mcpClients, errs := LoadMCPClients("config.json", ctx)
	if len(errs) > 0 {
		for _, err := range errs {
			log.Println(err)
		}
	}
	// defer func() {
	// 	for _, mcpClient := range mcpClients {
	// 		mcpClient.Close()
	// 	}
	// }()

	_ = godotenv.Load()
	apiKey := os.Getenv("OPENAI_API_KEY")
	baseURL := os.Getenv("OPENAI_API_BASE")
	model := os.Getenv("OPENAI_API_MODEL")
	if apiKey == "" || baseURL == "" || model == "" {
		fmt.Println("检查环境变量设置")
		return nil, errors.New("检查环境变量设置")
	}

	config := openai.DefaultConfig(apiKey)
	config.BaseURL = baseURL
	openaiClient := openai.NewClientWithConfig(config)

	cc := &ChatClient{
		openaiClient: openaiClient,
		model:        model,
		messages:     make([]openai.ChatCompletionMessage, 0),
		retainNum:    10,
		temperature:  0.7,
		maxTokens:    512,
		mcpClients:   mcpClients,
	}
	return cc, nil
}

// 获取用户的会话列表
func GetSessionListHandler(w http.ResponseWriter, r *http.Request) {
	// 从context中获取当前用户
	user, ok := r.Context().Value("user").(*User)
	if !ok {
		http.Error(w, "User not found", http.StatusUnauthorized)
		return
	}

	// 获取会话列表
	sessionList := user.GetSessionList()
	fmt.Println("GetSessionList", sessionList)

	// 返回会话列表
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(sessionList); err != nil {
		http.Error(w, "Failed to encode session list", http.StatusInternalServerError)
	}
}

// 获取会话的消息列表
func GetMessageListHandler(w http.ResponseWriter, r *http.Request) {
	// 从context中获取当前用户
	user, ok := r.Context().Value("user").(*User)
	if !ok {
		http.Error(w, "User not found", http.StatusUnauthorized)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}

	session, _ := user.getSession(sessionID)

	var messageList []openai.ChatCompletionMessage
	for _, message := range session.messages {
		if (message.Role == openai.ChatMessageRoleUser ||
			message.Role == openai.ChatMessageRoleAssistant) && strings.TrimSpace(message.Content) != "" {
			messageList = append(messageList, message)
		}
	}

	fmt.Println("GetMessageList", messageList)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(messageList); err != nil {
		http.Error(w, "Failed to encode message list", http.StatusInternalServerError)
	}
}

// 删除用户的会话
func DeleteSessionHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	// 从context中获取当前用户
	user, ok := r.Context().Value("user").(*User)
	if !ok {
		http.Error(w, "User not found", http.StatusUnauthorized)
		return
	}

	// 从会话列表中删除
	_, index := user.getSession(sessionID)
	if index == -1 {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	user.ChatSessions = append(user.ChatSessions[:index], user.ChatSessions[index+1:]...)

	sessionList := user.GetSessionList()
	fmt.Println("GetSessionList", sessionList)

	// 返回会话列表
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(sessionList); err != nil {
		http.Error(w, "Failed to encode session list", http.StatusInternalServerError)
	}
}

// 创建客户端实例，连接 MCP 服务端
func LoadMCPClients(configPath string, ctx context.Context) ([]*client.Client, []error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, []error{err}
	}

	var mcpConfig MCPConfig
	err = json.Unmarshal(data, &mcpConfig)
	if err != nil {
		return nil, []error{err}
	}

	if err := validator.New().Struct(mcpConfig); err != nil {
		return nil, []error{err}
	}

	var mcpClients []*client.Client
	var errors []error

	for name, mcpServer := range mcpConfig.MCPServers {
		var mcpClient *client.Client
		var err error

		switch strings.ToLower(mcpServer.Type) {
		case "stdio":
			mcpClient, err = client.NewStdioMCPClient(mcpServer.Command, mcpServer.Args)
		case "http":
			mcpClient, err = client.NewStreamableHttpClient(mcpServer.Command)
		case "sse":
			mcpClient, err = client.NewSSEMCPClient(mcpServer.Command)
		default:
			err = fmt.Errorf("未知服务类型: %s (%s)", name, mcpServer.Type)
		}

		if err != nil {
			errors = append(errors, fmt.Errorf("[%s] 创建客户端失败: %v", name, err))
			continue
		}

		// 初始化 MCP 客户端
		fmt.Println("Initializing client...")
		initRequest := mcp.InitializeRequest{}
		initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		initRequest.Params.ClientInfo = mcp.Implementation{
			Name:    name, // 使用配置中的名称作为客户端名
			Version: "1.0.0",
		}
		initResult, err := mcpClient.Initialize(ctx, initRequest)
		if err != nil {
			errors = append(errors, fmt.Errorf("[%s] 初始化失败: %v", name, err))
			continue
		}

		fmt.Printf("[%s] Connected to server: %s %s\n", name, initResult.ServerInfo.Name, initResult.ServerInfo.Version)

		mcpClients = append(mcpClients, mcpClient)
	}

	return mcpClients, errors
}
