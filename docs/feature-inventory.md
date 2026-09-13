# Orrery 功能盘点与对标

76 项功能，十组。每项标注：**对标 GitHub Actions 的哪个功能**、**对标其他产品的什么**、**来源**、**落在哪个阶段**。

**来源图例**：✓ act 白送 ｜ ◇ 继承 Mobius 已有 ｜ ◆ 自建·与 GH Actions 对等 ｜ ★ 自建·GH Actions 没有或做得差（差异化）

**阶段**按 charter P0–P3。

---

## 实现状态（2026-09-13）

P0 的骨架与执行器都已跑通，以下条目**已验证可用**，不再是计划：

| # | 条目 | 验证方式 |
|---|---|---|
| 8 | GitHub Actions 兼容 YAML | `examples/*.yml` 原样提交即跑 |
| 10 | 依赖 DAG（`needs:`） | `examples/hello.yml` 四 job：build →（test, lint）→ report |
| 11 | 可复用 action（`uses:`） | `examples/with-actions.yml`：`actions/checkout@v4` 真克隆、`actions/setup-node@v4` 装出 node v20 |
| 13 | 容器里按步骤跑命令 | 同上，`catthehacker/ubuntu:act-22.04` |
| 17 | 超时 / 取消传播，**默认开启** | `timeout_minutes` NOT NULL DEFAULT 60；回收器到点请求停止 |
| 20 | **带确认的协作式停止** | `stop_requested_at` → `stop_acked_at`（实测 1.2s），`force_terminated=0`，`cleanup_ran=1` |
| 25 | 实时日志流 | ack 定义投递；`::group::` 原样保留 |
| 29 | 密钥：作用域、按步注入 | 派发时注入，不落库；服务端日志只打印密钥名 |
| 31 | CLI：本地 = 生产同一引擎 | `orrery submit/runs/run/logs/stop` |
| 43 | runner label 路由 | `label[:schema[:args]]`，一个 runner 同时服务容器与宿主 job |
| 48 | `vars` 配置变量 | 与密钥同一注入路径，`ORRERY_VAR_*` |
| 49 | 步骤摘要与注解 | 步骤时间线（名称 / 结果 / 耗时 / 日志区间）落库并在 CLI 展示 |

| 1 36 38 | git 事件触发与过滤 | 签名校验的 GitHub webhook；`branches` / `tags` / `paths` / `types` 按 GitHub 语义过滤；按 delivery id 幂等 |
| 27 28 | 产物与缓存 | `examples/artifacts.yml`：upload → download 跨 job 取回同一文件（**钉 v3**，act 的服务端是 v3 协议） |
| 40 | job outputs 跨 job | `examples/outputs.yml`：`needs.build.outputs.image_tag` |
| 47 | **状态回写** | Commit Status API，context `orrery / <workflow>`；入队 pending、落定终态 |
| 67 | 幂等键 | webhook delivery id |

**下一项**是 job 级 `if:`（`always()` / `failure()`）——调度器目前在上游失败时把下游一律标 skipped，
所以"失败时通知"这类 job 不会跑。

**已知边界**（见 README）：`GITHUB_API_URL` 对 github.com 是错的（`gitea/runner` v1.0.8 硬编码 Gitea 的 API 形状，无配置开关）；
host 模式的 job 继承 runner 进程的环境变量；产物与缓存 action 必须钉 `@v3`，且属于产生它的那台 runner。

---

### 一、触发 —— 一个任务怎么开始

