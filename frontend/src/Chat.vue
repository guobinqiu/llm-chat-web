<template>
  <div id="chat">
    <div style="width: 200px; float: left; padding: 10px; border-right: 1px solid #ccc;">
      <button @click="newSession">+新会话</button>
      <h3>会话列表</h3>
      <ul>
        <li v-for="(session, index) in sessions" :key="index" @click="selectSession(session)">
          {{ session.session_id }}
          <button @click="deleteSession(session)">x</button>
        </li>
      </ul>
    </div>
    <div style="margin-left: 210px; padding: 10px;">
      <button @click="logout" style="float:right">退出登录</button>
      <div v-for="(msg, index) in showMessages" :key="index">
        <b>{{ msg.role }}:</b> {{ msg.content }}
      </div>
      <input v-model="text" placeholder="Say something..." @keyup.enter="sendMsg" />
      <button @click="stop">Stop</button>
    </div>
  </div>
</template>

<script>
class ConnectionError extends Error {
  constructor(type, message) {
    super(message);
    this.name = this.constructor.name;
    this.type = type;
  }
}

export default {
  name: 'ChatPage',
  data() {
    return {
      socket: null,
      text: '',
      messages: [], //消息列表
      message: '',
      token: '',
      sessions: [],  // 用于存储会话列表
      sessionID: '', // 当前选择的会话ID
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
    this.token = localStorage.getItem('token') || '';
    this.sessionID = localStorage.getItem("sessionID") || '';
    if (this.sessionID) {
      // this.connect(this.sessionID);
      this.connect(this.sessionID).then(async () => {
        await this.fetchSessions();
        await this.fetchMessages(this.sessionID);
      }).catch(err => {
        this.handleConnectionError(err, this.sessionID);
      });
    } else {
      this.newSession();
    }
  },
  beforeDestroy() {
    this.stopSocket();
  },
  methods: {
    connect(sessionID = '') {
      return new Promise((resolve, reject) => {
        // 初始化 WebSocket 连接
        this.socket = new WebSocket(`ws://localhost:8080/ws?token=${this.token}&session_id=${sessionID}`);

        // 设置 WebSocket 的二进制类型为 arraybuffer
        this.socket.binaryType = 'arraybuffer'; // 选项有 arraybuffer | blob

        // 监听 WebSocket 事件
        this.socket.onmessage = (event) => {
          // 接收服务端的二进制数据并解码
          const chunk = new TextDecoder().decode(event.data);

          // 将接收到的消息拼接到当前消息中
          this.message += chunk;

          // 如果接收到的消息是换行符，则将当前消息添加到消息列表
          if (chunk === '\n\n') {

            // 将当前消息添加到消息列表
            this.messages.push({ role: 'assistant', content: this.message });

            // 清空当前消息
            this.message = '';
          }
        }

        // 连接成功时的回调
        this.socket.onopen = async () => {
          console.log("WebSocket connection established.");

          // 感觉连接成功后的业务代码通过promise移到外面来处理比较好
          // if (!sessionID) {
          //   await this.fetchSessions();
          //   if (this.sessions.length > 0) {
          //       localStorage.setItem("sessionID", this.sessions[0].session_id);
          //       this.sessionID = this.sessions[0].session_id;
          //       this.messages = [];
          //       this.message = '';
          //   }
          // } else {
          //   await this.fetchSessions();
          //   await this.fetchMessages(this.sessionID);
          // }

          resolve(); // 连接成功，调用resolve
        };

        // 错误处理
        this.socket.onerror = (error) => {
          console.error("WebSocket error:", error);
          // this.stopSocket();
          reject(new ConnectionError('error', error)); // 连接失败，调用reject
        };

        // 连接关闭时的回调
        this.socket.onclose = () => {
          // console.log("WebSocket connection closed.");
          // setTimeout(() => {
          //   console.log("尝试重连...");
          //   this.connect(this.sessionID);
          // }, 5000);
          reject(new ConnectionError('close', 'Connection closed'));
        };
      });
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
        const response = await fetch(`http://localhost:8080/stop?session_id=${this.sessionID}`, {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'Authorization': 'Bearer ' + this.token,  // 这里带上 token
          },
        });

        if (!response.ok) {
          throw new Error(`HTTP error! status: ${response.status}`);
        }
        console.log('停止成功');

      } catch (error) {
        console.error('停止请求失败:', error);
      }
    },
    stopSocket() {
      if (this.socket && this.socket.readyState === WebSocket.OPEN) {
        this.socket.close();
        this.socket = null;
      }
    },
    logout() {
      // localStorage.removeItem("userID");
      localStorage.removeItem("token");
      localStorage.removeItem("sessionID");
      this.$router.push("/"); // 跳转到登录页
    },
    async selectSession(session) {
      // 选择会话时进行操作，比如切换当前会话
      console.log("Selected session:", session);

      try {
        // 关闭旧连接
        this.stopSocket();

        this.messages = [];
        this.message = '';
        
        // 重新连接
        await this.connect(session.session_id);

        // 获取会话消息
        await this.fetchMessages(session.session_id);

        localStorage.setItem("sessionID", session.session_id);
        this.sessionID = session.session_id;
      } catch(error) {
        this.handleConnectionError(error, session.session_id);
      }
    },
    async fetchSessions() {
      // 获取当前用户的所有会话
      try {
        const response = await fetch('http://localhost:8080/users/sessions', {
          method: 'GET',
          headers: {
            'Authorization': 'Bearer ' + this.token,
          },
        });

        if (!response.ok) {
          throw new Error(`HTTP error! status: ${response.status}`);
        }

        const data = await response.json();
        
        console.log("获取到的会话数据:", data);
        
        this.sessions = data || [];
      } catch (error) {
        console.error('获取会话列表失败:', error);
      }
    },
    async fetchMessages(sessionID) {
      try {
        const response = await fetch(`http://localhost:8080/users/sessions/messages?session_id=${sessionID}`, {
          method: 'GET',
          headers: {
            'Authorization': 'Bearer ' + this.token,
          },
        });

        if (!response.ok) {
          throw new Error(`HTTP error! status: ${response.status}`);
        }

        const data = await response.json();
        console.log("获取到的消息数据:", data); // 打印获取到的消息
        this.messages = data || []; // 更新当前会话的消息列表
      } catch (error) {
        console.error('获取会话消息失败:', error);
      }
    },
    async newSession() {
      console.log('newSession');
      try {
        await this.connect();
        await this.fetchSessions();
        if (this.sessions.length > 0) {
            localStorage.setItem("sessionID", this.sessions[0].session_id);
            this.sessionID = this.sessions[0].session_id;
            this.messages = [];
            this.message = '';
        }
      } catch (error) {
        this.handleConnectionError(error);
      }
    },
    async deleteSession(session) {
      try {
        const response = await fetch(`http://localhost:8080/users/sessions/delete?session_id=${session.session_id}`, {
          method: 'GET',
          headers: {
            'Authorization': 'Bearer ' + this.token,
          },
        });

        if (!response.ok) {
          throw new Error(`HTTP error! status: ${response.status}`);
        }

        const data = await response.json();
        
        console.log("获取到的会话数据:", data);
        
        this.sessions = data || [];
      } catch (error) {
        console.error('删除会话数据失败:', error);
      }
    },
    handleConnectionError(error, sessionID = '') {
      if (error instanceof ConnectionError) {
        if (error.type === 'error') {
          this.stopSocket();
        } else if (error.type === 'close') {
          setTimeout(() => {
            console.log("尝试重连...");
            this.connect(sessionID);
          }, 5000);
        }
      } else {
        console.error(error);
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