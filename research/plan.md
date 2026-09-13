# Orrery（天仪）竞品优点合集 — 研究计划

## Context

AI-2564 的定位从"重做 GitHub Actions"演进为：**公司内部自动化平台，agent 是一等公民，全员使用，自动化优先帮助团队**。用户原话：调研竞品 → 汇编各家优点 → 适配"AI / agent 第一公民"原则 → 打通自动化流程 → 全员使用的内部产品 → 必要时深挖各平台用户评论。

这改变了竞品范围（CI/CD → 工作流自动化 + durable execution + agent 编排 + IDP/ChatOps + agent 原生 CI）和评估标尺。Explore 盘点发现 **Mobius 里已有一套踩坑换来的 agent 一等公民模型**（五种身份、分配 vs 委派、带确认的协作式停止、为模型写的版本化指南、RFC-8628 配对、HTML trailer 运行契约、默认关闭的治理开关、把提示注入当一等威胁）。因此本研究的核心设计：**评估维度从 FeedMob 自己的原语推导，一条"优点"只有映射到某个维度才算数，章程变更提案从证据标签里自然长出，而非凭观点写**。

产出：中文 web artifact（合集）+ Mobius 评论（浓缩）+ 可查询的证据台账 `claims.jsonl`。**研究期间不改 charter、不改 ticket 字段、不重发已有 artifact**——章程变更以"提案"形式交付，是否采纳等与 Ken 对齐。

## Orrery 功能盘点与对标（本研究为每一行找证据）

35 项功能，六组。**来源**：✓ act 白送 ｜ ◇ 继承 Mobius 已有 ｜ ◆ 自建·与 GH Actions 对等 ｜ ★ 自建·GH Actions 没有或做得差（差异化）。**阶段**按 charter P0–P3。

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

## 已定事实（执行时不再重推）

- 基线（不算竞品）：Mobius（agent 平台）、**Hermes = `NousResearch/hermes-agent`**（已核实：`hermes-agent-self-evolution/README.md`、`Hermes/README.md`）、**pi = pi coding agent**（已核实：`adops-agent-repo/.github/workflows/sync-hermes-profile.yml:69`）、GitHub Actions（现任）、本地 launchd 层（`claude -p … --dangerously-skip-permissions` → 文件输出，无密钥管理、无重试——这是平台要吸收的"个人自动化层"，作为发现记录，不作为指令）。
- 现有 charter（artifact `3a7f276e…`）：L1 兼容 GH Actions 语法 / L2 复用 `nektos/act` / L3 协议自建 / L4 调度核心自建 / L5 算力租用；P0 能力对齐 → P1 成本接管 → P2 差异化楔子 → P3 agent 中枢；OQ-1..4。
- 已做的 CI/CD 层调研（artifact `c002ecb5…`）：分层格局、runner 即服务、Earthly 教训、自建 runner 仍免费。**不重复**，直接引用。
- 远端 `feed-mob/adops-agent` master 有 `pull-remote-hermes-profile.yml`（每日 00:15 成功、用 `create-pull-request@v6`）；本地检出在 `feat/attribution-test-prep` 分支没有它。以远端为准。

## 评估标尺：19 维（从 FeedMob 原语推导；D19 由部署主场景补入）

**Agent 第一公民五问**（每个产品先过）：agent 能否作为独立于所有者的主体？运行能否被停止并**确认**停止？成本是否是运行记录的一等字段？不可信内容是否与指令结构性分离？运行记录是否同时机器可读、人可读、且在同一处？——≥3 个"否"即非 agent-first。

