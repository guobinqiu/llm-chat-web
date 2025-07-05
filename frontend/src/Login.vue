<template>
  <div id="login">
    <h2>登录</h2>
    <input v-model="username" placeholder="用户名" />
    <input v-model="password" type="password" placeholder="密码" />
    <button @click="login">登录</button>
    <p v-if="error" style="color: red;">{{ error }}</p>
  </div>
</template>

<script>
export default {
  name: 'LoginPage',
  data() {
    return {
      username: '',
      password: '',
      error: '',
    };
  },
  methods: {
    async login() {
      try {
        const res = await fetch("http://localhost:8080/login", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            username: this.username.trim(),
            password: this.password.trim()
          }),
        });

        if (!res.ok) throw new Error("用户名或密码错误");

        const data = await res.json();
        // localStorage.setItem("userID", data.user_id);
        localStorage.setItem("token", data.token);
        this.$router.push("/chat");
      } catch (err) {
        this.error = err.message;
      }
    },
  }
}
</script>

<style scoped>
</style>
