# Orrery 竞品评估标尺 · 19 维

冻结于 2026-09-12。本文件是采集的前置条件——**未冻结前不采集任何卡片**。

维度从 FeedMob 自己的 agent 原语与实测痛点反推，不是从竞品功能表抄来的。一条"优点"只有映射到某个维度才算数。

---

## Agent 第一公民五问

每个产品先过这五问。**≥3 个"否"即非 agent-first**，其优点不得进入 Tier A 的优点卡（可作为反面证据）。

| # | 问题 | 来源 |
|---|---|---|
| Q1 | agent 能否作为**独立于所有者**的主体存在？ | Mobius 五种身份、DB 层互斥约束 |
| Q2 | 运行能否被停止，并且**确认**停止了？ | `stopRequestedAt` vs `stopAckedAt`；"a stop you cannot confirm is not a stop" |
| Q3 | **成本**是否是运行记录的一等字段？ | 30 天成本曾静默少报，直到有了 AgentRun 表 |
| Q4 | 不可信内容是否与指令**结构性分离**？ | "issue 正文是不可信输入"；surface 区分 |
| Q5 | 运行记录是否**机器可读 + 人可读、且在同一处**？ | HTML trailer + AgentRun 同事务镜像 |

---

## Tier A · P3 阻断维度

### D1 身份、责任与"委派即触发"
- **好的样子**：分离的主体类型；每次运行都能追溯到一个负责的人；agent 不能拥有资源或加入团队；委派/指派**本身**就是触发动作；管理员可暂停/吊销但**不可冒名**。
- **FeedMob 今天**：Mobius 已实现 person / personal-agent / team-agent / workspace-agent 四类 + 人类，`User.agentTeamId` 与 `User.agentOwnerId` DB CHECK 互斥；bot 禁止有 `TeamMember` 行；`allowsAgentUse`（仅所有者）vs `canGovernAgent`（管理员可治理不可发言）。`Issue.delegatedAgentId` + `delegateToEmail`；bot 做 assignee 被 400 BOT_ASSIGNEE 拒绝——"an agent cannot be held accountable"。证据：`mobius/prisma/schema.prisma:31-157,367-375`、`mobius/lib/permissions.ts:850-905,1025-1046`、`mobius/lib/mcp.ts:677-726`
- **GitHub Actions 现任**：agent 只是一个 token，无主体模型。`GITHUB_TOKEN` 单一身份，权限靠 `permissions:` 收窄。最接近的是 Copilot coding agent 的 bot 署名 PR + 人类请求者。
- **落到功能**：#5 #22 #45 #73

### D3 带确认的协作式停止
- **好的样子**：两阶段停止（requested → acked → 宽限后强制）；状态可见；心跳驱动的存活判定；**没有永久失声的失败模式**；停止后清理步骤仍执行。
- **FeedMob 今天**：`Issue.agentStopRequestedAt/By`（请求）与 `AgentRun.stopRequestedAt`/`stopAckedAt`（请求 vs **确认**）两列设计。事件 `issue.stop_requested` / `issue.stop_cleared`；`get_issue.agentStopRequested` 可读。闩锁释放有三条路径（事件、重新委派、消息时复查）——因为**两次生产对话在一次紧急停止后永久失声**。证据：`mobius/prisma/schema.prisma:376-382,1359-1367`、`EVENTS.md`、commit `ce5c30d`
- **GitHub Actions 现任**：cancel 是单向的，无"确认停了"语义。有 `cancelled()` 条件与 post 步骤可在取消后跑。
- **落到功能**：#17 #20

