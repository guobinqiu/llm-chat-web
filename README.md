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

<img width="618" alt="image" src="https://github.com/user-attachments/assets/679cb2b7-775a-483f-b10d-0b1ba4359a8f" />

## Feature

- [x] 加入停止回答功能
- [x] 加入websocket心跳检测和断线重连
- [ ] 加入多用户支持
