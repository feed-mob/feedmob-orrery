# Orrery 目标形态

立项形态文档 · AI-2564 · 2026-09-08 初版

> **2026-09-13 定位确认（OQ-9 已关闭）**
>
> **Orrery 的目标是取代 GitHub Actions**，不是在它之上加一层。竞品调研曾提出"做治理层、不接管执行"的替代方案（《竞品优点合集》§4.2），**该方案已评估并不采纳**——决策依据是产品意图，不是证据不足。
>
> 调研中仍然成立、且已并入本文的修正：**L2 的 fork 目标从 `nektos/act` 改为 `gitea/runner` 的 `act/` 目录**（act master 自 2026-06-01 零提交，且按设计没有编排层）。见 §分层。
>
> §4.2 的墓碑证据不因此作废，但它影响的是**推广策略**而非定位——见下方「迁移成本墙怎么绕」。

---

## 先回答 Ken 悬着的那个问题

Ken 在 2026-09-03 的会上说的是"重新做一遍 / 重构一下 / 重写一遍"，工单的待确认 (2) 把这条挂了起来：**究竟落到"新做"还是"重构已有实现"**。

> ## 都不是。答案是：语法上兼容，实现上全新。

不是重构现有的 13 个 workflow——那是维护，Ken 已经把这类活划给小卢和 Cathy。也不是从零发明一套新 DSL——那会让 Ken 要的对比实验根本做不成，还得放弃整个 action 生态。

**Orrery 读得懂现有的 workflow 文件，但底下是我们自己的引擎。** 兼容语法换来两件事：现有 13 个 workflow 能直接迁过来，以及同一份文件可以在两个引擎上跑、用四项指标做硬对比——这正是 Ken 要的"能做比较"。全新实现换来的是：不被 GitHub 的设计债绑住，后续想往哪进化都行。

---

## 分层：真正要写的只有中间那一层

"重做 GitHub Actions"听起来是个巨大的工程，但拆开之后，最大的一块——解析 workflow YAML、求值表达式、在容器里按序跑步骤——已经有 MIT 开源做完了。Gitea 当年做自己的 Actions 就没重写这块，把 act 的代码 vendor 进自己的 runner。我们照做。

**但必须认清 (a)/(b) 分界**——这是调研最重要的一条修正：

| | 内容 | 谁提供 |
|---|---|---|
| **(a) runner 侧** | 步骤执行、容器、表达式求值、`uses:` 解析 | **复用** `gitea/runner` 的 `act/` |
| **(b) 服务端侧** | 并发组、超时、取消、审批、保留期、required 检查、重跑上限、任务分配 | **全部自建** |

act 的文档自己列明：`concurrency`、`timeout-minutes`、`permissions`、`environment`、取消、annotation、step summary **全部 "ignored"**——因为它没有服务端，结构上没地方放。

**而 (b) 恰好是我们全部四个痛点的所在**（超时缺失、消费上限、重复 deploy.yml、部署卡死）。所以"复用执行器"省下的是我们最不缺的那一半，(b) 一行都没省。这不改变要取代 GitHub Actions 的决定，只是把工作量说清楚。

> **2026-09-13 实现进度**：(a) 与 (b) 都已落地并端到端验证。act 文档里那串 "ignored"——
> `concurrency`、`timeout-minutes`、取消、step summary——现在由服务端提供：并发组默认开启、
> 超时 NOT NULL 有平台默认值、停止是"请求→确认→超时强杀"三态、步骤时间线落库。
> 触发（webhook / schedule / workflow_dispatch）与回写（commit status）也在服务端。
> 唯一还在 (b) 里没做的 P0 项是 `permissions:` 收窄 token——它需要先有 GitHub App
> 才能铸造收窄的凭据，是部署决策而不是代码缺口。详见 README 的能力表。

```
┌─────────────────────────────────────────────┐
│ L1  管道定义 · workflow 语法                  │  兼容
│     兼容 GitHub Actions —— 现有文件直接迁移      │
├─────────────────────────────────────────────┤
│ L2  执行器 · 解析与跑步骤                      │  复用
│     fork nektos/act（MIT · 71.8k★）—— 不重写   │
├═════════════════════════════════════════════┤
║ L3  运行协议 · runner 取活                     ║  ← Orrery
║     自定义 gRPC over HTTP —— 学 Gitea           ║     本体
║                                              ║
║ L4  调度核心 · 状态机/日志流/缓存/密钥            ║
║     全部自建 —— 全部工作量，全部差异化            ║
├═════════════════════════════════════════════┤
│ L5  算力 · 机器                               │  租用
│     自建 runner 跑在自己 AWS —— 算力是商品，不做  │
└─────────────────────────────────────────────┘
```

