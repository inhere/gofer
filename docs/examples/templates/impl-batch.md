---
desc: 一个批次的实施任务书（通用示例；由 plan todo 派发时自动填 plan/todo 文本）
timeout_sec: 3600
vars:
  tasks:
    default: ""
    desc: 额外的任务正文（可选；`{{todo_title}}`/`{{todo_note}}` 之外的补充，markdown）
  base:
    default: main
    desc: 验收基线（写进通用约束里）
  verify_cmd:
    default: "<项目的验收命令>"
    desc: 本批次的验收命令（测试 / lint / 构建），按项目填写
---

# {{plan_title}}

{{plan_description}}

{{include: common.md}}

## 本批次任务

{{todo_title}}

{{todo_note}}

{{tasks}}

## 交付与汇报

- 按项目的提交规则提交，汇报里给出提交 hash。
- 汇报末尾贴 `{{verify_cmd}}` 的原始输出，以及 `git status --short`。
- 未完成/跳过的项如实列出，不要用"已通过"掩盖没跑的命令。

> 这份任务书可给 **plan todo 派发**用：`{{plan_title}}` / `{{plan_description}}` /
> `{{todo_title}}` / `{{todo_note}}` / `{{todo_id}}` 由派发该项的 server 从它自己的 plan/todo 里
> 解析，所以一份模板服务整个 plan 的每一项：
>
> ```bash
> gofer plan create --title "xxx 改造" --project <项目>
> gofer plan add-todo <plan-id> "步骤1: 数据模型迁移" --note "先跑迁移，再补索引"
> gofer plan set-todo <todo-id> --status ready --template impl-batch   # 立刻出 job，正文含上面的 plan/todo 文本
> ```
>
> 直接 `job run -t impl-batch --var tasks="…" --var verify_cmd="…"`（不挂 todo）时这几个内置变量渲染成空并告警，
> 任务正文就由 `--var tasks` 给——两种用法共用这一份模板。
>
> 使用方式：把这两个文件拷到项目的 `<项目根>/.gofer/templates/`（只对该项目生效）或
> server 的 `<config-dir>/templates/`（对所有项目生效，慎用），再按项目的规则改写。