| # | 功能（能干什么） | 对标 GitHub Actions | 对标其他产品 | 来源·阶段 |
|---|---|---|---|---|
| 1 | 代码事件触发：push / PR / tag 启动任务 | `on: push / pull_request`，完全对等 | Gitea Actions、Woodpecker | ✓ P0 |
| 2 | 定时触发：cron，带补跑与重叠策略（跳过 / 排队 / 取消）、单调度超时、时区 | `on: schedule`——有延迟、60 天不活动自动禁用、无重叠策略 | **Temporal Schedules**（金标准）、Airflow catchup/backfill、Hermes cron（基线） | ◆ P0→P1 |
| 3 | 手动触发：带类型化参数的表单 | `workflow_dispatch` inputs | Rundeck job options、Windmill 自动表单 | ✓ P0 |
| 4 | Webhook / API 触发：外部系统一个 HTTP 调用即启动 | `repository_dispatch` | Codex Triggers（亚秒）、Inngest 事件优先、n8n webhook | ◆ P0 |
| 5 | **Mobius 委派触发**：把 issue 委派给 agent 即启动一次运行，结果回写 issue | 无对等（最近的是 Copilot coding agent "assign the issue"） | Mobius `delegateToEmail`（基线，已实现）、Copilot coding agent | ◇★ P3 |
| 6 | workflow 调用 workflow：8 个仓库重复的部署抽成一份 | `workflow_call` 可复用 workflow | act 支持（相对路径有 open bug #5992） | ✓ P0 |
| 7 | **内容变化触发**：输入哈希没变就不跑，省掉无意义的 LLM 调用 | 无对等，需手写 | Dagster 数据版本/新鲜度、Airflow 数据感知调度、adops cron 哈希状态文件（基线） | ★ P2 |

### 二、定义 —— 怎么描述任务

| # | 功能 | 对标 GitHub Actions | 对标其他产品 | 来源·阶段 |
|---|---|---|---|---|
| 8 | GitHub Actions 兼容 YAML：现有 13 个 workflow 直接迁移 | 即 GH Actions 本身 | Gitea / Forgejo Actions（先例） | ✓ P0 |
| 9 | **Markdown / 自然语言 agentic workflow**：一段话描述任务，编译成锁定的可执行产物 | 无（gh-aw 是 GitHub 自己的公开预览，2026-06，独立于传统 Actions） | **gh-aw** `gh aw compile`、Codex Automations | ★ P3 |
| 10 | 矩阵 / 条件 / 依赖 DAG | `strategy.matrix` / `if:` / `needs:` | Argo Workflows DAG、Kestra | ✓ P0 |
| 11 | 可复用 action：本地 / Docker / JS / composite；经 SHA 钉住的镜像私服 | GH Marketplace action（可变 tag，tj-actions 2025-03 供应链事故） | act 白送；Gitea 的 action URL 前缀可配（OQ-4 答案） | ✓ P0，私服 P1 |
| 12 | **技能包**：`SKILL.md` 作为 agent 能力单元，frontmatter 描述即路由 | 无对等 | Agent Skills 开放标准、Hermes skills（基线）、Claude Code plugins；反面：ClawHub 341 恶意技能 | ◇ P3 |

### 三、执行 —— 在哪跑、怎么跑

| # | 功能 | 对标 GitHub Actions | 对标其他产品 | 来源·阶段 |
|---|---|---|---|---|
| 13 | CI job：干净容器里按步骤跑命令 | GH job on runner | act 白送 | ✓ P0 |
| 14 | **Agent job**：沙箱里跑一个 agent session（Claude Code / Codex / Hermes / pi），带轮次、成本、停止、心跳 | **无此 job 类型**——GH 的 agent 是另一个产品（Copilot coding agent） | Copilot coding agent 会话、**Claude Managed Agents** session、Codex cloud task、Devin session | ★ P3 · 最大新增 |
| 15 | 自建 runner 池：跑在自己的 AWS spot 上，GitHub 侧账单归零 | self-hosted runner（仍免费） | RunsOn、Blacksmith、Ubicloud | ◆ P1 |
| 16 | 沙箱隔离：每 run 一个 microVM / 容器、无环境凭据、出口 allowlist、push-only token | GH-hosted 是干净 VM，self-hosted 需自管；Copilot coding agent 有防火墙 | E2B、Daytona、Actuated Firecracker | ◆★ P0 要求（原 P2） |
| 17 | 超时 / 并发组 / 取消传播，**默认开启** | `timeout-minutes` / `concurrency` / `cancel-in-progress`——有但默认关（你们 12/13 没设） | Temporal 取消作用域、Hatchet 并发键 | ◆★ P0 |
| 18 | 多运行时 / 模型无关：同一份定义可指定任何 agent 引擎、BYO key | 无（`GITHUB_TOKEN` 单一身份） | gh-aw 四引擎、OpenHands、LiteLLM 路由（已在用） | ★ P3 |

