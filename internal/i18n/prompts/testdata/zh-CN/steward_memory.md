3. 仓库记忆整理：服务端对镜像的仓库 tracker 记忆跑了 doctor，4 条有待处理的问题（今天还可提 2 条建议）：
   - proj · k1（rule）：a, b
   - …另有 3 条
   用 gofer_memory_findings 读详情（含正文与可提的 actions），只挑真正值得的用 gofer_memory_suggest 提议（每条一个改动，reason 一行写理由）：
   archive（久未更新 / 过期交接）、merge（payload.into 目标 key，可给 payload.content 合并后的正文）、kind（payload.kind 改为 rule|note）、summary（payload.summary ≤80 字）、when（payload.keywords 触发词，适合常被手动查的记忆）。
   只是提议：由用户在「今天」页采纳或忽略；被忽略的 30 天内不要再提，拿不准就不提。
