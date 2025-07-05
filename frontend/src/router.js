import Vue from 'vue'
import Router from 'vue-router'
import Login from './Login.vue'
import Chat from './Chat.vue'

Vue.use(Router)

const router = new Router({
  routes: [
    {
      path: '/login',
      component: Login,
      name: 'Login',
    },
    {
      path: '/chat',
      component: Chat,
      name: 'Chat',

    },
    {
      path: '/',
      redirect: '/login',
    },
  ]
})

const whiteList = ['/', '/login'];

router.beforeEach((to, from, next) => {
  // const userID = localStorage.getItem("userID");
  const token = localStorage.getItem("token");

  if (!whiteList.includes(to.path) && !token) {
    next('/login');
  } else {
    next();
  }
});

export default router