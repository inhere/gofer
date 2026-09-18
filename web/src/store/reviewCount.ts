// 待验收计数（REV-01 验收台徽标）：顶栏与 /review 页共用的一个数字。
//
// 唯一的轮询源是 EscalationBell——它本来就每 5s 拉一次待应答聚合，这里只把同一轮
// 拉到的 `stats.jobs.by_status.needs_review` 写进来（不另起定时器）；验收台在本地
// 裁决后按自己的列表长度修正，避免徽标比列表慢一拍。
import { ref } from 'vue'

export const needsReviewCount = ref(0)
