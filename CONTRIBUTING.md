# 贡献指南

感谢你愿意为 AQUA-API 贡献代码。请先阅读 [README.md](README.md) 了解项目定位，
再按本指南参与。

## 一、协作模式（Fork + Pull Request）

本仓库采用 **fork 分支协作制**，两条约定构成全部规则：

1. **`main` 分支受保护**：只有维护者可以直接推送。任何人（包括贡献者）
   都不能直接向 `main` 提交，这保证主分支始终可编译、可回滚。
2. **贡献在 fork 里进行**：你在自己的仓库副本里可以开任意数量的分支
   （`feature/xxx`、`fix/xxx`，命名不限），自由开发自己的版本；
   想把改动合入主仓库时，通过 Pull Request 提交，经审查后合并。

```
你的 fork                       主仓库（受保护）
  main ──┬── feature/xxx ──►  PR #N ──► 审查 ──► 合入 main
         └── fix/yyy     ──►  PR #M
```

操作步骤：

```bash
# 1) 在网页上 fork 本仓库，然后：
git clone https://github.com/<你的用户名>/AQUA-API.git
cd AQUA-API
git remote add upstream https://github.com/LTZY-ACU/AQUA-API.git

# 2) 从最新 main 拉出工作分支
git fetch upstream
git checkout -b feature/你的功能名 upstream/main

# 3) 开发、验证（见第三节）、提交、推送
git push origin feature/你的功能名

# 4) 在网页上向 upstream/main 发起 Pull Request
```

## 二、提交规范

- 提交信息遵循 Conventional Commits（类型：`feat` / `fix` / `refactor` / `docs` /
  `test` / `chore` / `perf` / `style`，范围：模块名），一句话说明做什么。
- **小步提交**：一个可独立描述的改动一次提交，禁止把无关改动混在一起；
  严禁把很多天的开发攒成一次大提交——完整的提交时间线是本项目
  创作过程的证据链（见 [NOTICE](NOTICE)），squash 会破坏它。
- PR 请**不要 squash 合并**，保留你原有的提交序列。

## 三、验证要求

提 PR 前必须通过：

```bash
go build ./...
go test ./...

cd web && npm run type-check && npm run build
```

涉及数据库结构的改动必须附带迁移脚本（`internal/store/migrations/sqlite/`
下递增编号），并保证迁移幂等。

## 四、注释与代码风格（工程化注释）

本项目对注释的要求是**描述性**的，只回答两个问题：
"这个文件/函数做什么"与"这行代码为什么这样写（技术原因）"。

- 不写个人感想、情绪、口号与署名性内容；
- 注释语言为中文，专有名词保留英文；
- 每个源文件头部说明：模块职责、数据流转、扩展点；
- 与规则不符的 PR 会被要求修改注释后再合并。

## 五、报告问题

- Bug 与安全漏洞：优先开 Issue，模板见 `.github/ISSUE_TEMPLATE.md`；
  安全问题请勿在公开 Issue 中附可利用细节，写明"已复现 + 影响面"即可。
- 功能建议：开 Issue 并标注 `enhancement`。

## 六、行为边界

使用本项目与参与贡献均视为接受 [DISCLAIMER.md](DISCLAIMER.md)
与 [TRADEMARK.md](TRADEMARK.md)。贡献内容不得包含违法违规功能、
第三方专有代码或未经授权的接口实现。