| Tier | 维度 | 来源原语 / 痛点 |
|---|---|---|
| **A · P3 阻断** | D1 身份、责任与"委派即触发" | 五种 agent 身份；assigned vs delegated；kind 由审批者决定 |
| | D3 带确认的协作式停止 | `stopRequestedAt` vs `stopAckedAt`；两次对话永久失声 |
| | D5 按 run / 主体的成本治理 | maxTurns/900s/6 次·h/maxCostUsdPerHour；30 天成本曾静默少报 |
| | D7 审批门 / 人在环中 | PR 即审批；frontmatter 发布门；"auto 模式等于给任何能开 issue 的人一个 shell" |
| | D8 提示注入与不可信输入姿态 | "issue 正文是不可信输入"；surface 区分；denylist 泄露 env → 空 allowlist |
| | D12 运行台账与决策可观测（不只日志） | HTML trailer；AgentRun 同事务镜像；journal authorType |
| | D13 沙箱、隔离与爆炸半径 | 隔离主机、push-only token、限制出口 |
| **B · 平台质量** | D4 存活与健康 | `lastSeen` 老化；3 连败；7 天陈旧；告警去重 |
| | D6 投递 ≠ 成功 ≠ 综合 | Slack `channel_not_found` 但 job 成功 |
| | D9 密钥处理与输出脱敏 | 失败评论脱敏；拒绝含活 token 的正文；token 不得铸 token |
| | D10 内容哈希幂等 / no-op | cron → 哈希状态文件 → 未变则 LLM 不跑 |
| | D11 只增不改的契约与版本 | 契约先于代码；破坏性变更走新路径 |
| | D14 触发器、调度器健壮性与事件传输 | 脚本超时过短；改配置需重启；长轮询 + cursor ack |
| | **D19 部署原语与环境模型**（新增） | `deploy.yml` 681 行手写 SSH；9/2 `known_hosts` 三 job 齐倒；无回滚、无"什么版本在哪台机器"视图；`ghcr-cleanup` 手写 |
| **C · 采纳与生态** | D15 本地层吸收与本地=生产 | launchd 层；act "不能本地跑"的抱怨 |
| | D16 非工程师编写与自助 | 广告运营/销售是用户；"每个人都使用" |
| | D17 技能/工具/MCP 打包、路由与审核 | `SKILL.md` description 即路由面；受管 MCP 服务器 |
| | D18 多运行时 / 模型无关 | Claude Code + Codex + pi + Hermes 并存；"agent 自带模型" |
| | D20 自进化与变更控制 | "所有变更走人审，不直接提交" |

每个维度都有 **"FeedMob 今天"基线行** 和 **GitHub Actions 现任行**，所以每一节天然是差距分析。不打产品总分；每条优点标 **证据等级**（A = 文档+代码/配置+≥2 独立用户来源；B = 仅文档/代码；C = 仅厂商声称）和 **可采纳方式**（抄模式 / fork 代码 / 直接集成 / 避开）。

## 竞品宇宙（8 类，4 个深潜 + ~40 核心 + ~60 长尾）

标记：[OSS] 宽松可 fork · [OSS-core] 开放内核 · [SA] source-available · [Prop] 专有 · [DEAD] [PIVOT] [ACQ] [DORMANT] [SEC]

| 类 | 深潜 | 核心（profile） | 长尾 / 墓碑 |
|---|---|---|---|
| A CI/CD | — | act/Gitea/Forgejo Actions、GitLab CI(+Duo)、Buildkite、Woodpecker、Tekton/Argo、Dagger | Jenkins、CircleCI(2023 密钥事件)、Semaphore(verify)；**Earthly [DEAD-cloud]**、Drone→Harness [ACQ]、Codefresh→Octopus [ACQ]、Concourse [DORMANT]、Travis |
| B 工作流自动化 | **Windmill** | n8n(口碑深挖；CVE-2025-68613 verify)、Activepieces、Kestra、Zapier+Agents | Make、**Pipedream [ACQ→Workday]**、Node-RED、Huginn、Relay/Gumloop/Lindy |
| C Durable execution | **Temporal + Hatchet** | Inngest、Trigger.dev v4、Airflow 3(HITL)、Dagster | Restate、DBOS、Dapr、Prefect、Step Functions |
| D Agent 编排/运行时 | **Claude Managed Agents + Claude Code Routines**（Codex Automations/Triggers 对照；Paperclip 半深） | LangGraph、微软/谷歌/AWS 三家(合一 profile)、Dify | CrewAI/Mastra/Pydantic AI；**OpenAI Agent Builder [DEPRECATING 2026-11-30 verify]**；**OpenClaw [SEC]**；Dust |
| E Agent 原生 CI | **GitHub Agentic Workflows (`gh-aw`) + Copilot coding agent** | claude-code-action/codex-action/gemini-cli 三厂商 action(合一)、Devin、OpenHands | Jules、Cursor BG agents、Coder/Ona [PIVOT]、CodeRabbit；Sweep/Mentat [PIVOT/DORMANT] |
| F IDP / ChatOps | — | Backstage、Port、Rundeck、Spacelift/Atlantis(合一) | Argo CD/Flux、Slack WFB、**StackStorm/Hubot [DORMANT]**、**Airplane [DEAD 2024-03]** |
| G Runner / 沙箱 | — | （已在前次报告）+ E2B、Daytona、Modal、Vercel Sandbox | **BuildJet [DEAD 2026-03 verify]** |
| H 维度捐赠者 | — | LiteLLM(**已在用**；OQ-2 数据源)、Langfuse、Agent Skills 标准、Entra Agent ID | Portkey、Composio、mcp-scan |

