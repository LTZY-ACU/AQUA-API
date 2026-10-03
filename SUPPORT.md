# 获取支持

## 一、先自助（多数问题这里就有答案）

1. [README](README.md) 的「常见问题」章节 —— 覆盖了最常见的坑；
2. 部署与配置问题看 README 的「快速开始」与「配置」；
3. 报错先看服务状态与日志：

```bash
curl -s http://127.0.0.1:8787/healthz     # 服务是否就绪（含迁移版本）
systemctl status aqua-api                 # systemd 部署
journalctl -u aqua-api -n 200 --no-pager  # 最近的日志
```

`/healthz` 返回 503 通常是数据库不可用；SQLite 场景优先检查数据目录权限。

## 二、再提问（按类型选通道）

| 类型 | 去哪里 |
| --- | --- |
| Bug 报告 | [开 Issue](https://github.com/LTZY-ACU/AQUA-API/issues/new)（请用模板填写） |
| 功能建议 | 开 Issue 并标注 `enhancement` |
| 使用问题 / 交流讨论 | **QQ 群 1103667832** |
| 安全漏洞 | **不要开公开 Issue**，见 [SECURITY.md](SECURITY.md) |
| 贡献代码 | 见 [CONTRIBUTING.md](CONTRIBUTING.md) |

## 三、提问时请附上

- 版本号（`/healthz` 的 `version`）与部署方式（二进制 / Docker / 源码）；
- 最小复现步骤；
- 相关日志或 HTTP 响应 —— **务必抹掉令牌与密钥**。

信息不全的 Issue 往往要多轮往返才能定位，附上这些能显著加快处理。

## 四、响应预期

本项目由维护者利用业余时间维护，不承诺 SLA。但**每一个 Issue 都会被看到**；
重复问题会被合并或关闭，并给出指向已有答案的链接。
