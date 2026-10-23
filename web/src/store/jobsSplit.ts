// 任务页「并排 / 单栏」选择的单例（§16：记在本地）。列表页与详情宿主页共享一份，
// 在列表头切换后详情面板立刻跟着变或退回单栏，不需要刷新。
import { ref } from 'vue'
import { readSplitPreference, saveSplitPreference, type JobsLayoutPreference } from '../utils/jobsLayout'

export const jobsSplitPreference = ref<JobsLayoutPreference>(readSplitPreference())

export function setJobsSplitPreference(value: JobsLayoutPreference): void {
  jobsSplitPreference.value = value
  saveSplitPreference(value)
}