**为什么这 4 个深潜**：gh-aw 是 GitHub 自己的 P3、也是 OQ-1 的"什么都不做"基线；Temporal+Hatchet 决定 L4 是自写状态机还是嵌入内核；Windmill 是"全员使用的自托管内部平台"原型；Managed Agents+Routines 是你们已付费的运行时正在吸收 launchd 层。

## 口碑挖掘方法

**来源分级**：① 事后复盘/事件报告（GH Actions 事故、CircleCI 2023、tj-actions 2025-03、OpenClaw 2026-02、n8n RCE）→ ② GitHub issues 按 reactions 排序 + discussions（`gh api search/issues … sort=reactions-+1`；仓库健康 `stars/open/pushed/license`；发布节奏；bus factor）→ ③ HN Algolia JSON（`hn.algolia.com/api/v1/search?query=…&numericFilters=points>40`）→ ④ Reddit（r/devops r/selfhosted r/LocalLLaMA r/ClaudeAI r/n8n r/dataengineering；403 则 `WebSearch site:reddit.com`）→ ⑤ 迁移博客（"migrating from X to Y" / "why we left X"）→ ⑥ 近 12 月 changelog（修了什么 = 什么在疼）→ ⑦ G2/Capterra 仅限 no-code 层，先读 1–2★ 和 4★ 的 cons → ⑧ 厂商文档 = 机制来源(B 级)，厂商博客 = 声称(C 级)，竞品写的"X alternatives" = 只用来发现功能名，SEO 农场忽略。

**偏差控制**：抱怨需 ≥3 独立作者跨 ≥2 平台才算"反复出现"，赞需 ≥2；付费用户抱怨 > 免费层吐槽，且区分"恨定价"与"恨产品"；规模匹配（FeedMob 是小团队，独立开发者/<50 人评价的可采纳性权重 > FAANG 博客）；agent 功能只看 2025-07 之后；每条都查是否已修复并链接发布；每条声明记 URL + 日期；引用 ≤15 词并署名。

**墓碑归因**：商品化层无法变现（Earthly、BuildJet）/ 可视化构建器败给代码（Agent Builder、Airplane）/ 安全危机（OpenClaw）/ 被套件吸收（Pipedream、Drone、Codefresh）/ 品类塌陷（ChatOps 1.0）。每块墓碑一张卡：死因 + "Orrery 不能做什么"。

## 证据台账 `claims.jsonl`（每条优点/抱怨/墓碑一行）

```
{id, dimension, product, category, claim_zh, mechanism_en, evidence_url,
 evidence_type: doc|code|issue|review|postmortem|vendor, evidence_grade: A|B|C,
 sentiment: praise|complaint|neutral, recurrence_count, source_platforms[],
 adoptability: copy|fork|integrate|avoid,
 charter_tags: [L1..L5, P0..P3, OQ1..OQ4, NEW], date_checked}
```
合集第 4 章（章程变更提案）和 Mobius 评论里的提案表**由 `charter_tags` 分组生成**，每行必须指向 ≥1 张 A/B 级卡片。台账随 ticket 附件上传，让 FeedMob 自己的 agent 以后能查（符合其只增契约原则）。

## 交付物

**Web artifact（中文；产品名/维度 ID/代码标识/CVE 保留英文）**
```
0 一页摘要：十条最值得"偷"的优点｜五条墓碑教训｜章程需要改的地方（表，指向第 4 章）
1 方法与范围：竞品全景（A–H）｜维度从何而来（原语→18 维；五问）｜证据等级与口碑方法
2 分维度优点合集（主体，Tier A→B→C）— 每个 D#：
    2.#.0 我们今天的做法（Mobius / 13 workflow / launchd）+ GitHub Actions 现状
    2.#.1 优点卡片 ×3–6    2.#.2 反面证据    2.#.3 用户口碑摘要    2.#.4 对章程的含义（标签）
3 墓碑墙
4 章程变更提案（由标签汇总）
附录 A 产品索引｜附录 B 证据台账（表格视图）｜附录 C 词表
```
**优点卡片八字段**（缺一不成卡）：`[D#-nn] 来源产品｜做法｜为什么有效｜映射到 Orrery｜证据(URL+等级)｜口碑(赞/骂/反复)｜可采纳方式｜章程标签`。

