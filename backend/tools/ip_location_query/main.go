package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	// Create a new MCP server
	s := server.NewMCPServer(
		"ip-location-server",
		"1.0.0",
		server.WithResourceCapabilities(true, true),
		server.WithPromptCapabilities(true),
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)

	// Add an ip tool
	ipTool := mcp.NewTool("ip_location_query",
		mcp.WithDescription("查询IP地址的地理位置"),
		mcp.WithString("ip",
			mcp.Required(),
			mcp.Description("要查询的IP地址"),
		),
	)

	// Add the ip handler
	s.AddTool(ipTool, ipQueryHandler)

	// Start the server
	httpServer := server.NewStreamableHTTPServer(s)
	if err := httpServer.Start(":8082"); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}

func ipQueryHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ip, ok := request.GetArguments()["ip"].(string)
	if !ok {
		return nil, errors.New("ip must be a string")
	}

	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return nil, errors.New("无效的 IP 地址")
	}

	// 调用外部IP地理位置服务
	resp, err := http.Get("http://ip-api.com/json/" + ip)
	if err != nil {
		return nil, fmt.Errorf("查询失败: %v", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体错误: %v", err)
	}

	// return &mcp.CallToolResult{
	// 	Content: []mcp.Content{
	// 		mcp.TextContent{
	// 			Type: "text",
	// 			Text: string(data),
	// 		},
	// 	},
	// }, nil
	return mcp.NewToolResultText(string(data)), nil
}
