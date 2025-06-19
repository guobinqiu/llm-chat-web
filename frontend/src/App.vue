<template>
  <div id="app">
    <div v-for="(msg, index) in showMessages" :key="index">
      <b>{{ msg.role }}:</b> {{ msg.content }}
    </div>
    <input v-model="text" placeholder="Say something..." @keyup.enter="sendMsg" />
    <button @click="stop">Stop</button>
  </div>
</template>

<script>
export default {
  data() {
    return {
      socket: null,
      ChatMessage: null,
      text: '',
      messages: [], //消息列表
      message: '',
    };
  },
  computed: {
    showMessages() {
      // 创建消息列表副本
      const all = [...this.messages];

      // 在副本上做更新
      if (this.message.trim()) {
        all.push({ role: 'assistant', content: this.message });
      }

      return all;
    }
  },
  mounted() {
    // 初始化 WebSocket 连接
    this.initSocket();
  },
  methods: {
    initSocket() {
      // 初始化 WebSocket 连接
      this.socket = new WebSocket('ws://localhost:8080/ws');

      // 设置 WebSocket 的二进制类型为 arraybuffer
      this.socket.binaryType = 'arraybuffer'; // 选项有 arraybuffer | blob

      // 监听 WebSocket 事件
      this.socket.onmessage = (event) => {
        // 接收服务端的二进制数据并解码
        const chunk = new TextDecoder().decode(event.data);

        // 将接收到的消息拼接到当前消息中
        this.message += chunk

        // 如果接收到的消息是换行符，则将当前消息添加到消息列表
        if (chunk === '\n') {

          // 将当前消息添加到消息列表
          this.messages.push({ role: 'assistant', content: this.message })

          // 清空当前消息
          this.message = '';
        }
      }

      // 连接成功时的回调
      this.socket.onopen = () => {
        console.log("WebSocket connection established.");
      };

      // 错误处理
      this.socket.onerror = (error) => {
        console.error("WebSocket error:", error);
      };

      // 连接关闭时的回调
      this.socket.onclose = () => {
        console.log("WebSocket connection closed.");
      };
    },
    sendMsg() {
      // 如果输入框为空，则不发送消息
      if (!this.text.trim()) return;

      // 将输入的文本添加到消息列表
      this.messages.push({ role: 'user', content: this.text.trim() });

      // 将消息对象转换为二进制格式
      const buffer = new TextEncoder().encode(this.text.trim()).buffer;

      // 发送二进制数据
      this.socket.send(buffer);

      // 清空输入框
      this.text = '';
    },
    async stop() {
      try {
        const response = await fetch('http://localhost:8080/stop', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
          },
        });

        if (!response.ok) {
          throw new Error(`HTTP error! status: ${response.status}`);
        }
        console.log('停止成功');

      } catch (error) {
        console.error('停止请求失败:', error);
      }
    }
  }
}
</script>

<style scoped>
input {
  width: 300px;
  padding: 10px;
  margin-bottom: 10px;
}
</style>