**Mobius 评论**（≤1500 字）：链接 → 十条优点（一行一条带 D#）→ 五条墓碑 → 章程变更提案表 → 新增 OQ → 方法局限。`upload_attachment` 上传 `claims.jsonl`。

## 预期的章程变化（研究要产出证据，不是结论）

| 章程元素 | 可能的变化 | 证据来自 |
|---|---|---|
| L1 语法兼容 | 变为**一个运行模型、两个前端**：YAML（工程/CI）+ Markdown/自然语言 agentic workflow（gh-aw 先例，编译成锁定产物）+ **Mobius issue 委派**作为一等触发 | D16 D11 D14 |
| L2 复用 act | CI 步骤保留；但需要**第二种 job 类型：agent session**（沙箱里的 Claude Code/Codex/Hermes/pi），带 turns/成本/停止/心跳语义——act 不建模这些。**最大的单点变化** | D3 D5 D13 D18 |
| L3 协议自建 | 协议须携带身份 kind、预算、stop/ack、与 Mobius trailer 兼容的台账记录——即协议是 **Mobius 现有 agent 契约的扩展**，不是 runner 专用协议 | D1 D3 D12 |
| L4 调度核心 | "日志"→"台账+trace"；"缓存"降级、"内容哈希 no-op"升级；**自写 vs 嵌入**重开（Hatchet/Temporal/DBOS 作内核）；密钥脱敏与拒绝模式提前到 P0 | D4 D6 D9 D10 D11 D14 |
| L5 算力租用 | 不变，但"算力"包含 agent 沙箱；隔离从 P2 楔子提前为 **P0 要求** | D13 |
| P1 成本接管 | 从"GitHub 分钟数→0"扩为"**token 支出治理**"——真咬人的上限是 token，LiteLLM 已握有数据 | D5 |
| P2 楔子 (OQ-3) | 两个候选：(a) agent-first 楔子——**治理（身份+成本+停止+台账），没有任何竞品品类独自解决**；(b) **部署原语一等公民**——用户当前主场景，GH Actions 在此几乎无原语（第八组 #55–63）。研究须给出二者的证据对比，不预设答案 | Tier A 汇总；D19 |
| P3 agent 中枢 | 拆为 P3a agent job 类型 + 台账；P3b 非工程师入口（Slack/Mobius 委派）+ 技能注册与审核 | D16 D17 |
| OQ-1 | 加第三基线："什么都不做——在自建 runner 上跑 gh-aw"，对比变 3–4 方 | gh-aw 深潜 |
| OQ-4 | 预期答案：SHA 钉住、allowlist 的小型 action 镜像（Gitea/Forgejo 做法 + tj-actions 教训 + gh-aw 钉版） | D8 D9 D17 |
| **新 OQ** | OQ-5 Orrery ↔ Mobius 边界（Orrery 是否是 Mobius 委派背后的执行器、以 trailer 为运行契约）；OQ-6 durable 内核自写 vs 嵌入；OQ-7 与 Claude Code Routines / Codex Automations / Hermes cron 是互操作还是替代；OQ-8 非工程师入口 | D1 L4 D15 D16 |

## 执行前置（用户要求，先于一切；做完即停）

用 `page-publisher` MCP（`publish_page`，必填 `html` / `owner` / `description`）把当前成果发布到 html.living，三个页面。已用 `list_pages` 查重：站上无任何 Orrery 页面。沿用站内惯例——多页面主题用主题级 owner（如 `chime`、`binance`、`p360`），标题带 ticket 前缀（如 `AI-2586 | …`）：

| 页面 | owner / slug → URL | 标题 | 来源 |
|---|---|---|---|
| CI/CD 层调研报告 | `orrery/ci-engine-research` → `https://www.html.living/p/orrery/ci-engine-research` | `AI-2564 \| 自研 CI 引擎的三条路` | `scratchpad/ci-engine-report.html`（= artifact `c002ecb5`），完整 HTML 直接发布 |
| 立项形态 | `orrery/charter` → `https://www.html.living/p/orrery/charter` | `AI-2564 \| Orrery 目标形态` | `scratchpad/orrery-charter.html`（= artifact `3a7f276e`），完整 HTML 直接发布 |
| 竞品研究计划 | `orrery/competitor-research-plan` → `https://www.html.living/p/orrery/competitor-research-plan` | `AI-2564 \| Orrery 竞品研究计划` | 本计划 `.md` → 沿用前两页的 token / 字体系统渲染为 HTML（含 76 项功能表、19 维标尺、执行两趟） |