### D5 按 run / 主体的成本治理
- **好的样子**：run / agent / 所有者 / 团队四级预算；**三重独立硬停**（轮次、墙钟、美元）；成本与运行记录**同事务**写入；预测 + 告警去重；触顶失败要**响**，不能静默。
- **FeedMob 今天**：`maxTurns` 40、`maxSessionSeconds` 900、`maxSessionsPerHour` 6、可选 `maxCostUsdPerHour`。教训：**`maxTurns` 约束的是轮次不是时间**——挂住的工具/模型调用需要独立的墙钟护栏。30 天成本曾靠扫描最近 3000 条评论**推导**，20–30 个 agent 时该窗口只覆盖两三天，**静默少报**，直到加了 `AgentRun` 表。LiteLLM 已在用（预算/虚拟 key/花费日志）。证据：`mobius/agent-worker/README.md`、`worker.ts:245,390`、`schema.prisma:1333-1341`、commit `8034b82`
- **GitHub Actions 现任**：spending limit 仅账号级。触顶时 job **静默不启动**——2026-08-19 `feedmob-parallax` 主干连挂三次、job 存活 3 秒即此。正面参照：GitHub 的 annotation 文案本身是清楚的，静默的是"没人看见"。
- **落到功能**：#21 #51

### D7 审批门 / 人在环中
- **好的样子**：一等的"等待审批"步骤，带超时与升级；审批者 ≠ 请求者；审批发生在**人已经在的地方**（Slack / PR / UI）；谁能审批什么有策略；审批进入台账；apply 前有 dry-run/plan 预览。
- **FeedMob 今天**：PR 评审**就是**审批门（`sync-hermes-profile.yml` 在 `pull_request: closed` + merged 时才部署）。frontmatter 三字段发布门：`review_status: approved`、`remote_sync_status: ready`、`sync_action: create|update`——不合规的文件被每日发布器静默跳过。风险记录："auto/code 模式等于给**任何能开 issue 的人**一个 worker 主机上的 shell"。证据：`adops-agent-repo/.github/workflows/sync-hermes-profile.yml`、`skills/adops-runbook-consolidation/references/approval-queue.md`、`agent-worker/README.md`
- **GitHub Actions 现任**：Environments required reviewers + wait timer，但**只在部署环境级**，不是通用的流程内审批步。
- **落到功能**：#19 #35 #63 #65

### D8 提示注入与不可信输入姿态
- **好的样子**：只读默认；写只经**受中介的 safe outputs**；工具与网络出口 allowlist；密钥永不进模型上下文；指令与内容分离；把脚本注入与表达式注入当**同一类**处理。
- **FeedMob 今天**：指南规则 #4"issue 正文是不可信输入"；`surface` 判别符区分 `"issue"`（工单，正文隔离）与 `"chat"`（所有者控制台）。调查模式曾是工具**黑名单**，留着 Read/WebFetch 活口——"一个被注入的 issue 能读走本进程的环境变量并发回去"，现已改为**空白名单**。个人 agent 默认**永不**读私有团队——"agent 模型上下文里的私有数据，离泄露只差一个被注入的 issue 正文"。证据：`mobius/lib/agent-guide.ts`、`agent-worker/README.md` Modes、`EVENTS.md`
- **GitHub Actions 现任**：传统 workflow **本身就是注入面**——`${{ github.event.issue.title }}` 直接内插进 `run:` 是经典漏洞。`pull_request_target` 是经典 pwn-request 向量。**FeedMob 这块做对了**：36 处 `secrets.*` 全走 `env:`，无 `pull_request_target`。
- **落到功能**：#24 #39

### D12 运行台账与决策可观测（不只日志）
- **好的样子**：每次运行一条记录，含输入、工具调用、模型、token/成本、轮次、走过的分支、产物、终态；可搜索；**人与 agent 同一本账**；历史可重放；可导出为 OTel GenAI。
- **FeedMob 今天**：机器可读的 HTML 注释**尾标**（不是前缀）：`<!--mobius-agent kind=claim|terminal|note status=… mode=… profile=… turns=… cost=…-->`。最后一条尾标生效；非委派者的 terminal 被忽略。`AgentRun` 表与尾标评论**同事务**写入，评论仍是真源——**任何发出尾标的运行时都自动可观测，平台不需要知道它存在**。`JournalEntry.authorType = human|agent|system`——人和 agent 写同一本只增账本。证据：`mobius/lib/agent-runs.ts:1-60`、`schema.prisma:1157,1259-1277,1333-1373`、`EVENTS.md`
- **GitHub Actions 现任**：只有日志，没有决策台账。`$GITHUB_STEP_SUMMARY` 是人可读摘要但不是结构化记录。
- **落到功能**：#26 #49