### 四、控制 —— 人怎么管着它

| # | 功能 | 对标 GitHub Actions | 对标其他产品 | 来源·阶段 |
|---|---|---|---|---|
| 19 | 审批门 / 人在环中：等待审批步、超时升级、审批者 ≠ 请求者、在 Slack / PR / UI 审 | Environments required reviewers（仅部署环境级） | gh-aw safe outputs、Spacelift plan→apply、Airflow 3.1 HITL、Windmill 审批步、Trigger.dev `wait.forToken` | ◆★ P2 |
| 20 | **协作式紧急停止**：请求 → agent 确认 → 超时强制；清理步仍执行；不会永久失声 | cancel 单向，无"确认"语义 | Temporal 取消 + 心跳、Mobius `stopRequestedAt / stopAckedAt`（基线） | ◇★ P3 |
| 21 | **成本预算**：按 run / agent / 所有者 / 团队设上限，三重硬停（轮次、墙钟、美元），成本与运行记录同事务写入 | spending limit 仅账号级，触顶 job 静默不启动（8/19 撞的就是它） | Devin ACU、Codex `/goal`、Cursor max-spend、LiteLLM 预算（已在用）、Mobius maxCostUsdPerHour（基线） | ◇★ P1 |
| 22 | **agent 身份与责任**：agent 是独立主体但不可被追责——分配给人、委派给 agent；所有者可以其身份说话，管理员只能暂停不能冒名 | agent 只是一个 token，无主体模型 | Copilot coding agent（bot 署名 PR + 人类请求者）、Mobius 五种身份（基线）、Entra Agent ID | ◇ P3 |
| 23 | 治理开关默认关闭、按 agent 暂停、健康告警（7 天陈旧 / 3 连败 / 30 天成本窗、去重） | 无 | Mobius（基线）、Temporal 心跳、Airflow zombie 检测 | ◇ P3 |
| 24 | **提示注入防护**：只读默认、写只经 safe outputs、工具与出口 allowlist、密钥不入模型上下文、issue 正文标为不可信 | 传统 workflow 本身是注入面（`${{ github.event.issue.title }}` 直接进 `run:`） | gh-aw、Copilot coding agent、Claude Code 权限模式、Mobius 空 allowlist（基线） | ◇★ P0 |

### 五、数据面 —— 跑的过程中产生的东西

| # | 功能 | 对标 GitHub Actions | 对标其他产品 | 来源·阶段 |
|---|---|---|---|---|
| 25 | 实时日志流：可回溯、`::group::` 折叠、扛住单 job 几十万行 | GH Actions 日志 | Temporal UI、Buildkite | ◆ P0 |
| 26 | **运行台账**：一条记录含输入、工具调用、模型、token / 成本、轮次、分支、产物、终态；机器可读 + 人可读同一处 | 只有日志，没有决策台账 | **Temporal 事件历史**（可重放）、Claude Managed Agents session log、Langfuse trace、Mobius trailer（基线） | ◇★ P3 |
| 27 | 产物：上传 / 下载 / 保留策略 | `actions/upload-artifact` | act 自带本地服务端，协议不用重设计 | ✓◆ P0 |
| 28 | 缓存：key / restore-keys → 升级为内容寻址 | `actions/cache`（key 猜谜、miss 静默、淘汰不透明） | Dagger / Bazel / Turborepo 内容寻址 | ✓→★ P0→P2 |
| 29 | 密钥：作用域、按步注入、日志自动打码、拒绝含活 token 的输出、OIDC 短期凭据 | secrets + `::add-mask::` + OIDC，对等 | GitLab protected/masked vars、Mobius 脱敏与拒绝（基线） | ◆◇ P0 |
| 30 | **投递 ≠ 成功**：job / 投递 / 综合各自记录，投递可重试有死信 | `if: always()` + `continue-on-error`，弱且需手写 | Inngest / Trigger.dev 步级重试、n8n error workflow、adops cron `last_status` vs `last_delivery_error`（基线） | ★ P2 |

