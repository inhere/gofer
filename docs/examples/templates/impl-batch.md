---
desc: 一个批次的实施任务书（示例）
agent: omp
timeout_sec: 3600
verify: [go, test, ./...]
vars:
  tasks:
    required: true
    desc: 本批次的任务正文（markdown）
  base:
    default: main
    desc: 验收基线（写进通用约束里）
---

# 实施批次

{{include: common.md}}

## 本批次任务

{{tasks}}

## 交付与汇报

- 每个任务编号单独提交（conventional commit），汇报里给出提交 hash。
- 汇报末尾贴 `go test` 的 `ok`/`FAIL` 原始行，以及 `git status --short`。
- 未完成/跳过的项如实列出，不要用"已通过"掩盖没跑的命令。
