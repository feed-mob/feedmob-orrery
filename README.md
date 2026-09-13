# Orrery（天仪）

> **orrery** /ˈɔːrəri/ — 太阳系仪。一组精密齿轮驱动的天体模型，每个星体按自己的周期运行，全部由同一套传动机构带动，运转可预测、可推演。

FeedMob 内部自动化平台。**agent 是一等公民，全员使用。**

一句话定位：**兼容 GitHub Actions 语法、实现全新的事件驱动执行引擎。** 给未来一大堆智能体用的中枢，先从接住我们自己 8 个仓库开始。

追踪工单：[AI-2564](https://mobius.feedmob.com/issue/AI-2564)

---

## 为什么做

Ken 在 2026-09-03 的 AI 重复会上提出：GitHub Actions 是什么、为什么大家都在用、能不能重新做一遍。出发点是两层——公司已经在 GitHub Actions 上撞到成本上限，而团队即将有一大堆 agent 需要被驱动，GitHub Actions 是中枢。

调研之后，"重做"的确切答案是：

> **语法上兼容，实现上全新。**

- **不是重构现有 13 个 workflow** —— 那是维护
- **不是从零发明新 DSL** —— 那会让对比实验做不成，还要放弃整个 action 生态

Orrery 读得懂现有的 workflow 文件，但底下是我们自己的引擎。兼容语法换来两件事：现有 workflow 直接迁移，以及同一份文件能在两个引擎上跑、用四项指标做硬对比。全新实现换来的是不被 GitHub 的设计债绑住。

---

## 架构：五层里只有中间两层是我们写的

| 层 | 决策 | 依据 |
|---|---|---|
| **L1** 管道定义（workflow 语法） | 兼容 | 迁移成本近零；对比实验必须同一份输入 |
| **L2** 执行器（解析 + 跑步骤） | 复用 ⚠ | ~~`nektos/act`~~ → **`gitea/runner` 的 `act/` 目录**（MIT）。调研推翻了原判断，见下方结论 1 |
| **L3** 运行协议 | **自建** | GitHub 的 V2 Broker API 是私有契约，字段说改就改 |
| **L4** 调度核心（状态机 / 日志流 / 缓存 / 密钥） | **自建** | 全部工作量与全部差异化都在这层 |
| **L5** 算力 | 租用 | 自建 runner 在 GitHub 上仍免费；Earthly 因"算力是商品卖不动"关停云服务 |

上面兼容、下面租用、执行器复用——这条边界把季度级工程压回可控规模。

---

## 阶段

| | 阶段 | 出口条件 |
|---|---|---|
| **P0** | 能力对齐 | `fm-freewheel-beeswax-mcp/test.yml`(18行) 与 `feedmob-pages/ci.yml`(43行) 跑出与 GH Actions 一致的结果 |
| **P1** | 成本接管 | GitHub 侧月度分钟账单降到接近零；消费上限阻断不再可能 |
| **P2** | 差异化楔子 ⚠ | 四选一做深。调研证据指向「治理与确定性层」，见下方结论 3 |
| **P3** | Agent 中枢 | `adops-agent` 每日 Hermes profile 同步完整跑在 Orrery 上（含自动开 PR） |

**P1 的价值独立于 Orrery 成败**——即使后续下马，自建 runner 省下的成本也留得住。

---

## 验收：同一份文件，两个引擎，四项指标

任务集按难度阶梯，全用我们自己的真实 workflow：

| # | workflow | 行数 | 考察 |
|---|---|---|---|
| 1 | `fm-freewheel-beeswax-mcp/test.yml` | 18 | 冒烟 |
| 2 | `feedmob-pages/ci.yml` | 43 | 多 job、Python + Node 双工具链 |
| 3 | `feedmob-pixel-mcp/ci.yml` | 57 | uv 工具链、超时语义 |
| 4 | `mobius/build-image.yml` | 100 | Docker 构建、推 GHCR、镜像缓存 |
| 5 | `adops-agent/pull-remote-hermes-profile.yml` | 271 | cron + agent + 自动开 PR（最接近 P3） |
| 6 | `feedmob-pixel-dashboard/deploy.yml` | 681 | 多阶段 SSH 部署、矩阵、跨 job 依赖（最难） |

四项指标：**成功率**（同 commit 连跑 20 次）、**延迟**（触发到开始执行的 P50/P95）、**成本**（每次 + 折算月度）、**可扩展性**（并发上限与退化曲线）。

---

## 边界

**归 Orrery**：运行协议、调度核心、语法兼容层、runner 编排、对比实验、agent 调度语义。

**归日常维护**：补 `timeout-minutes`(12个缺)、`concurrency`(7个缺)、`permissions`(6个缺)、action 钉 SHA(27处)、修 `known_hosts` 部署失败、Node 20 弃用跟进。

这些虽不归 Orrery，但同时是它的需求来源——"12/13 没有超时上限"不是团队疏忽，是平台默认值的必然结果。

---

## 竞品调研结论（2026-09-12）

55 个产品、19 维、158 张优点卡、14 块墓碑。完整见 [`research/competitor-collection.md`](research/competitor-collection.md)，证据台账 [`research/claims.jsonl`](research/claims.jsonl)。

**三条改变章程的发现：**

1. **`nektos/act` 已停更，且按设计没有编排层。** master 自 2026-06-01 零提交；`concurrency`/`timeout-minutes`/`permissions`/取消/annotation 全部 "ignored"。兼容 GitHub Actions 天然分两半——runner 侧（步骤执行）与服务端侧（并发、超时、取消、审批），**而服务端侧恰好是我们全部四个痛点的所在**。正确的 fork 目标是 `gitea/runner` 的 `act/` 目录（MIT，两周内发了五个版本）。

2. **55 个产品里只有 8 个通过 agent-first 五问**，且 durable execution / 工作流自动化 / 部署三个品类的领头羊**全部不通过**。agent 治理这一层没有现成占位者。

3. **墓碑指向同一个结论**：Orrery 应是 GitHub Actions 之上的**治理与确定性层**，不是它的替代品。Earthly CI 死于迁移成本墙；Drone、Codefresh 被套件吃掉；BuildJet 被平台原生化挤死。判断标准：**如果这个功能写在 GitHub 的 roadmap 上是合理的，Orrery 就不该建它。**

**新增硬约束**（来自 Airplane.dev 那块碑）：**Orrery 挂掉时，8 个仓库必须仍能用原生 GitHub Actions 部署。**

---

## 仓库结构

```
docs/
  charter.md               目标形态：分层决策、四阶段、验收、开放问题
  feature-inventory.md     76 项功能盘点与对标（GitHub Actions 逐项核对 + 竞品签名功能）
research/
  rubric.md                19 维评估标尺（从 FeedMob 自己的 agent 原语反推）
  ci-engine-research.md    前置调研：CI/CD 分层格局、runner 协议、经济账
  competitor-collection.md 竞品优点合集（按维度组织，每条带证据等级）
  claims.jsonl             证据台账（可查询，供 agent 使用）
  plan.md                  研究计划
```

---

## 待拍板

| | 问题 |
|---|---|
| **OQ-1** | geng 那份实现要不要并行对比？ |
| **OQ-2** | 50–60 美元/月需要核实（账单接口需 `admin:org` + `user` scope） |
| **OQ-3** | P2 的楔子选哪一个：agent 治理 vs 部署原语 |
| **OQ-4** | 兼容到哪一层：只兼容语法，还是也跑 GitHub 生态的 action |
| **OQ-5** | Orrery ↔ Mobius 边界：Orrery 是否是 Mobius 委派背后的执行器 |
| **OQ-6** | durable 内核自写 vs 嵌入（Hatchet / Temporal / DBOS） |
| **OQ-7** | 与 Claude Code Routines / Codex Automations / Hermes cron 是互操作还是替代 |
| **OQ-8** | 非工程师入口做在哪 |
