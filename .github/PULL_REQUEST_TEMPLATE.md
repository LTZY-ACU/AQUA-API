<!--
  Pull Request 模板：发 PR 时默认填入（GitHub）。
  合并方式约定见 CONTRIBUTING.md 第二节：保留提交序列，不 squash。
-->

### 变更说明

<!-- 这个 PR 做了什么、为什么这么做（一段话）。 -->

### 关联 Issue

<!-- 例如：Closes #12。没有关联可留空。 -->

### 自查清单

- [ ] `go build ./...` 通过
- [ ] `go test ./...` 通过
- [ ] `cd web && npm run type-check && npm run build` 通过
- [ ] 涉及数据库结构时已附迁移脚本（幂等）
- [ ] 注释只描述代码行为，无个人感想
- [ ] 提交信息遵循 Conventional Commits，且未混入无关改动

### 风险与回滚

<!-- 可选：本次改动可能影响的行为面，以及如何快速回滚。 -->
