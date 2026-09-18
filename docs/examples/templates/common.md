# 通用约束（示例模板片段）

> 这是一个 **include 片段**：它没有 frontmatter，整篇都是正文，由 `impl-batch.md`
> 用一条 include 指令（`include: common.md`）从**同一目录**拼进去——被 include 的文件
> 不再展开自己的 include，所以片段里写死一行 `include:` 也只是字面文字。
> 片段里的 `{{base}}` 与宿主模板共用同一套变量值。

- 改文件只用 apply_patch；不要用 PowerShell 双引号字符串写文件（反引号会被吃掉）。
- 文本文件 LF、UTF-8 无 BOM。
- 每个任务编号完成后**单独 `git commit`**（conventional commit），**不要 push**；提交前
  `git status --short` 确认无夹带。
- 禁止重启/reload 正在运行的 gofer server 或 worker（它们正在执行你这个 job）。
- 测试一律 `t.TempDir()`；临时起 serve 必须用临时 config 与随机端口，禁止指向真实配置目录。
- 判定命令成败同时看 exit code 与输出；`go test` 贴 `ok`/`FAIL` 原始行，不要只写"通过"。
- 发现设计或文档与代码事实冲突时**停下**，在汇报里写清冲突点与建议，不要自作主张扩范围。
- 基线：`{{base}}`（本批次从这条基线开始，验收也以它为准）。