### 分层决策与依据

| 层 | 决策 | 依据 |
|---|---|---|
| 管道定义 | **兼容** | 迁移成本近零；对比实验必须同一份输入；不放弃 action 生态 |
| 执行器 | **复用** | **`gitea/runner` 的 `act/` 目录**（MIT）。~~`nektos/act`~~ master 自 2026-06-01 零提交、114 个开放 PR；`gitea/act` 已归档并 vendor 进 `gitea/runner`，后者两周内发五个版本 |
| 运行协议 | **自建** | GitHub 的 V2 Broker API 是私有契约，字段说改就改（Depot 已吃过亏） |
| 调度核心 | **自建** | 队列、依赖、矩阵、取消传播、日志流、缓存、密钥作用域——差异化全在这 |
| 算力 | **租用** | 自建 runner 在 GitHub 上仍免费；Earthly 因"算力是商品卖不动"关停云服务 |

---

## 路线：四个阶段

Ken 说"不用着急"，所以阶段划分的原则是**每一阶段结束时都有一个可演示、可对比的东西**，而不是憋一个大版本。前两阶段不碰差异化，只求"能跑"和"省钱"。

### P0 · 能力对齐

把 Orrery 跑起来：自定义协议的 runner 能注册、取活、在容器里跑完一个真实 workflow、把日志流式传回来。只求跑通，不求快，不求功能全。

**出口条件**：`fm-freewheel-beeswax-mcp/test.yml`（18 行）与 `feedmob-pages/ci.yml`（43 行）在 Orrery 上跑出与 GitHub Actions 一致的结果。

> 按"几乎所有基础功能"的要求，P0 的真实范围不止"跑通一个 workflow"，而是**跑通 workflow + 队列 + runner 注册 + 日志流**四件一起。另需包含 #47 状态回写——没有它 Orrery 挂不上现有 PR 流程。

### P1 · 成本接管

把 runner 挪到我们自己的 AWS spot 上，让 Orrery 和 GitHub Actions 共用同一批机器。这一阶段解决 Ken 说的"花到头儿了"，并且**不依赖 Orrery 成熟**——即使 Orrery 后续下马，这部分收益也留得住。

**出口条件**：GitHub 侧月度分钟数账单降到接近零；2026-08-19 那类消费上限阻断不再可能发生。

### P2 · 差异化楔子

从候选方向里**只选一个**做深，不要全做。候选都直接对着实测出来的痛点：

- 本地与生产同一引擎
- 内容寻址缓存
- **部署原语一等公民**（当前主场景）
- 默认安全（超时/权限/并发默认开、action 解析时钉 SHA）
- agent 治理（身份 + 成本 + 停止 + 台账）

**出口条件**：选定方向上，Orrery 明显优于 GitHub Actions，且能用数字说清楚好多少。

### P3 · Agent 中枢

Ken 真正要的那一层：一大堆智能体需要被调度时，Orrery 是中枢。这一阶段才引入 agent 专有语义——长任务、分级授权、人工介入点、可审计的执行沙箱。

**出口条件**：`adops-agent` 的每日 Hermes profile 同步完整跑在 Orrery 上，含自动开 PR。

---

## 验收：Ken 要的对比实验

自研版与现有 GitHub Actions 同时跑一组固定任务，比成功率、延迟、成本、可扩展性。任务集直接用我们自己的真实 workflow，按难度排成阶梯——这样任何一级卡住，都能立刻知道卡在什么能力上。

### 固定任务集

