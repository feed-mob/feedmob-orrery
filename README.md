# Orrery（天仪）

> **orrery** /ˈɔːrəri/ — 太阳系仪。一组精密齿轮驱动的天体模型，每个星体按自己的周期运行，全部由同一套传动机构带动，运转可预测、可推演。

FeedMob 内部自动化平台。**agent 是一等公民，全员使用。**

一句话定位：**取代 GitHub Actions**——兼容它的语法，换掉它的引擎。 给未来一大堆智能体用的中枢，先从接住我们自己 8 个仓库开始。

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
| **L2** 执行器（解析 + 跑步骤） | 复用 | **`gitea/runner` 的 `act/` 目录**（MIT）。仅覆盖 (a) runner 侧；(b) 服务端侧全部自建 |
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
| **P2** | 服务端侧补全 | 并发组、超时、取消、审批、保留期、required 检查——act 全部 "ignored" 的那些 |
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

55 个产品、19 维、158 张优点卡（**61 张 A 级**）、**20 条被用户证据推翻的文档声称**、14 块墓碑。完整见 [`research/competitor-collection.md`](research/competitor-collection.md)，证据台账 [`research/claims.jsonl`](research/claims.jsonl)。

**三条改变章程的发现：**

1. **`nektos/act` 已停更，且按设计没有编排层。** master 自 2026-06-01 零提交；`concurrency`/`timeout-minutes`/`permissions`/取消/annotation 全部 "ignored"。兼容 GitHub Actions 天然分两半——runner 侧（步骤执行）与服务端侧（并发、超时、取消、审批），**而服务端侧恰好是我们全部四个痛点的所在**。正确的 fork 目标是 `gitea/runner` 的 `act/` 目录（MIT，两周内发了五个版本）。

2. **55 个产品里只有 8 个通过 agent-first 五问**，且 durable execution / 工作流自动化 / 部署三个品类的领头羊**全部不通过**。agent 治理这一层没有现成占位者。

3. **墓碑指向同一个结论**：Orrery 应是 GitHub Actions 之上的**治理与确定性层**，不是它的替代品。Earthly CI 死于迁移成本墙；Drone、Codefresh 被套件吃掉；BuildJet 被平台原生化挤死。判断标准：**如果这个功能写在 GitHub 的 roadmap 上是合理的，Orrery 就不该建它。**

4. **20 条文档级声称被用户证据推翻**，其中几条正好落在原本准备抄的设计上——`LiteLLM` 的 `max_budget_per_session` **不是硬闸**（准入不预留，并发请求双双放行）、`gh-aw` 的 AI Credits 定价**会间歇性整个失败**、`Argo CD` 的 rollback 在默认 auto-sync 下**不可用**。见 [§2.5](research/competitor-collection.md)。

> 方法论：这 20 条**没有一条是靠读文档能发现的**。抄任何机制之前，先去它的 issue 区按 reactions 排序翻前 30 条——文档写的是设计意图，issue 区写的是它实际怎么坏的。

**新增硬约束**（来自 Airplane.dev 那块碑）：**Orrery 挂掉时，8 个仓库必须仍能用原生 GitHub Actions 部署。**

---

## 跑起来

```bash
go build -o bin/ ./cmd/...
export ORRERY_REGISTRATION_TOKEN=$(openssl rand -hex 16)

# 密钥只在派发时注入进程与容器，从不落库
printf 'GITHUB_TOKEN=%s\n' "$(gh auth token)" > secrets.env && chmod 600 secrets.env

./bin/orrery-server -db orrery.db -secrets secrets.env &     # 控制面
./bin/orrery-runner \
  -labels 'self-hosted:host,ubuntu-latest:docker://catthehacker/ubuntu:act-22.04' &

./bin/orrery submit examples/with-actions.yml --wait   # checkout + setup-node，跑在容器里
./bin/orrery submit examples/hello.yml --wait          # 4 个 job 的 DAG，跑在宿主机
./bin/orrery runs
./bin/orrery run <run-id>                     # 含每一步的结果、耗时与日志区间
./bin/orrery logs <job-id>
./bin/orrery stop <job-id>                    # 请求停止；runner 收尾后确认
```

```bash
# 手动触发一次部署：参数在 run 创建之前就校验，错了不会有 job 起来
./bin/orrery dispatch feed-mob/app deploy.yml --ref main \
  --input environment=production --input version=v1.2.3 --wait
```

`submit` 默认从 workflow 所在的 git 检出读取 `repo` / `ref` / `sha`——这三个值直接变成
`github` 上下文，`actions/checkout` 按字面使用它们。`-forge-url` 指向代码所在的
forge（默认 `https://github.com`），它和 runner 的 `-actions-url`（`uses:` 从哪解析）
是两个独立问题。

### P0 已经能做什么

