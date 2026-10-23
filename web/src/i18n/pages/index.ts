// 按页面拆分的文案在这里合并进主字典（index.ts）。新增页面文件时在 pages 里加一行。
import type { PageDict } from './types'
import { work } from './work'
import { sessions } from './sessions'
import { review } from './review'
import { mail } from './mail'
import { jobs } from './jobs'
import { plans } from './plans'
import { today } from './today'
import { workbench } from './workbench'

export const pages: PageDict[] = [today, jobs, plans, workbench, sessions, review, mail, work]