| # | workflow | 行数 | 考察的能力 |
|---|---|---|---|
| 1 | `fm-freewheel-beeswax-mcp / test.yml` | 18 | 冒烟：能不能跑完一个 job |
| 2 | `feedmob-pages / ci.yml` | 43 | 多 job、Python + Node 双工具链 |
| 3 | `feedmob-pixel-mcp / ci.yml` | 57 | uv 工具链、超时语义 |
| 4 | `mobius / build-image.yml` | 100 | Docker 构建、推 GHCR、镜像缓存 |
| 5 | `adops-agent / pull-remote-hermes-profile.yml` | 271 | cron 触发、agent 执行、自动开 PR——最接近 P3 形态 |
| 6 | `feedmob-pixel-dashboard / deploy.yml` | 681 | 多阶段 SSH 部署、矩阵、跨 job 依赖——最难 |

### 四项指标

| 指标 | 怎么量 | 及格线 |
|---|---|---|
| **成功率** | 同一 commit 连跑 20 次的通过率 | 不低于 GitHub Actions |
| **延迟** | 从触发到 job 开始执行的 P50 / P95 | P95 优于 GitHub Actions（无排队是结构性优势） |
| **成本** | 每次运行的机器成本 + 折算的月度总额 | 显著低于当前账单 |
| **可扩展性** | 并发跑满时的最大 job 数与退化曲线 | 能说清上限在哪、瓶颈是什么 |

> 延迟这一项值得单独说：GitHub 托管 runner 在高峰期排队是公认痛点，而我们自己的 spot 池没有别的租户来抢。这是 Orrery 最容易先拿下的一项。

---

## 边界：Ken 在会上划过的那条线

Ken 明确说过"别去抢人家那个小活儿"，简单维护归小卢和 Cathy。这条边界要写清楚，避免 Orrery 的立项被日常修 CI 的活稀释掉。

| Orrery 做 | Orrery 不做 | 归日常维护 |
|---|---|---|
| 运行协议与调度核心 | 算力本身——租，不自研 | 补 `timeout-minutes`（12 个缺） |
| 日志流、缓存、密钥作用域 | workflow 执行器——复用 act | 补 `concurrency`（7 个缺） |
| workflow 语法兼容层 | 新 DSL——兼容现有语法 | 补 `permissions`（6 个缺） |
| 自建 runner 的编排与伸缩 | Git 托管——继续用 GitHub | action 版本钉 SHA（27 处） |
| 与 GitHub Actions 的对比实验 | CD 制品仓库——继续用 GHCR | 修 `known_hosts` 部署失败 |
| agent 调度语义（P3） | | Node 20 弃用跟进 |

第三列这六项虽然不归 Orrery，但它们同时是 Orrery 的需求来源——"13 个 workflow 里 12 个没有超时上限"不是团队疏忽，是平台默认值的必然结果，而这正是 P2「默认安全」那个楔子要解决的。

---

## 能力边界：Orrery 接管不了什么

Orrery 能完整接管"跑任务"这件事，但接管不了"和 GitHub 长在一起"这件事。下面这些的本质不是功能，是**长在 GitHub 那一侧**：

- PR 上的 status check 与 required checks（合并门禁）
- Checks API 的行内注解（标到代码行上）
- `GITHUB_TOKEN` 的自动注入与权限模型
- Environments、部署保护规则、人工审批
- OIDC 联邦到 AWS/GCP 做无密钥部署

**在自建框架的前提下，这一组变成框架的职责**——代码从哪检出、PR/分支事件从哪来、状态回写给谁看、谁是这个 run 的发起人、UI。参见 #47 状态回写。

---

## 迁移成本墙怎么绕

取代 GitHub Actions 要正面对上 Earthly CI 的死因：客户反复说"**不值得换**"。创始人后来直说销售电话转化不了，而且把官网文案从 CI 改成 builds 后转化率翻倍——市场不接受"换一个 CI"这个提议。

Orrery 有三道天然的护城板，但要显式维持住：

| 护城板 | 做法 |
|---|---|
| **语法兼容** | 现有 13 个 workflow **一行不改**就能跑在 Orrery 上。这是与 Earthly 最大的差别——Earthly 要求用户重写 Earthfile，我们不要求 |
| **单一用户** | 我们是自己的唯一客户，不需要说服任何人"值得换"。Earthly 死于卖不动，这个死因对我们不适用 |
| **可退回** | 关掉 Orrery，13 个 workflow 回 GitHub Actions 照跑。这让"换"的风险接近零——因为随时能换回来 |