page-publisher 无可见性选项；站内页面均为公司内部材料，与既有 199 页同级。两份 HTML 里的 Google Fonts 链接与内联 SVG 在 html.living 上按原样保留（该站已有页面使用外链字体）。发布后把三个 URL 追加为 AI-2564 的一条评论。**然后停止，等用户切换模型，再开始步骤 0。**

## 执行：两趟，中间有可交付的检查点

工作目录 `/Users/yongcheng/Desktop/projects/github-action/orrery-research/`（用户为此建的空目录）。工具：WebSearch / WebFetch / `gh api`（只读）/ Algolia JSON / Mobius MCP。

**第一趟（≈10.5–11.5 h，到"切线 A"，交付 v1）**
| # | 步骤 | 产出 |
|---|---|---|
| 0 | 冻结 18 维为 `rubric.md`；建空 `claims.jsonl`；写两条基线行（FeedMob 今天 / GH Actions） | 标尺 + 台账骨架 · 0.5 h |
| 1 | **深潜 gh-aw + Copilot coding agent**：架构、safe outputs、威胁模型、4 引擎、`gh aw compile` 锁格式、logs/audit；safe-output 源码略读；top-30 issues by reactions；discussion #186451；2026-02→09 changelog；3 个 HN 帖 | 18 维记分卡；6–8 卡；OQ-1 基线；OQ-4 证据 · 2.5–3 h |
| 2 | **深潜 Temporal (+Hatchet)**：取消作用域/心跳、Schedules 重叠/补跑策略、版本化/patched、事件历史、安全模型、Cloud vs 自托管运维负担；Hatchet 并发/取消/Postgres 模型；"why we left Temporal" | L4 自写 vs 嵌入证据；D3 D4 D11 D14 卡 · 2 h |
| 3 | **深潜 Windmill (+n8n/Activepieces/Kestra profile)**：审批步、自动表单、CLI/git sync、变量/资源权限、worker 隔离、带错误处理的调度；n8n 口碑（Reddit/G2）+ CVE 史 | D7 D9 D15 D16 卡；非工程师编写证据 · 2 h |
| 3b | **半深 Kamal（+Argo CD / Spacelift profile）**：Kamal 的 destinations、healthcheck、rollback、accessories 与 SSH 模型（与你们 Docker + SSH 到 EC2 的形态最近）；Argo CD 漂移检测与回滚；Spacelift plan→apply 与 OPA 策略——对应第八组 #55–63 | D19 卡；OQ-3 部署楔子证据 · 1.5 h |
| 4 | **深潜 Claude Managed Agents + Routines (+Codex 对照；Paperclip 半深)**：session 定价、沙箱/密钥、session log、触发类型与限制（1h 最小）、stop/interrupt 语义、routine 输出如何被审；`codex exec` 退出码、`/goal` 预算；Paperclip 组织/预算/治理模型 | D5 D12 D13 D15 D18 卡；OQ-7 证据 · 2 h |
| 8a | 综合：写中文 artifact v1（17/19 维有卡；附录薄）、由标签生成第 4 章、Mobius 评论、上传 `claims.jsonl` | **v1 交付** · 1.5 h |

**第二趟（≈6–7 h，到"切线 B"，交付 v2）**
| # | 步骤 | 产出 |
|---|---|---|
| 5 | 剩余 ~35 核心产品 profile，每个 ≤25 min（首页 + 文档目录 + 许可 + `gh api repos` + top-10 issues + 1 HN 帖 → 一段 + 命中维度 + 1–3 卡） | 附录 A；长尾卡 · 3 h |
| 6 | 口碑扫描：4 深潜 + 8 主要（Devin、OpenHands、Dagster、Airflow、Inngest、Trigger.dev、Zapier、Backstage/Port、Rundeck） | 2.#.3 口碑块 · 1.5–2 h |
| 7 | 墓碑：Earthly、BuildJet、Airplane、Agent Builder、OpenClaw、Pipedream、Drone/Codefresh、StackStorm/Hubot、Sweep（创始人帖 + HN + Wayback） | 墓碑墙 · 1 h |
| 8b | 更新 artifact（同 URL 重发）与 Mobius 评论 | **v2 交付** · 1 h |