| | 状态 |
|---|---|
| workflow 解析（`jobs` / `needs` / `runs-on` / `timeout-minutes` / `env` / `run` 步骤） | ✅ 含依赖环检测与未知 `needs` 校验 |
| runner.v1 协议（`Register` / `Declare` / `FetchTask` / `UpdateTask` / `UpdateLog`） | ✅ Connect 风格 JSON over HTTP |
| 调度：`needs` DAG、标签匹配、原子抢占 | ✅ 上游失败时下游标 `skipped` 而非永久阻塞 |
| 日志流：增量提交 + 服务端 ack 定义投递 | ✅ 重复窗口幂等，跳跃窗口被拒 |
| **带确认的停止**：请求 → 确认 → 超时强杀 | ✅ 台账区分 `cleanup_ran=true/false`；被打断的步骤记 `cancelled` 而非 `failure` |
| 平台级默认超时 | ✅ 作者可下调，不可遗漏 |
| **并发组**（#17） | ✅ `concurrency` / `cancel-in-progress`；**平台默认开启**，见下 |
| **`uses:` action** | ✅ `actions/checkout@v4` + `actions/setup-node@v4` 已端到端跑通 |
| **容器执行** | ✅ `ubuntu-latest:docker://…` 起容器；`self-hosted:host` 跑宿主机；同一 runner 兼顾 |
| 表达式、`::group::`、`::error::`、矩阵、`services`、composite action | ✅ 由 act 承担（见 L2） |
| 步骤时间线（名称 / 结果 / 耗时 / 日志区间） | ✅ 落库并在 CLI 展示 |
| 密钥注入与作用域 | ✅ 派发时注入，不落库；日志只打印密钥名 |
| **git 事件触发**（#1 #36 #38） | ✅ 签名校验的 GitHub webhook；按 delivery id 幂等；`branches` / `tags` / `paths` / `types` 过滤 |
| **定时触发**（#2） | ✅ `on: schedule`，从默认分支注册；重叠由并发组接管——GitHub 根本没有重叠策略 |
| **带参手动触发**（#3） | ✅ `workflow_dispatch` inputs：required / default / choice / boolean / number 全部在 run 创建前校验 |
| **`github.event`** | ✅ 事件原文入库并注入，`${{ github.event.pull_request.number }}` 可用 |
| **跨 job 传值**（#40） | ✅ `needs.<job>.outputs.*` 与 `needs.<job>.result` |
| **状态回写**（#47） | ✅ Commit Status API，`orrery / <workflow>`；入队 pending、落定终态 |
| **产物与缓存**（#27 #28） | ✅ runner 内置产物与缓存服务端；跨 job 传产物已验证。**必须钉 v3**，见下 |
| **job 级 `if:`** | ✅ `always()` / `failure()` / `cancelled()` 与任意表达式，用 act 自己的解释器求值 |
| `permissions:` 收窄 token（#45） | ⛔ 需要先有 GitHub App 才能铸造收窄的 token |

### 接到 GitHub 上

```bash
./bin/orrery-server -db orrery.db \
  -forge-token "$(gh auth token)" \        # 读 workflow 文件、回写状态
  -webhook-secret "$(openssl rand -hex 32)" \
  -public-url https://orrery.example.com \
  -secrets secrets.env
```

仓库 Settings → Webhooks 里指向 `https://…/api/webhooks/github`，content type
`application/json`，secret 填同一个值，勾选 push / pull request。

- **触发**：webhook 到达 → 验签 → 按 delivery id 去重 → 在触发的那个 commit 上
  读 `.github/workflows/` → 每个 `on:` 匹配的 workflow 起一个 run。
  `pull_request` 读的是 PR head 的 workflow，所以 PR 可以改自己的 CI；
  `pull_request_target` **不接受**（那是 pwn-request 向量，见 feature-inventory #39）。
- **定时**：push 到默认分支时，把该仓库 workflow 里的 `on: schedule` cron 注册下来
  （只认默认分支——feature 分支上加的 cron 在合并前不该开始跑，否则"审一次 cron 变更"
  就没有意义）。cron 语法是 GitHub 的五字段方言。**重叠不需要单独的策略**：定时 run
  和别的 run 一样走并发组，上一次还没跑完，下一次就排队——这是 GitHub 的 schedule
  完全没有的东西。
- **回写**：run 入队即写 `pending`，落定写 `success` / `failure` / `error`，
  context 是 `orrery / <workflow 名>`——这就是分支保护里要勾的那个名字。
- 没有 `-webhook-secret` 时 webhook 端点直接 503。**未签名的 webhook 端点等于
  给任何能连到这个端口的人一次任意 workflow 执行**，还附带这台服务器注入的密钥。

用 Commit Status 而不是 Checks API 是有意的：**check run 只有 GitHub App 的
installation token 能创建**，PAT 和 OAuth token 一律被拒。Commit Status 任何能写
仓库的 token 都能用，分支保护的 required checks 同样认它。等 Orrery 有了自己的
GitHub App，再升级到 Checks API 拿 diff 行内注解。