第三条要写成 **P0 的验收项**，不是口号：**关掉 Orrery，13 个 workflow 必须照跑。** 它同时兜住内部 bus factor（Airplane 那块碑的教训：关键内部工具不能变成只有一个人能维护的黑盒）。

> 迁移节奏建议按任务集的难度阶梯走：先迁 18 行的 `test.yml`，最后迁 681 行的 `deploy.yml`。任何一级迁不动，就停在那一级——已迁的继续用 Orrery，没迁的继续用 Actions，两边共存不冲突。

---

## 墓碑清单里仍然适用的部分

即使定位是取代，下面这些"不要做"仍然成立——它们约束的是**怎么做**，不是**做不做**：

| 不做 | 为什么仍适用 |
|---|---|
| 把价值主张写成"跑得更快 / 构建更可靠" | 执行是商品（Earthly、BuildJet）。Orrery 的卖点是治理与确定性，不是速度 |
| 拖拽式 workflow 画布 | OpenAI 自己 14 个月就放弃了 Agent Builder。编排真相住在仓库的文件里 |
| 技能 / 插件市场 | OpenClaw ClawHavoc：2,857 个技能里 341 个恶意 |
| 绑死单一聊天平台的命令入口 | Hubot 死于 Slack 抽走 RTM 底座。命令层必须平台无关 |
| 通用"传感器/规则/动作"元模型 | StackStorm 的抽象成本要等规则足够多才回本，我们到不了那个规模 |
| 第五个自研编码 agent 运行时 | 这一层被基础模型厂商的官方 CLI 吃掉了。Orrery 调度它们，不做第五个 |

---

## 待 Ken 拍板

| | 问题 | 说明 |
|---|---|---|
| **OQ-1** | geng 那份实现要不要并行对比 | 对应工单待确认 (3)。若确有另一份实现，对比应是三方而非两方，任务集与指标口径需提前对齐。另需加第三基线："什么都不做——在自建 runner 上跑 gh-aw" |
| **OQ-2** | 50–60 美元/月需要核实 | 对应待确认 (5)。"消费上限被顶穿"已确认是事实（2026-08-19 annotation 原文可查），但拿不到实际发票，账单接口需 `admin:org` + `user` scope。另需扩展到 **LLM token 支出**——真咬人的上限是 token，LiteLLM 已握有数据 |
| ~~OQ-3~~ | ~~P2 的楔子选哪一个~~ | **已关闭**：定位既定为取代，P2 不再是"选楔子"，而是把 (b) 服务端侧做到能接管现有 workflow |
| **OQ-4** | 兼容到哪一层（**取代定位下优先级升高**） | 只兼容语法，还是也跑 GitHub 生态的 action？兼容 `actions/checkout@v4` 这类意味着运行时要去 GitHub 拉第三方代码，迁移成本最低但引入外部依赖与供应链面。预期答案：SHA 钉住、allowlist 的小型 action 镜像私服（Gitea/Forgejo 做法 + tj-actions 教训） |
| **OQ-5** | Orrery ↔ Mobius 边界 | Orrery 是否是 Mobius 委派背后的执行器，以 Mobius 的 HTML 尾标为运行契约？ |
| **OQ-6** | durable 内核自写 vs 嵌入 | L4 调度核心是手写状态机，还是嵌入 Hatchet（MIT）/ Temporal（MIT）/ DBOS？ |
| **OQ-7** | 与现有 agent 运行时的关系 | 与 Claude Code Routines / Codex Automations / Hermes cron 是互操作还是替代？ |
| **OQ-8** | 非工程师入口做在哪 | Slack？Mobius 委派？自建 UI？ |

---

## 命名

**Orrery（天仪）** —— 太阳系仪，一组齿轮驱动多个各自周期不同的天体，运转可预测、可推演。隐喻对应实际形态：8+ 个仓库和服务各有各的触发周期（push / PR / cron），需要一个中枢精确带动。

站得进现有命名族（Mobius、Parallax、Hermes、Aura、Blindspot——真实存在的器械或现象，非生造词）。中文「天仪」呼应张衡浑天仪。

已核实 GitHub 无领域内撞名（815 个同名 repo，最大仅 155★）。查名时否掉了两个：**Cadence**（`cadence-workflow/cadence` 9,435★，Uber 的工作流编排引擎，同领域撞名）与 **Talos**（`siderolabs/talos` 11,150★，K8s 操作系统）。