### 六、入口 —— 谁怎么用

| # | 功能 | 对标 GitHub Actions | 对标其他产品 | 来源·阶段 |
|---|---|---|---|---|
| 31 | CLI：本地 = 生产同一引擎，推之前先在本机跑一遍 | **做不到**，被骂最狠的一条 | `act`、Dagger、Trigger.dev dev server | ✓★ P0 |
| 32 | Web UI：run 列表、时间线、日志、重跑单 job / 全部、取消 | GH Actions 页面 | Temporal UI、Windmill | ◆ P1 |
| 33 | **Slack / Mobius 入口**：非工程师提需求、审批、看结果，不碰 YAML | 无 | Zapier / Make（非工程师金标准）、Devin Slack 原生、Mobius 委派（基线） | ★ P3 |
| 34 | **吸收本地 launchd 层**：一条命令接管现有 launchd / cron 任务，给它密钥管理、重试、集中日志 | 无 | Claude Code Routines、Codex Automations、Hermes cron（基线）；现状是 `claude -p … --dangerously-skip-permissions`、失败靠 macOS 通知 | ★ P2 |
| 35 | **agent 经 PR 修改自动化**：agent 想改 workflow 只能开 PR，人审后生效 | 无原生 | gh-aw safe outputs、Codex Automations、hermes-agent-self-evolution "全走人审"（基线） | ◇★ P3 |

### 七、补遗 —— 对标 GitHub Actions 的完整性检查

把 GitHub Actions 的功能面逐项核对（触发 / jobs / steps / 表达式 / runner / 数据 / 可观测 / 安全治理 / 生态 / API，约 50 项）后，前 35 项漏了下面 19 项。**归组**指它属于上面六组中的哪一组。