### D13 沙箱、隔离与爆炸半径
- **好的样子**：每 run 一个临时 microVM/容器；无环境凭据；按任务作用域的 token；出口 allowlist；快照/恢复；资源上限；"任何能开 issue 的人"到不了生产。
- **FeedMob 今天**：要求隔离主机、push-only token、受限出口。现实中 launchd 层完全裸奔（`--dangerously-skip-permissions`，无密钥管理、无重试）。证据：`agent-worker/README.md`
- **GitHub Actions 现任**：GH-hosted 是干净 VM（每 job 全新）；self-hosted 需自管，官方文档明确警告不要在公共仓库用。Copilot coding agent 有防火墙 allowlist。
- **落到功能**：#16 #44

---

## Tier B · 平台质量维度

### D4 存活与健康
- **好的样子**：租约/心跳带过期；连续失败策略；陈旧检测；告警分组去重。
- **FeedMob 今天**：`STALE_AFTER_MS = 7d`、`FAILURE_STREAK = 3`、`COST_WINDOW_MS = 30d`，alert-key 去重（陈旧的 agent 只被告知一次）。设计注释："一个死掉的运行时不会宣告自己：它的 token 只是不再被使用，唯一痕迹是逐渐变老的 `lastSeen`"；"配置错的运行时**每次都以同样方式失败**——3 次连续失败是模式，1 次是运气不好"。证据：`mobius/lib/agent-health.ts:1-15`
- **GitHub Actions 现任**：`renewjob` 每 60s 把 `lockedUntil` 推后 10 分钟——runner 一死锁到期、服务端回收 job。这是**租约基线**，值得抄。
- **落到功能**：#23

### D6 投递 ≠ 成功 ≠ 综合
- **好的样子**：每阶段独立结果；投递是自己可重试的步骤，有死信；部分成功状态；通知去重。
- **FeedMob 今天**：Slack `channel_not_found`——job 成功但投递失败。规则因此写死："这三件事分开报"：`last_status`（job）vs `last_delivery_error`（投递）vs 综合。证据：`adops-agent-repo/hermes-profile/cron/jobs.json`、`skills/adops-wiki/references/adops-cron-automation.md`
- **GitHub Actions 现任**：`if: always()` + `continue-on-error` + job outputs，弱且全靠手写。
- **落到功能**：#30 #50

### D9 密钥处理与输出脱敏
- **好的样子**：按步注入而非进程环境；日志自动打码；**拒绝**含密钥的输出（push-protection 模式）；OIDC 短期凭据；默认最小权限 token；按 run 作用域；访问审计。
- **FeedMob 今天**：失败评论**脱敏**——"SDK 错误可能引用失败的请求，连 header 一起"。服务端拒绝任何含活 token 的评论/issue 正文。受管 MCP 服务器的 header 可存密钥，在管理 API 里**打码**，只为 worker 的 bot PAT 解析。"token 绝不能铸 token"（配对时批准必须来自浏览器会话）。指南 v1 曾让 agent 从**私有**仓库复制目录——"一条任何 agent 机器都执行不了的指令"，改为服务端自供插件 tarball。证据：`agent-worker/README.md`、`schema.prisma:899-920`、`integrations/PAIRING.md`、`lib/agent-guide.ts` v2 note
- **GitHub Actions 现任**：secrets + `::add-mask::` + OIDC + `permissions:`，**这一层 GH 做得好**，是对等目标不是差异化点。
- **落到功能**：#29 #48

### D10 内容哈希幂等 / no-op
- **好的样子**：引擎原生的输入指纹 → 跳过；幂等键；事件去重；"有没有变"是一等谓词；内容寻址缓存。
- **FeedMob 今天**：cron 三段链——`no_agent: true` 的脚本 job 抓取并写带内容哈希的状态文件，稍后的 LLM job **在哈希未变时 no-op**。证据：`hermes-profile/cron/jobs.json`
- **GitHub Actions 现任**：无对等，全靠手写。`actions/cache` 的 key 是猜谜、miss 静默、淘汰不透明。
- **落到功能**：#7 #28 #67

