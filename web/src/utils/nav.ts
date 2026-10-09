// 主导航分组（App.vue 顶栏与窄屏抽屉共用）。
export interface NavItem {
  to: string
  label: string
}

// N3 §6 导航两组：「工作」是天天看的，「配置」是配好后很少再看的对象。路由都不变
// （Jobs 就是 /board 的 job 列表，只改文案，避免与 Works 的状态分列混淆）。
export const navGroups: Array<{ label: string; items: NavItem[] }> = [
  {
    label: '工作',
    items: [
      { to: '/today', label: '今天' },
      { to: '/workbench', label: '工作台' },
      { to: '/work', label: 'Works' },
      { to: '/plans', label: 'Plans' },
      { to: '/board', label: 'Jobs' },
      { to: '/sessions', label: 'Sessions' },
      { to: '/issues', label: 'Issues' },
      { to: '/dashboard', label: 'Dashboard' },
    ],
  },
  {
    label: '配置',
    items: [
      { to: '/agents', label: 'Agents' },
      { to: '/runners', label: 'Runners' },
      { to: '/projects', label: 'Projects' },
      { to: '/workflows', label: 'Workflows' },
      { to: '/schedules', label: 'Schedules' },
    ],
  },
]
