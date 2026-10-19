// 顶栏导航的唯一出处：App.vue 的主导航行、顶栏「更多 ▾」下拉与窄屏抽屉都从这里取。
//
// 一行放不下全部导航项（原来「工作」9 项 + 「配置」5 项 + 设置，会把右侧按钮挤掉），所以分成三份：
//  - navPrimary：最常看的口子，一行 6 项，顺序固定；
//  - navMoreGroups：「更多 ▾」下拉里的分组（组标题照样显示，见 components/NavMore.vue）；
//  - drawerNavGroups：窄屏抽屉的完整分组列表（主导航 + 下拉里的项 + 信箱），抽屉不做下拉。
// N3 §6 的两组语义不变：「工作」是天天看的，「配置」是配好后很少再看的对象。路由都不变
// （任务就是 /board 的 job 列表，只改文案，避免与工作项的状态分列混淆）。文案统一取
// utils/uiText.ts 的 NAV（导航、页面标题共用一份，见该文件头注释）。
import { NAV } from './uiText'

export interface NavItem {
  to: string
  label: string
}

export interface NavGroup {
  label: string
  items: NavItem[]
}

// 主导航行：这 6 项直接铺在顶栏上（抽屉的「工作」组也以它们开头）。
export const navPrimary: NavItem[] = [
  { to: '/today', label: NAV.today },
  { to: '/workbench', label: NAV.workbench },
  { to: '/work', label: NAV.work },
  { to: '/plans', label: NAV.plans },
  { to: '/board', label: NAV.jobs },
  { to: '/sessions', label: NAV.sessions },
]

// 「更多 ▾」：不天天点、但仍是导航项的页面。分组标题与原来的导航一致。
export const navMoreGroups: NavGroup[] = [
  {
    label: '工作',
    items: [
      { to: '/issues', label: NAV.issues },
      { to: '/dashboard', label: NAV.dashboard },
    ],
  },
  {
    label: '配置',
    items: [
      { to: '/agents', label: NAV.agents },
      { to: '/runners', label: NAV.runners },
      { to: '/projects', label: NAV.projects },
      { to: '/workflows', label: NAV.workflows },
      { to: '/schedules', label: NAV.schedules },
    ],
  },
]

// 窄屏抽屉：完整分组列表。信箱（design 2026-10-16）只在这里还是导航项——顶栏上它是右侧
// 「随手记」旁边的信封按钮（components/MailButton.vue），不再占主导航一行。
export const drawerNavGroups: NavGroup[] = [
  { label: '工作', items: [...navPrimary, { to: '/mail', label: NAV.mail }, ...navMoreGroups[0].items] },
  { label: '配置', items: navMoreGroups[1].items },
]

// 「更多」的激活态：当前路由落在下拉内任一项上（含子页，如 /agents/<id>）。
export function isNavMoreActive(path: string, groups: NavGroup[] = navMoreGroups): boolean {
  return groups.some((g) => g.items.some((i) => path === i.to || path.startsWith(`${i.to}/`)))
}