| # | 归组 | 功能（GH Actions 有的） | GH Actions 现状 | Orrery 处理 | 来源·阶段 |
|---|---|---|---|---|---|
| 36 | 一 | 路径 / 分支 / tag 过滤与活动类型过滤（`paths` `branches` `types: [opened, labeled]`） | 有 | 对等：解析 act 白送，事件匹配在调度器自建 | ✓◆ P0 |
| 37 | 一 | `workflow_run`：一个 workflow 完成后触发另一个 | 有 | 对等；Orrery 自身 run 状态也作为事件源对外发出（webhook） | ◆ P0 |
| 38 | 一 | GitHub 全量 webhook 事件作触发源（issues / issue_comment / release / label / deployment / merge_group …） | 有，约 40 种事件 | 对等：接 GitHub webhook 映射到 `on:`；自有框架事件走同一入口 | ◆ P0→P1 |
| 39 | 一 | `pull_request_target`：以基分支权限跑 fork PR | 有——经典 pwn-request 向量 | **有意不复刻**，用 safe outputs 模式替代（#24） | ★ 不做 |
| 40 | 二 | job outputs 跨 job 传递（`needs.<job>.outputs`） | 有 | 对等；跨机器传递由调度器承载 | ◆ P0 |
| 41 | 二 | `services:` 边车容器（Postgres / Redis 随 job 起） | 有 | 对等；act 支持有限需实测，缺口自补 | ✓? P0 |
| 42 | 二 | starter workflows / 模板（golden paths） | 有 | 对等，且面向非工程师（配合 #33） | ◆ P2 |
| 43 | 三 | runner labels / groups 路由、弹性伸缩（对标 ARC）、**预装工具镜像** | 有；GH-hosted 镜像预装数百工具 | 对等；镜像自建是 P1 的隐藏工作量 | ◆ P1 |
| 44 | 三 | macOS / Windows runner（iOS 构建） | 有（macOS $0.062/min） | **保留，延后**：暂无 iOS 构建需求，先 Linux-only；需要时接入 Mac 硬件或租 WarpBuild。runner 池的 label 路由（#15 / #43）天然支持异构机型，日后接入只是多注册一个 `macos` 标签的 runner，不需改架构 | ◆ P3+ 预留 |
| 45 | 四 | `permissions:` 按 job 收窄自动 token 权限 | 有，默认可读写取决于组织设置 | 对等且**默认最小**；与 #22 身份模型合并 | ◆★ P0 |
| 46 | 四 | 组织级 ruleset：要求特定 workflow 通过才可合并；allowed actions 策略 | 有 | 前者依赖 #47 状态回写；后者即 #11 私服 allowlist | ◆ P1 |
| 47 | 五 | **状态回写**：向 GitHub Checks API / Commit Status 报告 run 结果，供分支保护与合并门禁使用 | 原生——Actions 就是 GitHub 的一部分 | **对等且必须**：没有它 Orrery 挂不上现有 GitHub 流程；自有框架用同一抽象 | ◆ P0 · **最重要的遗漏** |
| 48 | 五 | `vars` 配置变量（repo / org / env 级非密钥配置） | 有 | 对等，与 #29 密钥同一作用域模型 | ◆ P0 |
| 49 | 五 | `$GITHUB_STEP_SUMMARY` 步骤摘要；`::error / ::warning / ::notice` 注解（含 file/line）；debug 日志开关 | 有；注解可标到 PR 代码行 | 摘要与注解对等（act 白送写入，渲染自建）；PR 行内注解依赖 #47 | ✓◆ P0 |
| 50 | 五 | 运行通知：失败 / 成功 / 恢复 → Slack / 邮件 | 有 | 对等，与 #30 投递语义合并 | ◆ P1 |
| 51 | 五 | 用量 / 计费仪表盘：分钟数与成本按仓库 / workflow / agent 拆分 | 有，账号级、粗 | 对等且更细，与 #21 预算同源 | ◆ P1 |
| 52 | 五 | 产物溯源 / 构建证明（`attest-build-provenance`，SLSA） | 有，2024 GA | 对等，供应链；与 #11 SHA 钉住配套 | ◆ P2 |
| 53 | 六 | 完整管理 API + 管理 CLI（list / view / watch / rerun / cancel / 下载日志） | 有，REST + `gh run` | 对等；#4 #31 #32 各覆盖一角，此处补齐 | ◆ P1 |
| 54 | 六 | action 版本自动更新（对标 Dependabot for actions） | 有 | 归入 #35：agent 开 PR 升级版本 | ◇ P3 |

### 八、部署 —— 服务器部署服务（当前主场景）

你们的实际部署形态：Docker 构建 → 推 GHCR → SSH 到 EC2 → 拉镜像 → 重启 → 冒烟（`deploy.yml` 681 行、`SMOKE_BASE`、`ghcr-cleanup.yml`）；9/2 三个 release job 齐倒于 `known_hosts`。这一组是 charter P2 楔子"部署原语一等公民"的展开，GH Actions 在这里几乎没有原语。