### D11 只增不改的契约与版本
- **好的样子**：版本化的 workflow schema；跨版本确定性重放；编译/锁定产物；SHA 钉住；机器可读的 run schema。
- **FeedMob 今天**：`PAIRING.md` 与 `EVENTS.md` 都写着"状态：已实现——**本文档先于代码写成，代码照着它建**"。客户端必须忽略未知字段/类型；破坏性变更必须走新路径（`/v2`），绝不原地改。这让 Hermes 插件装一次就能在后端上线后**自升级**到实时事件流。指南版本 `AGENT_GUIDE_VERSION = "4"`，只增式，bump = "重读这个"。证据：`integrations/PAIRING.md`、`EVENTS.md`、`lib/agent-guide.ts`
- **GitHub Actions 现任**：靠 git；action 版本靠可变 tag（tj-actions 2025-03 供应链事故的根因）。
- **落到功能**：#11 #52 #54 #70

### D14 触发器、调度器健壮性与事件传输
- **好的样子**：cron 带补跑/重叠策略（跳过/排队/取消）、按调度的超时、git 热重载、暂停/恢复、时区与夏令时、抖动、backfill；事件至少一次 + 幂等 + 从 cursor 重放；签名 webhook。
- **FeedMob 今天**：长轮询 `GET /api/mcp/events` 带 cursor ack，`deliveredAt`/`ackedAt` 在聊天 UI 渲染为**已读回执**（"两个勾"）；打字信号、在线状态、至少一次、7d/14d 保留、只增式。痛点：Hermes cron 脚本超时对多条目 Femini 导出**太短**；**调度器不重启就不加载配置变更**。证据：`EVENTS.md`、`lib/agent-events.ts`、`adops-cron-automation.md`
- **GitHub Actions 现任**：`on: schedule` 有延迟、**60 天不活动自动禁用**、无重叠策略。
- **落到功能**：#2 #4 #36 #37 #38 #68

### D19 部署原语与环境模型
- **好的样子**：部署目标/环境是一等对象；SSH/主机指纹/跳板机内建；幂等的"拉→切→健康检查"；一键回滚；"什么版本在哪台机器上"的视图；漂移检测；部署策略；策略即代码（谁能部署到哪、冻结窗）。
- **FeedMob 今天**：`feedmob-pixel-dashboard/deploy.yml` **681 行手写**，`docker/login-action` 出现 9 次、`ruby/setup-ruby` 7 次、`webfactory/ssh-agent` 3 次。2026-09-02 两次主干失败都倒在第 6 步 `Add server to known_hosts`，连带 `release_backend`/`release_frontends`/`release_summary` 三个 job；当时的提交标题正是"Retry SSH host-key scans during deployment"和"Update deployment jump host"——反复试错，唯一调试手段是推一次看一次。无回滚原语、无"什么版本在哪"视图、`ghcr-cleanup.yml` 手写。
- **GitHub Actions 现任**：Environments（URL、密钥、required reviewers、wait timer）+ Deployments API，**但没有部署原语**——SSH、健康检查、回滚全靠手写。
- **落到功能**：#55–63

---

## Tier C · 采纳与生态维度

### D15 本地层吸收与本地=生产
- **好的样子**：同一份定义本地与云上都能跑；一条命令接管现有 launchd/cron；本地密钥；厂商自己的 routine 能互操作。
- **FeedMob 今天**：个人自动化层 = launchd plist → `claude -p "/monitor-pipeline" --dangerously-skip-permissions` 或 `codex exec` → 文件输出；**无密钥管理、无重试**，失败靠 macOS 通知（`osascript`）。`multi_agent_poster` 甚至在驱动 Terminal 窗口（`tab 1 of window id …`）。证据：`monitoring_agent_auto/`、`monitoring-auto-post/`、`multi_agent_poster/`
- **GitHub Actions 现任**：**做不到本地跑**——"没有本地执行、日志可见性有限、edit-commit-push-wait 循环"是它被骂最狠的一条。
- **落到功能**：#31 #34