### 并发组：默认开启

GitHub 有 `concurrency`，默认关着——所以你们十三个 workflow 里没人写，两次快速
push 可以同时部署到同一台机器。Orrery 保留语法，改掉默认：

```
-default-concurrency '${{ github.workflow }}@${{ github.ref }}'   # 默认值
```

没写 `concurrency:` 的 workflow 自动落进这个组，同组的 run 排队而不是并跑。
默认是**排队不取消**——等待不丢弃任何工作，取消会。写了 `concurrency:` 的以
workflow 自己的为准，包括 `cancel-in-progress: true`。把这个 flag 设成空串就
恢复 GitHub 的行为（完全不限制）。

规则和 GitHub 一致：一个组同时只有一个 run 在跑，后面最多只排一个——再来一个
会把排队中的那个作废（`superseded`），因为等它开跑时"上上个 commit 的部署"几乎
不会是任何人想要的。`cancel-in-progress` 是**请求**正在跑的 job 停下并等它确认，
不是直接杀掉。

### 产物与缓存

runner 进程里起两个 HTTP 服务：产物服务端（默认 34567）和缓存服务端（端口由
OS 选）。它们绑在宿主机的出站 IP 上，因为容器里的 job 要能路由到——绑 loopback
容器就够不着。两个都可以用 `-no-artifacts` / `-no-cache` 关掉（关掉之后对应的
action 会**失败**，而不是静默什么都不做）。

两条必须知道的边界：

**钉 `@v3`。** act 的产物服务端实现的是 v3 协议（`_apis/pipelines/workflows/…`），
缓存服务端实现的是 `_apis/artifactcache`。`upload-artifact@v4` / `download-artifact@v4`
和 `cache@v4.2+` 用的是另一套 twirp Results API，不支持。我们**有意不设**
`ACTIONS_RESULTS_URL`——设了会把"请钉 v3"这条清晰的约束变成一个跑到一半才失败的
action。

**产物与缓存属于产生它的那台 runner。** 只有一台 runner 时这没有区别；有多台时，
下游 job 的 `download-artifact` 只有在恰好落到同一台 runner 上才找得到上游的上传。
中心化的产物存储是修法，在 P1。

### 两个已知缺口（不是疏忽，是已知边界）

**`GITHUB_API_URL` 对 github.com 是错的。** `gitea/runner` v1.0.8 在构造步骤环境时硬编码了
Gitea 的 API 形状（`<forge>/api/v1`，且把 `GITHUB_GRAPHQL_URL` 置空），没有留配置开关。
克隆代码的步骤不受影响（走 `GITHUB_SERVER_URL`，已正确）；调用 GitHub REST API 的步骤会受影响。
修它意味着把 act 那棵树接管过来自己维护，而不是继续跟上游——这是产品决策，不是补丁，
见 charter 的 (a)/(b) 分工。

**host 模式的 job 会继承 runner 进程的整个环境变量。** 这是 act host 模式的行为，
GitHub 的 self-hosted runner 同样如此。默认标签里 `self-hosted:host` 是开着的，
所以运行不可信代码的 runner 必须是独立主机——这正是 `agent-worker/README.md` 里那条规矩。
容器 job 不受影响。此外 **daemon socket 默认不挂进容器**（`-mount-docker-socket` 显式开启）：
拿到 host daemon 的步骤等于跳出了它自己的沙箱。

### 两条设计不要"顺手简化"掉

**超时是 NOT NULL 且有平台默认值。** Tekton、Argo、Dagger 三家形状一致：默认值属于平台、不属于流水线作者。我们 13 个 workflow 里 12 个没写超时，不是疏忽，是 GitHub 没有组织级默认值、忘记写不要钱。这里忘不掉。

**停止是三列不是一个布尔。** `stop_requested_at` 记录请求，`stop_acked_at` 记录 runner 确认已收尾，`force_terminated` 记录我们放弃等待。**一个你无法确认的停止不是停止**——Mobius 曾因此让两次生产对话永久失声。强杀时 `cleanup_ran` 留在 0，台账明说它握着的东西从没释放。

---

## 仓库结构

```
cmd/
  orrery-server/           控制面：runner RPC、提交 API、回收器
  orrery-runner/           agent：取活、执行、流式回传日志
  orrery/                  CLI：submit / runs / run / logs / stop
internal/
  protocol/                runner.v1 线契约
  store/                   SQLite：runs / jobs / runners / logs
  server/                  HTTP 路由与回收器
  forge/                   GitHub REST：读 workflow 文件、回写 commit status
  runner/                  取活循环、act 执行器、标签路由、日志发运
  workflow/                workflow YAML 解析与校验
examples/
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
