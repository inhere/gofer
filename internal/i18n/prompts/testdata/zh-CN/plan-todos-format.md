输出格式：方案末尾附一个 ```gofer-todos``` 围栏块，内容是 YAML 列表，每个元素一步：
- `title`：步骤标题（必填）；
- `after`：依赖的前序步骤（标题或从 1 起的序号，可写列表）；省略 = 依赖上一步，`[]` = 无依赖；
- `acceptance`：验收标准；
- `check_prompt`：交付核验要求（可选；不是 shell 命令）；
- `scope`：改动范围（路径 glob 列表，相对仓库根）；
- `check`：检查点命令（可选；生成一个 exec 复核项，后续步骤等它通过）。

```gofer-todos
- title: 存储层加字段与迁移
  acceptance: 迁移可重复执行；旧数据可读
  scope: [src/storage/**]
- title: 接口暴露新字段
  scope: [src/api/**]
  check: <项目的测试命令>
```