| # | 功能 | 对标 GitHub Actions | 对标其他产品 | 来源·阶段 |
|---|---|---|---|---|
| 55 | 部署目标与环境注册：staging / prod × 服务器，每环境独立密钥、变量与保护规则 | Environments（URL、密钥、required reviewers、wait timer） | Spacelift stacks、Argo CD apps、Rundeck nodes、Kamal destinations | ◆ P1 |
| 56 | **SSH / agent 部署原语**：`known_hosts`、跳板机、幂等的"拉镜像 → 切换 → 健康检查"内建为步骤类型 | 无原语，每仓库手写 | **Kamal**（37signals；Docker + SSH 部署到自有服务器，与你们形态最接近）、Ansible、appleboy/ssh-action | ★ P2 |
| 57 | 镜像仓库集成：构建 → 推送 → 按策略清理 | GHCR + 手写清理 workflow | Depot、Harbor 保留策略 | ◆ P1 |
| 58 | **回滚**：一键回到上一个已知良好版本，保留 N 个版本 | 无原生，靠重跑旧 commit | Argo CD rollback、Kamal rollback、Spacelift | ★ P1 |
| 59 | **部署现状与历史**："哪个版本现在跑在哪台机器上"；每次部署谁触发、何时、结果 | Deployments API + 环境页 | Argo CD 应用状态、Port 目录 | ◆ P1 |
| 60 | 部署后健康检查 / 冒烟测试作为一等步骤，失败自动回滚 | 无原语 | Kamal healthcheck、Argo Rollouts analysis | ◆ P1 |
| 61 | **漂移检测**：实际运行版本 ≠ 期望版本时告警 | 无 | Argo CD / Flux drift detection、Spacelift | ★ P2 |
| 62 | 部署策略：滚动 / 蓝绿 / 金丝雀 | 无 | Argo Rollouts、Kamal | ◆ P3，当前规模可能不需要 |
| 63 | **策略即代码**：谁能部署到哪、什么时段可部署（冻结窗）、部署前置条件 | ruleset 部分覆盖 | Spacelift / OPA、Atlantis | ◆ P2 |

### 九、执行语义 —— 来自 durable execution 竞品

Temporal / Inngest / Trigger.dev / Hatchet 的定义性能力。GH Actions 全部没有；agent 长任务与审批流必需。

| # | 功能 | 对标 GitHub Actions | 对标其他产品 | 来源·阶段 |
|---|---|---|---|---|
| 64 | **持久执行**：worker 崩溃后从最后完成的步骤恢复，不从头跑 | 无——job 失败即整个重跑 | **Temporal**、Inngest、Trigger.dev、Hatchet | ★ P2 |
| 65 | **持久等待**：任务挂起数小时到数天等审批或外部事件，不占 runner | 无——job 上限 6 小时，且等待期间占着 runner | Temporal signals、Inngest `waitForEvent`、Trigger.dev `wait.forToken` | ★ P2 · #19 审批门的底座 |
| 66 | 步级重试策略：退避、最大次数、不可重试错误分类 | 无原生 | Temporal retry policy、Inngest、Airflow retries | ◆ P0 |
| 67 | 幂等键：同一事件重复投递只跑一次 | 无 | Inngest idempotency key、Temporal workflow ID | ◆ P1 |
| 68 | 限流 / 节流 / 去抖：webhook 风暴不压垮系统，同键事件合并 | 无 | Inngest concurrency / throttle / debounce、Hatchet | ◆ P1 |
| 69 | 向运行中的任务发信号 / 查询其状态 | 无 | Temporal signals & queries | ◆ P2 |
| 70 | workflow 定义的版本化与回滚 | 靠 git | Trigger.dev 原子版本化部署、Temporal worker versioning | ◆ P1 |

### 十、agent 与集成补遗

