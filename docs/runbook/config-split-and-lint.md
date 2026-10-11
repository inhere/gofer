# 配置拆分与去重

server 的 `config.yaml` 用久了会变长：agents、projects 两段往往占大半，还夹着大量与默认值相同的字段。本文是整理它的操作步骤。设计见 [design](../design/2026-10-11-config-split-and-dedup-design.md)。

## 前提

server、所有 worker、各处 CLI 先升级到支持 `include` / `extends` 的版本，再修改配置。旧版本把这两个键当作未知键忽略：`include` 里的段读不到，`extends` 的 agent 只剩差异字段。

## 步骤（在 server 主机上做）

lint 的判定依赖本机与当前 gofer 版本的默认值，所以在 server 主机上执行，`-c` 指向 server 正在用的配置。

```bash
gofer config lint -c <config.yaml>          # 1. 看报告：可删的键、可改写成 extends 的 agent、未知键
gofer config lint --fix -c <config.yaml>    # 2. 删除可删的键（先备份 <file>.bak-<时间>，改完复核等价）
gofer config lint worker -c <worker.yaml>   # worker.yaml 同样可查（在该 worker 所在机器上执行），--fix 同理
#    3. 按报告里的 extends 建议手工改写 agent（报告给出了改写后的完整写法）
gofer config split -c <config.yaml>         # 4. agents / projects 搬到 config.d/（同样先备份、复核）
gofer config validate server -c <config.yaml>
gofer serve reload -c <config.yaml>         # 5. 让运行中的 server 读新文件；回执列出 files
```

- 每一步之后都可以 `gofer config validate` + `gofer serve reload` 确认；需要回退时用备份文件覆盖回去，再 reload。
- `--fix` 只删报告中标为可删的键，并且逐个删、每删一个都确认生效配置不变；删空的段（如 `log:`）一并去掉。其余文本、注释原样保留。
- 未知键（报告里的 `unknown key`）不会被自动删除，多半是写错了字段名，按报告的行号去修正。
- `extends` 建议在内置模板与你的写法有差别时，会把差别显式写出来（例如 `global_args: null`），保证改写后效果不变；你也可以去掉这些行，改为采用模板的值。

## 拆分后的日常

- 配置页（web）每段旁边标出所在文件，保存时写回该文件。
- `gofer config info` 列出所有文件及各自的段；`gofer config edit --key agents` 打开 agents 所在文件。
- 想再拆其他段：`gofer config split --keys supervisor,roles`（已有 `include` 时会追加条目）。
- 一个段只能在一个文件里；同一段出现在两个文件时加载报错并指出这两个文件。