**停止规则**：profile 25 分钟盒；20 分钟内文档触及 <6 维则降为长尾；深潜 3 小时硬停；综合时无 URL 的声明一律删除；某维度零 A 级卡 → 如实写"未找到可信外部答案"（这本身是发现——标记 Orrery 的差异化空间）。

**"只有 6 小时"变体**：步骤 0 + 深潜 1、2 + 1 小时综合 → 交付 Tier A 覆盖、OQ-1/3/4 证据、L4 自写 vs 嵌入问题，其余列为待办。

## 明确不做

不写代码；不编辑 charter artifact（`3a7f276e…`）或前次报告（`c002ecb5…`）；不改 AI-2564 的标题/状态/字段（只加评论 + 附件）；不因通知或唤醒而重发 artifact（当前两个 artifact 的自动回复订阅因中断而暂停，仅在用户要求时恢复）。

## 验证

1. **标尺完整性**：`rubric.md` 18 维每维都有"FeedMob 今天"与"GH Actions 现任"两行，且每行引用 Explore 报告中的文件路径（如 `mobius/lib/agent-runs.ts`、`hermes-profile/cron/jobs.json`）。
2. **台账可解析**：`jq -c . claims.jsonl | wc -l` 无报错；`jq -r .evidence_grade claims.jsonl | sort | uniq -c` 的 A 级占比 ≥30%；`jq -r 'select(.evidence_url=="" or .evidence_url==null)' claims.jsonl` 为空。
3. **卡片八字段**：`jq 'select(has("claim_zh") and has("mechanism_en") and has("evidence_url") and has("adoptability") and has("charter_tags"))' claims.jsonl | wc -l` 等于总行数。
4. **第 4 章可追溯**：章程变更提案每一行的证据引用，在台账里 `jq --arg t "L2" 'select(.charter_tags|index($t))'` 能查到 ≥1 张 A/B 级卡。
5. **口碑门槛**：`sentiment=complaint` 且进入正文 2.#.3 的条目 `recurrence_count ≥3` 且 `source_platforms|length ≥2`。
6. **五问一致性**：每个深潜产品的五问答案在正文与附录 A 一致；≥3 个"否"的产品不出现在 Tier A 的优点卡里（除非标注为"反面证据"）。
7. **交付到位**：artifact 在浅色/深色主题下可读（图内颜色走 token）；Mobius 评论 ≤1500 字且含 artifact 链接；`claims.jsonl` 出现在 AI-2564 附件列表（`get_issue` 的 `attachments`）。
8. **不越界**：`get_issue` 显示标题/状态/字段未变；charter artifact 版本号未变。

## 关键文件

- `AI-2564`（Mobius `get_issue`）— 章程层/阶段/OQ 的真源；浓缩评论与附件的目的地
- `https://claude.ai/code/artifact/3a7f276e-bdfe-4e56-9c3a-9680200a8bcc` — 现有 charter，第 5 节标签所指的元素（只读引用，不编辑）
- `https://claude.ai/code/artifact/c002ecb5-3661-4b39-b476-85b1315c51ea` — CI/CD 层前次调研（只读引用，不重复）
- `orrery-research/rubric.md` — 冻结的 18 维与基线行（步骤 0 产出；未建立前不采集任何卡）
- `orrery-research/claims.jsonl` — 证据台账；第 4 章与 Mobius 附件由此生成
- `orrery-research/competitor-collection.zh.html` — 合集 artifact 源文件（沿用前两份 artifact 的 token 与字体系统：Archivo / Source Serif 4 / JetBrains Mono，深松绿 accent）
- Explore 报告中的基线证据：`mobius/prisma/schema.prisma`、`mobius/integrations/PAIRING.md`、`mobius/integrations/hermes-mobius-plugin/EVENTS.md`、`mobius/lib/agent-guide.ts`、`mobius/lib/permissions.ts`、`mobius/lib/agent-runs.ts`、`mobius/lib/agent-health.ts`、`adops-agent-repo/hermes-profile/cron/jobs.json`、`adops-agent-repo/.github/workflows/sync-hermes-profile.yml`
