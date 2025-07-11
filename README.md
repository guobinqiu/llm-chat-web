# LLM Chat Web版

把 [LLM Chat](https://github.com/guobinqiu/llm-chat) (命令行版) 改成 Web 版

## 运行

1. 运行后端服务

```
cd backend && go run main.go
```

2. 运行前端服务

```
cd frontend && npm run serve
```

## 效果图

![llm-chat-web-v0 4](https://github.com/user-attachments/assets/1ba5fdb9-dc5e-4110-808a-78eb82240da3)

## RoadMap

- [x] 加入停止回答功能
- [x] 加入websocket心跳检测和断线重连
- [x] 加入多用户支持
- [x] 加入多会话支持
- [x] [把Agent能力合并进来](https://github.com/guobinqiu/ai-agent)
- [x] 加入流式调用MCP
- [x] 加入流式调用Funcation Call
- [ ] 加入图片阅读能力
- [ ] 数据持久化

## Backlog

- [ ] WebSocket改SSE
- [ ] SSE搭配SharedWorker减少连接数
- [ ] 前端加上样式
- [ ] 后端使用Gin框架