| # | 功能 | 对标 GitHub Actions | 对标其他产品 | 来源·阶段 |
|---|---|---|---|---|
| 71 | **agent 跨运行记忆**：agent 记得上次做了什么、学到了什么 | 无 | Claude Managed Agents memory、Letta、Mobius journal（基线） | ◇★ P3 |
| 72 | **集成连接器策略**：不做 n8n 式 400+ 节点库，用 MCP 服务器 + 技能包覆盖 SaaS 集成 | 无 | n8n / Zapier 节点库（对标对象）、Composio / Arcade 工具认证 | ★ P3 · 策略决定 |
| 73 | RBAC：谁能触发 / 审批 / 看日志 / 改密钥 / 改定义，按团队与环境 | 仓库权限 + 环境保护 | Port RBAC、Windmill 权限、Mobius 五种身份（基线） | ◆◇ P1 |
| 74 | 代码定义管道（TS / Python SDK 而非 YAML）作为第三前端 | 无 | Dagger、Trigger.dev、Inngest | ◆ 待定 |
| 75 | 动态管道：运行中生成后续步骤，agent 决定下一步 | 无 | Buildkite dynamic pipelines | ★ P3 |
| 76 | 可视化流程编辑器 | 无 | n8n、Windmill、OpenAI Agent Builder（2026-11 弃用） | **有意不做**：用 #9 自然语言 + #33 入口替代 |

**有意不复刻**（3 项）：#39 `pull_request_target`（→ safe outputs）；Marketplace 公开生态直连（→ SHA 钉住的私服，#11）；#76 可视化编辑器（→ 自然语言 + Slack/Mobius 入口；Agent Builder 弃用是教训）。
**延后预留**（1 项）：#44 macOS / Windows runner——暂无 iOS 构建需求，保留在清单里；label 路由使其日后接入不需改架构。
**不属于 Actions 本体、不算遗漏**：Code scanning / CodeQL、Dependabot 安全更新、Copilot coding agent 本体、GitHub Pages 部署。

**覆盖结论 · GitHub Actions**：功能面约 50 项 → 对等约 38 项（含 act 白送约 12）、有意不复刻 2 项、延后预留 1 项、非本体不计 4 项。**对标完整。** 最要紧的补项是 #47 状态回写——没有它 Orrery 挂不上现有 PR 流程。

**覆盖结论 · 竞品签名功能**（逐家核对其定义性能力是否有落点）：

| 竞品 | 签名功能 | 落在 |
|---|---|---|
| Temporal / Inngest / Trigger.dev / Hatchet | 持久执行、持久等待、信号、重试策略、幂等、限流、调度重叠策略 | #64–70、#2 |
| gh-aw / Copilot coding agent | 自然语言 workflow、safe outputs、多引擎、agent 开 PR、issue → 沙箱 → PR | #9 #24 #18 #35 #5 #14 |
| Claude Managed Agents / Routines / Codex Automations | agent 会话、预算、记忆、停止、定时 agent、吸收本地层 | #14 #21 #71 #20 #2 #34 |
| Windmill / n8n / Activepieces / Zapier | 表单、审批步、连接器、可视化、错误流 | #3 #19 #72 #76 #30 |
| Argo CD / Spacelift / Kamal | GitOps、plan → apply、回滚、漂移、策略即代码、SSH 部署 | 第八组、#19 |
| Dagger | 本地 = CI、内容寻址、SDK 定义 | #31 #28 #74 |
| Backstage / Port / Rundeck | 模板、自助表单、RBAC、目录 | #42 #3 #73 #59 |
| Paperclip / Mobius | agent 组织、身份、预算、治理 | #22 #21 #23 |
| Buildkite | 动态管道、混合控制平面 | #75 #15 |
| Blacksmith / Depot / RunsOn | 快缓存、Docker 层缓存、spot | #28 #15 |
| Airflow / Dagster | 补跑、数据感知调度、新鲜度、SLA | #2 #7 #23 |

**盘点**（76 项）：✓ act 白送约 12 项；◇ 继承 Mobius 约 12 项；◆ 自建对等约 34 项；★ 差异化约 24 项（与前三类有重叠）；有意不做 3 项；延后预留 1 项。**GitHub Actions 完全没有的**：#5 #7 #9 #12 #14 #18 #22 #23 #26 #33 #34 #56 #58 #61 #64 #65 #71 #75——18 项，围绕 agent 与部署。研究的评估维度（18 维 + 新增 D19 部署原语）就是从这 76 行反推出来的口径。