### D16 非工程师编写与自助
- **好的样子**：自然语言或表单编写 + 护栏；模板/黄金路径；Slack/Mobius 入口；跑之前先解释；成本预览；安全默认值。
- **FeedMob 今天**：广告运营与销售是目标用户（"每个人都使用"）。现有的非工程师接触面只有 frontmatter 三字段发布门。
- **GitHub Actions 现任**：`workflow_dispatch` inputs + starter workflows，但仍要碰 YAML。
- **落到功能**：#3 #33 #42 #76

### D17 技能/工具/MCP 打包、路由与审核
- **好的样子**：带元数据的版本化技能包；带审核与来源的中央注册；按 agent 的 allowlist；密钥打码的 MCP 管理。
- **FeedMob 今天**：Hermes 约定——每个技能一个目录，`SKILL.md` + 可选 `mcporter.json`/`references/`/`scripts/`/`templates/`；**frontmatter `description` 就是路由面**，`AGENTS.md` 只放横切规则；adops profile 有 35 个技能。`AgentMcpServer`——管理员在应用内添加 HTTP MCP 服务器，header 可存密钥、在管理 API 打码，stdio 仅限文件。证据：`adops-agent-repo/README.md`、`hermes-profile/skills/*/SKILL.md`、`schema.prisma:899-920`
- **GitHub Actions 现任**：Marketplace action（可变 tag），tj-actions 2025-03 是供应链反面教材。
- **落到功能**：#11 #12 #72

### D18 多运行时 / 模型无关
- **好的样子**：按 run 可插拔引擎；BYO key；跨引擎**相同的运行契约**；LiteLLM 式路由。
- **FeedMob 今天**：Claude Code + Codex + `pi` + Hermes 四种运行时并存。Mobius 规则："agent 自带模型"——agent token 被拒绝使用平台 AI（`triage_issue`/`workspace_briefing`/`ask_mobius`）。LiteLLM 已在用。运行契约（尾标）是**引擎无关**的——任何发出尾标的运行时都可观测。证据：`lib/permissions.ts` `assertCanUseAi`、`lib/agent-runs.ts`
- **GitHub Actions 现任**：无（`GITHUB_TOKEN` 单一身份）。gh-aw 支持四引擎是其亮点。
- **落到功能**：#18 #14

### D20 自进化与变更控制
- **好的样子**：agent 只能经 PR/评审改自动化；对 workflow 变更有策略即代码；定义的金丝雀与回滚。
- **FeedMob 今天**：`hermes-agent-self-evolution` 护栏 #5："所有变更走人审，**绝不直接提交**"（5 个阶段只实现了阶段 1）。GEPA/DSPy 技能进化 → 最佳变体 → 对 hermes-agent 开 PR。远端 `feed-mob/adops-agent` 每日 00:15 用 `peter-evans/create-pull-request@v6` 开 PR。证据：`hermes-agent-self-evolution/README.md`、`PLAN.md`
- **GitHub Actions 现任**：无原生；Dependabot for actions 是最接近的（自动开 PR 升 action 版本）。
- **落到功能**：#35 #54

---

## 采集规则

**证据等级**
- **A** = 官方文档/代码 + ≥2 个独立用户来源（issue / HN / Reddit / 迁移博客）
- **B** = 仅官方文档或源码
- **C** = 仅厂商声称（博客、落地页）

**可采纳方式**：`copy` 抄模式 ｜ `fork` fork 代码 ｜ `integrate` 直接集成 ｜ `avoid` 避开

**口碑门槛**：抱怨需 ≥3 个独立作者、跨 ≥2 个平台才算"反复出现"；赞需 ≥2。

**硬规则**
1. 卡片八字段缺一不成卡。
2. 无 URL 的声明在综合时一律删除。
3. 某维度零 A 级卡 → 如实写"未找到可信外部答案"——这本身是发现，标记 Orrery 的差异化空间。
4. 五问 ≥3 个"否"的产品，其优点不进 Tier A 卡（可作反面证据）。
