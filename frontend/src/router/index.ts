import { createRouter, createWebHistory } from 'vue-router'
import FeedbackView from '../views/FeedbackView.vue'
import { useModeStore } from '../stores/mode'
import { installModeTransition } from './modeTransition'
import { legacyDocRedirect, paramString } from '../lib/docNav'
import { ownerProjectIdWithin } from '../lib/docNavClient'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/',
      name: 'home',
      component: FeedbackView
    },
    {
      // 任务清单：与首页共用同一个壳（FeedbackView），右侧工作区整页换成这个项目（workflow）的任务清单；
      // 项目写在地址里便于直达与刷新，不带项目时由页面自己挑一个（上次看的 → 当前会话的 → 最近有动静的）。
      path: '/work/:workflowId?',
      name: 'work',
      component: FeedbackView
    },
    {
      // 群聊（#117）：/group 是群列表，/group/:groupId 是一个群；与首页共用同一个壳。
      path: '/group/:groupId?',
      name: 'group',
      component: FeedbackView
    },
    {
      // 项目知识库（#114）：/projects 是项目列表，/projects/:projectId 是一个项目（?tab=kb|modules|module|settings|migration|grants|audit）。
      path: '/projects/:projectId?',
      name: 'projects',
      component: FeedbackView
    },
    {
      // 项目里的单篇文档页（#131）：项目壳、「知识库」页签高亮、可返回知识库。工作流属于项目时，文档都在这里打开。
      path: '/projects/:projectId/docs/:workflowId/:docRef+',
      name: 'project-doc',
      component: FeedbackView
    },
    {
      // 老的按工作流的文档库整页：只服务还没归入项目的工作流；属于项目的工作流由下面的全局守卫替换到项目内（老链接不失效）。
      path: '/docs/:workflowId?/:docRef*',
      name: 'docs',
      component: FeedbackView
    },
    {
      path: '/settings',
      name: 'settings',
      component: () => import('../views/SettingsView.vue')
    },
    {
      path: '/oauth/consent',
      name: 'oauth-consent',
      component: () => import('../views/OAuthConsentView.vue')
    },
    {
      // ExecMesh：终端与模型执行。会话清单在左侧栏，右侧是终端页、模型线程页或主机页。
      path: '/exec',
      component: () => import('../views/exec/ExecLayout.vue'),
      children: [
        { path: '', name: 'exec', component: () => import('../views/exec/ExecTerminalView.vue') },
        { path: 't/:sid', name: 'exec-session', component: () => import('../views/exec/ExecTerminalView.vue') },
        { path: 'r/:key', name: 'exec-thread', component: () => import('../views/exec/ExecThreadView.vue') },
        { path: 'hosts', name: 'exec-hosts', component: () => import('../views/exec/ExecHostsView.vue') },
        { path: 'archive', name: 'exec-archive', component: () => import('../views/exec/ExecArchiveView.vue') }
      ]
    },
    // 旧地址：远程终端现在是 ExecMesh 模式的一部分。
    {
      path: '/terminal',
      redirect: '/exec'
    },
    {
      path: '/archive',
      name: 'archive',
      component: () => import('../views/ArchiveView.vue')
    },
    {
      path: '/:pathMatch(.*)*',
      redirect: '/'
    }
    // /oauth/consent 必须在 catch-all 之前（见上）
  ]
})

// 老文档地址 /docs/:工作流/:文档 → 查到工作流属于某个项目就替换成项目内的文档页（#131）；没有项目就留在老页。
// 用全局守卫而不是路由上的 beforeEnter：后者在只有参数变化时（从一个工作流的文档点到另一个）不会触发。
// 查询迟迟不回（3 秒）或失败都按「没有项目」处理——老文档页永远是兜底，入口不会因为这道守卫消失。
router.beforeEach(async to => {
  if (to.name !== 'docs') return true
  const wf = paramString(to.params.workflowId as string | string[] | undefined)
  if (!wf) return true
  const pid = await ownerProjectIdWithin(wf)
  if (!pid) return true
  return legacyDocRedirect({ params: to.params as Record<string, string | string[]>, query: to.query, hash: to.hash }, pid)
})

// 每个模式记住自己上次停留的位置，切换器据此回到离开时的页面。
router.afterEach(to => {
  useModeStore().remember(to.fullPath)
})

// RelayMesh ↔ ExecMesh 之间切换时的过渡动画（旧画面化作粒子消散）。
installModeTransition(router)

export default router
