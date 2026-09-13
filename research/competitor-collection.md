# Orrery 竞品优点合集

竞品调研 · AI-2564 · 2026-09-12

**规模**：55 个产品、19 个评估维度、**158 张优点卡**、23 条反复出现的抱怨、**14 块墓碑**。三个并行工作流、34 个 agent、1450 次工具调用、约 390 万 token。

**方法的一句话**：评估维度不是从竞品功能表抄来的，是从 **FeedMob 自己踩坑换来的 agent 原语**反推的。一条"优点"只有映射到某个维度才算数。

---

## 0 · 一页摘要

### 0.1 最值得"偷"的十条

| # | 维 | 来源 | 做法 | 对 Orrery 的意义 |
|---|---|---|---|---|
| 1 | D5 | **Devin** | ACU 用**减法**定义：等人回话、等测试、clone 仓库一律**不计费**；30 分钟无活动自动 sleep | 让"人在环中"与"成本控制"不再对立。别家按时长计费时，每设一道审批门都在烧钱，产品就本能地少设门 |
| 2 | D5 | **LiteLLM**（已在用） | `agent_id`/`session_id` 是**真实建表列**，另有按 agent 归集的 `LiteLLM_DailyAgentSpend` 日聚合表 ⚠ 但 `max_budget_per_session` **不是硬闸**（见 §2.5） | 台账可直接用——`run_id` 绑进 `x-litellm-trace-id`，**OQ-2 变成一次聚合查询**。但**熔断必须 Orrery 自己做** |
| 3 | D8 | **gh-aw** | agent 永远只读；写操作吐结构化 JSON 请求，由另一个按操作类型授最小权限的 job 落地。约 50 种 safe output，每种带 `max:` 上限 | 部署域直接照抄：agent job 不持有 SSH 凭据，只能产出 `{"type":"deploy","target":...}`。681 行 deploy.yml 里真正危险的只有几个动词 |
| 4 | D3 | **Temporal** | 取消拆成两条事件（`CancelRequested` / `Canceled`）+ 强杀第三态；清理代码放 `nonCancellable` 作用域，**取消信号进不去** | 印证 Mobius 的 `stopRequestedAt`/`stopAckedAt` 是对的，并补上第三态。"两次生产对话永久失声"的正解 |
| 5 | D1 | **WorkOS** | 令牌用 `sub` + `act` 双 claim 同时表达"谁在干"和"谁授权的"，全程复用 RFC 8693，无私有协议 | Mobius 在工单层区分了分配 vs 委派，但**委派没落到令牌上**。补上这半，D7 判 `act`、D12 两列不塌缩、D5 可双维度切 |
| 6 | D1 | **Entra Agent ID** | 每个 agent 必须挂人类 sponsor；sponsor 离职时**自动转交其经理** | 全组唯一把"追责"写成机制而非日志的设计。加 `sponsor_user_id` 非空约束 + 授权带 `expires_at` |
| 7 | D16 | **Backstage / Port / Rundeck** | 三家收敛到同一条：**永远不给自由文本框**，合法值来自系统自己知道的真实清单 | `known_hosts` 反复试错、deploy.yml 抄 8 遍，根子都是"参数靠人手敲"。部署表单做成"从已知服务器里选"，两个痛点在提交前就消失 |
| 8 | D11 | **GitLab CI** | 把**可选的复用**（components + catalog）与**不可选的强制**（pipeline execution policies）拆成两套独立机制 | FeedMob 的痛点同时含"重复"（复用问题）与"12/13 无超时"（强制问题）——**用同一套机制解不了** |
| 9 | D14 | **Tekton / Argo / Dagger** | 三家形状完全相同：**默认值属于平台，不属于流水线作者**；作者可覆盖，不可遗漏 | 这是"12/13 个 workflow 没超时"的结构性解释——GH Actions 没有组织级默认超时开关，"忘记写"必然是常态 |
| 10 | D13 | **Vercel Sandbox** | 文档**把自己的绕过面写进去**：SNI 匹配挡不住 domain fronting、`subnets.allow` 会放开 DNS（可做隧道外泄）、空策略等价 deny-all | 这份"把失效边界写清楚"的纪律本身比任何功能都值得偷 |

### 0.2 五条墓碑教训

| 模式 | 证据 | 对 Orrery 的硬约束 |
|---|---|---|
| **执行是商品** | Earthly 告别帖原话 "CI compute being viewed as a commodity"；BuildJet "The gap we set out to fill has largely closed" | 价值主张只要写成任何形式的"跑得更快/构建更可靠"，**就在排队等死** |
| **可视化编排被它自己的发明者放弃** | OpenAI 2025 DevDay 发布 Agent Builder，2026-06-03 宣布弃用，11-30 停机，迁移方向是 code-first | 编排真相必须住在**仓库的代码/声明文件**里，UI 只是渲染与触发面 |
| **品类塌陷是底座被抽走** | Hubot 今天仍有 16,795 星、2026 年初还发 v14，但 Slack 的 `hubot-slack` 适配器 2024-08-12 归档、classic apps 2026-11-16 停止工作 | 命令层必须**平台无关**，Slack / Mobius 只能是可替换适配器 |
| **最像 Orrery 的那个死于组织** | Airplane.dev（Tasks/Runbooks/Schedules/Views）账上有钱、客户满意、收入在涨，CEO 去了 Airtable，客户拿到不到三个月迁移期 | 自研是结构性优势，但对应风险是 bus factor。**硬约束：Orrery 挂掉时，8 个仓库必须仍能用原生 GitHub Actions 部署** |
| **agent 的账单是授权边界不是能力** | OpenClaw 73 天内 100 条安全公告（51 条 high），几乎全是同一类缺陷：批准的 exec 活过了被审阅的工作目录、语音继承 owner 权限。ClawHavoc：2,857 个技能里 341 个恶意（12%） | 不能有技能市场，不能让未审阅的第三方代码进 agent 执行路径。授权绑定到**具体仓库 + 环境 + 时间窗**，过期失效、不可继承、不可跨会话复用 |

### 0.3 章程要改的地方

| 元素 | 现状 | 证据说什么 | 详见 |
|---|---|---|---|
| **L2 执行器** | 复用 `nektos/act` | **act 已停更 + 按设计没有编排层** | §4.1 |
| **P2 楔子 (OQ-3)** | 四选一待定 | 墓碑证据指向**治理与确定性层**，不是引擎 | §4.2 |
| **L4 调度核心** | 自建状态机 | Temporal 无成本概念；LiteLLM 已解决成本归属 | §4.3 |
| **D1 身份** | 沿用 Mobius 五身份 | 需补令牌层（RFC 8693）与 sponsor 问责 | §4.4 |
| **新增硬约束** | — | Orrery 挂掉时必须能退回原生 GH Actions | §4.5 |

---

## 1 · 方法与范围

### 1.1 怎么做的

| 工作流 | agent | 产出 |
|---|---|---|
| **深潜** | 5 × [调研 → 对抗核验] = 10 | 5 个目标、78 张卡 |
| **群扫** | 10 组 + 墓碑考古 = 11 | 50 个产品、80 张卡、14 块墓碑 |
| **口碑挖掘** | 13 | 39 个产品的用户口碑块、125 条声称裁定（56 升 A / 49 维持 B / **20 条被推翻**） |

**两道核验都起了作用。** 第一道（对抗核验）：：深潜阶段调研员自评给出的等级，被核验员逐条访问 URL 之后**全部降级**——78 张卡最终 58 张 B、20 张 C，**零张 A**。其中一张 Claude Managed Agents 的停止语义卡被抓到**引用错页**（`session-operations` 页根本没有它引的那段话），直接降 C。

### 1.2 证据等级，以及为什么 A 级这么少

口碑挖掘的 13 个 agent 对 125 条文档级声称逐条去找独立用户来源，裁定结果：

| 裁定 | 条数 | 含义 |
|---|---|---|
| **upgrade-to-A** | 56 | 找到 ≥2 个独立用户来源印证，升 A |
| **stays-B** | 49 | 找不到用户层面的证据——这是常态，不是失败 |
| **contradicted** | **20** | **用户证据反驳了文档**，降 C（见 §2.5） |

最终分布：

| 等级 | 定义 | 数量 |
|---|---|---|
| **A** | 官方文档/代码 **+ ≥2 个独立用户来源** | **61**（31%） |
| **B** | 仅官方文档或源码 | 105 |
| **C** | 仅厂商声称，或**被用户证据反驳** | 29 |

**20 条被推翻的声称是整轮研究最有价值的产出**——每一条都是"文档这么写、实际不是这样"，而且**有几条正好落在我原本准备抄的设计上**。做技术选型时，B 级 = "文档这么写的"，不等于"它在真实环境里真是这样"。

### 1.3 竞品全景

| 类 | 深潜 | 核心 profile | 墓碑 |
|---|---|---|---|
| **兼容先例** | — | Gitea Actions、Forgejo Actions、nektos/act、gitea/act、act_runner | — |
| **CI/CD 引擎** | — | GitLab CI、Buildkite、Woodpecker、Jenkins、CircleCI | Earthly、Drone、Codefresh、Travis |
| **K8s / 可编程管道** | — | Tekton、Argo Workflows、Dagger | — |
| **Durable execution** | **Temporal + Hatchet** | Inngest、Trigger.dev v4、Airflow 3、Dagster、Prefect | — |
| **工作流自动化** | **Windmill** | n8n、Activepieces、Kestra | Pipedream |
| **Agent 编排框架** | — | LangGraph、MS Agent Framework、Google ADK、AWS Strands、Dify | OpenAI Agent Builder |
| **Agent 原生 CI** | **gh-aw + Copilot coding agent** | Devin、OpenHands、Jules、Cursor、CodeRabbit、三家厂商 action | Sweep、Mentat |
| **Agent 运行时** | **Claude Managed Agents + Routines** | Codex、Paperclip | OpenClaw（安全危机） |
| **部署** | **Kamal + Argo CD + Spacelift** | Atlantis | — |
| **IDP / 自助** | — | Backstage、Port、Rundeck | StackStorm、Hubot |
| **沙箱 / runner** | — | E2B、Daytona、Modal、Vercel Sandbox、Cloudflare、Actuated | BuildJet |
| **维度捐赠者** | — | LiteLLM、Langfuse、Agent Skills 标准、Entra/Okta/WorkOS、Composio | — |

### 1.4 Agent 第一公民五问的结果

每个产品先过五问：能否作为独立主体？能否停止并**确认**停止？成本是否一等字段？不可信内容是否结构性分离？运行记录是否机器+人可读且同处？

**55 个产品里只有 8 个通过。**

| 深潜目标 | 判定 |
|---|---|
| gh-aw + Copilot coding agent | ✅ agent-first（3 yes / 2 partial） |
| Claude Managed Agents + Routines | ✅ agent-first |
| **Temporal + Hatchet** | ❌ not-agent-first |
| **Windmill** | ❌ not-agent-first |
| **Kamal / Argo CD / Spacelift** | ❌ not-agent-first |

> **durable execution、工作流自动化、部署这三个品类的领头羊全部不是 agent-first。** 这是本次研究最重要的结构性发现——agent 治理（身份 + 成本 + 停止 + 台账）这一层**没有现成占位者**。

开源侧尤其干净：LangGraph、Strands、ADK、Dify 里的 agent 全都只是**进程内的一个对象**，没有 ID 意义上的身份，只有一个 name 字符串。Q1 在开源侧**全军覆没**。只有三家云厂商（Entra / Google Cloud Agent Identity / AWS AgentCore）在目录/IAM 层新建了主体类型。

---

## 2 · 分维度优点合集

### Tier A · P3 阻断维度

---

#### D1 身份、责任与"委派即触发" — 8 张卡 / 8 个产品

> **FeedMob 今天**：Mobius 已实现四类 agent 身份 + 人类，DB CHECK 互斥；bot 禁止有 `TeamMember` 行；"分配给人、委派给 agent——agent 无法被追责"，bot 做 assignee 被 400 拒绝。
> **GitHub Actions 现任**：agent 只是一个 token，无主体模型。

**★ WorkOS AuthKit** `[B·fork]` — 令牌里用 `sub` 和 `act` 两个 claim 同时表达"谁在干"和"谁授权的"，全程复用 RFC 8693 委派模式，没有私有协议。
→ **落地**：Mobius 在工单层区分了分配与委派，但**委派没有落到令牌上**。Orrery 发给 agent 的令牌应携带 `sub=agent 自身`、`act=本次授权它的人`。三个下游直接成立：D7 审批门判的是 `act`，D12 台账 `sub`/`act` 各存一列（"谁批的"与"谁干的"永不塌缩），D5 成本可按 agent 和按委派人两维度切。
`https://workos.com/docs/authkit/agent-auth`

**★ Microsoft Entra Agent ID** `[B·copy]` — 每个 agent 身份必须挂一个人类 sponsor 作为问责人；sponsor 离职时 sponsorship **自动转交给其经理**，保证"永远有一个人对这个 agent 负责"。
→ **落地**：加两个必填字段 `sponsor_user_id`（不可为空）与 `sponsorship_transferred_from`（审计留痕）。agent 对 EC2 环境的部署授权一律带 `expires_at`，到期前通知 sponsor，不续期就失效——直接掐掉"某个一次性 agent 的部署密钥留了半年"。
`https://learn.microsoft.com/en-us/entra/id-governance/agent-id-governance-overview`

**★ Copilot coding agent** `[B·copy]` — 有身份但**被结构性削权**的主体：commit 由 Copilot 署名、发起人记为 co-author、加密签名显示 Verified；同时不能批准 PR、不能推默认分支（只能推 `copilot/` 前缀）、它开的 PR 上的 Actions 必须有写权限的人批准才跑、**只能访问它正在开 PR 的那一个仓库**。
→ **落地**：Mobius 已解决主体建模，Orrery 要补"削权"这一半。把"只能访问它正在操作的那一个仓库"翻译到部署域：**一次部署运行拿到的 SSH 凭据只对本次目标主机有效**，不是一把通吃 8 个仓库 N 台 EC2 的万能钥匙。
`https://docs.github.com/en/copilot/responsible-use/copilot-cloud-agent`

**★ Rundeck** `[B·fork]` — 授权是 deny-by-default 的 YAML 策略文件，**deny 先于 allow 评估**；分 application 与 project 两层 context——"能看到它存在"和"能对它做事"是两套独立权限。
→ **落地**：动作集四分 `view`/`read`/`run`/`approve`，广告运营默认拿 `view`+`run`（限特定模板），不拿 `read`。deny 先评估且不可被 allow 覆盖——这条在 agent 场景尤其重要：一条"agent 不得触碰生产 secrets"的 deny 必须**无法被任何委派链条上的 allow 抵消**。策略用 YAML 走 git 审查，同时满足 D20。
`https://docs.rundeck.com/docs/administration/security/authorization.html`

**其余**：Paperclip（agent 有职位、恰好一个上级、原子 checkout 抢任务返 409）、Port（agent 与每次调用都是目录里的 blueprint 实体，RBAC 零新增机制）、Windmill（service account 主体类型 + operator 被硬性剥夺 preview 能力）、Spacelift（access policy 拿得到 `timestamp_ns` 与 `remote_ip`）。

**→ 对章程**：D1 不需要发明协议——三家身份厂商已收敛到 RFC 8693。Mobius 的"分配 vs 委派"正好是这套凭证语义的上层。补的是令牌层 + sponsor 问责 + 授权过期。

---

#### D3 带确认的协作式停止 — 7 张卡 / 6 个产品

> **FeedMob 今天**：`stopRequestedAt` vs `stopAckedAt` 两列；曾因一次紧急停止导致**两次生产对话永久失声**，闩锁释放才加到三条路径。
> **GitHub Actions 现任**：cancel 单向，无"确认停了"语义。

**★ Temporal** `[B·copy]` — 停止拆成两条事件：`WorkflowExecutionCancelRequested`（请求已记录）与 `WorkflowExecutionCanceled`（客户端已确认取消并真的停了），另有 `WorkflowExecutionTerminated` 表示强杀。活动层同样有 `ActivityTaskCancelRequested`/`Canceled` 一对。
→ **落地**：直接印证 Mobius 两列设计是对的，并给出应补的**第三态**：`stop_requested` / `stop_acked` / `force_terminated` 三种终态分开记录，且 `force_terminated` 必须显式标注"清理逻辑未执行"。
`https://docs.temporal.io/references/events`

**★ Temporal（第二条）** `[B·copy]` — 用"可取消作用域"与"断连上下文"保证清理一定跑完：清理代码放进 `nonCancellable` 作用域 / `NewDisconnectedContext`，**取消信号进不去**。
→ **落地**：闩锁释放、Slack 收尾播报、临时凭据回收这三条清理路径必须运行在"不可被本次取消打断"的作用域里——**直接回应"两次生产对话永久失声"那次事故**。
`https://typescript.temporal.io/api/classes/workflow.CancellationScope`

**★ Windmill** `[B·copy]` — 停止拆两级：`cancel` 是协作式（worker 自己收尾，写 `canceled`/`canceled_by`/`canceled_reason`），`force_cancel` 是**独立的 API 端点**；两者按同一把读锁鉴权，分别记 `jobs.cancel` 与 `jobs.force_cancel` 两种审计操作。
→ **落地**：Mobius 两列已比 Windmill 更明确。要补抄两条：①force 必须是独立端点和独立审计动作，不能是 cancel 的一个布尔参数；②**分享链接一律只读**——agent run 的分享链接绝不能带停止能力。
`https://github.com/windmill-labs/windmill/blob/main/backend/tests/jobs_read_auth.rs`

**★ Devin** `[B·copy]` — 状态枚举把停止做成两阶段：`suspend_requested` → 真正停住；恢复同理有 `resume_requested`；并区分"从前端请求"与"从 API 请求"。
→ **落地**：`stop_requested_at` 与 `stopped_at` 是两个字段，**二者之差就是"agent 收尾花了多久"**，可直接用来定义 D4 的僵死判据（超过 N 秒仍未 stopped = 强杀并记一次连败）。停止请求带 `requested_by` 与 `requested_via`（human-ui / api / watchdog / budget-guard）——预算闸门触发的停止和人点的停止，事后通知与重试策略必须不同。**这条把 D3 和 D5 缝在一起。**
`https://docs.devin.ai/api-reference/v1/sessions/list-sessions`

**★ gitea/runner** `[B·copy]` — 取消是双方协商、以确认收尾的两阶段协议，不是 `kill -9`；而且"我已停止"这句话走的是**一条与被取消上下文分离的通道**。
→ **落地**：agent 运行时注册时声明支持哪一档停止语义；停止请求**写进 run 记录而不是发信号**，由 agent 下次心跳取回；跑完 post-step（回滚半成品部署、恢复 known_hosts、删临时 SSH key）后用独立连接回报"已停止 + 停在第几步"；未确认的 run 由 zombie reaper 超时后定性为 cancelled 并标注"未确认"。**这把"带确认的停止"从概念变成了可实现的线协议。**
`https://github.com/go-gitea/gitea/blob/main/routers/api/actions/runner/runner.go`

**其余**：Claude Managed Agents（`user.interrupt` 入队即返回，`processed_at` 保持 null 直到真正应用）、MS Agent Framework（未答复的审批与 workflow 状态在同一份 checkpoint，重启后重新发出）。

**⚠ 反例**：Managed Agents 的 `stop_reason` 不区分被打断与自然结束。**Orrery 必须区分** `completed` / `stopped_by_human` / `stopped_by_budget` / `stopped_by_timeout`。

---

#### D5 按 run / 主体的成本治理 — 13 张卡 / 11 个产品

> **FeedMob 今天**：`maxTurns` 40 / `maxSessionSeconds` 900 / `maxSessionsPerHour` 6 / 可选 `maxCostUsdPerHour`。教训："`maxTurns` 约束的是轮次不是时间"。30 天成本曾靠扫最近 3000 条评论推导而**静默少报**。
> **GitHub Actions 现任**：spending limit 仅账号级。触顶时 job **静默不启动**——2026-08-19 主干连挂三次即此。

**这是本次研究卡片最多、也是落差最大的一维。核心结论：没有一家完整做到，最好的答案是三家拼起来。**

| 子问题 | 谁解得最好 | 做法 |
|---|---|---|
| **口径**（一次 run 花了什么） | **Devin** | ACU 减法定义：等人、等测试、clone 一律不计；30 分钟无活动自动 sleep |
| **归属**（算在谁头上） | **OpenHands** | 组织/默认成员/成员覆写三层；人均预算故意做成 **lifetime 而非月度**（新人风险窗口）；80/90/100 三档告警 |
| **闸门**（到上限怎么办） | **Cursor** | 明确区分 spend alerts（只通知）与 spend limits（真拦），并直说 "Enforcement is not instant" |

**★ Devin ACU** `[A·copy]` ← **本次唯一的 A 级成本卡**
→ **落地**：run 记录设两个分开的时长字段：`agent_active_seconds`（计入成本）与 `awaiting_human_seconds`/`awaiting_external_seconds`（不计入）。部署场景直接对应：等 known_hosts 人工确认、等 CI、等审批门放行，**这些时间不能进 agent 的成本账**。
`https://docs.devin.ai/admin/billing/usage`

> ⚠ 但有个值得警惕的反例：Devin 的口径写得这么清楚，HN 上仍有人说 ACU 过于不透明——因为**口径写在文档里，不等于成本显示在 run 旁边**。Devin 的 `GET /v1/sessions` 返回的 SessionSummary 里根本没有 acu 或 cost 字段。**教训：成本必须显示在 run 记录上，不是文档里。**

**★ LiteLLM** `[B·integrate]` ← **最大的一顿免费午餐**
花费台账里 `agent_id` 和 `session_id` 是**真实建表列**（`session_id` 还有索引），成本按 agent 运行归集而非按用户归集。另有 `max_iterations` + `max_budget_per_session` 硬闸，可强制每次调用必须带 trace id。
→ **落地**：**不要自建 run 成本表**。`run_id` 写进 `x-litellm-trace-id`（落到 `session_id` 列），agent 身份写进 `agent_id`，仓库名/部署目标写进 `request_tags`。**OQ-2「核实真实花费」就是对这张表按 `session_id` 做一次聚合查询，不需要新建任何数据管道。** `require_trace_id_on_calls_by_agent=true` 让任何不声明归属的调用一律 429。
`https://github.com/BerriAI/litellm/blob/main/schema.prisma` · `https://docs.litellm.ai/docs/a2a_iteration_budgets`

**★ gh-aw** `[B·copy]` — AI Credits 美元化（1 AIC = $0.01），两级硬顶：单次 `max-ai-credits` 与 24 小时 `max-daily-ai-credits`；**超日预算时激活阶段直接跳过 agent job**，不是让它跑完再算账。
→ **落地**：预算检查发生在 activation 阶段、**在启动 agent 之前**；触顶时产生一条显式的「被预算拦截」终态记录，进健康告警。
`https://github.github.com/gh-aw/reference/cost-management/`

**★ Claude Managed Agents** `[B·copy]` — `budget.max_list_cost` 是创建时的一等参数，用**整数美分字符串**表达以杜绝浮点舍入；触顶时会话不是终止而是**转 idle** 并给出专属 `stop_reason: budget_reached`，且只接受结算类事件、拒绝任何会开新工的事件。
→ **落地**：budget 用 integer cents，禁止小数；触顶写 `stopReason='budget_reached'` 而不是标 failed；触顶后进 paused 态，只接受 approve/deny/tool_result/stop 四类事件。抄"预算只能在创建时挂、之后可改可删但**不可重新添加**"的单向约束——防止运维事后补一个假上限掩盖问题。
`https://platform.claude.com/docs/en/managed-agents/budgets`

**★ GitLab CI** `[B·copy]` — 配额不是开关而是**三档预警 + 一段宽限**：剩余 25% / 5% / 归零分别通知；配额用尽后 runner 停止接新 job、pending 与 retry 的 job 直接 drop，但**已在运行的 job 允许跑到整个 namespace 超额 1000 分钟之后才被杀**。
→ **落地**：成本治理**不要做成"到顶即拒"**。给每个已 admit 的 run 一个可配置的 overrun 预算，保证部署类 run 绝不会因成本闸门中途被杀。保留"自托管 runner 不计配额"的逃生门：EC2 上的自有 agent 与租用算力分账，账单顶穿只影响后者。**这条直接对应主干 CI 连挂三次。**
`https://docs.gitlab.com/ci/pipelines/instance_runner_compute_minutes/`

**★ AWS Strands** `[B·copy]` — 预算耗尽是**具名的、优雅的终止状态**（`limit_turns` / `limit_total_tokens` / `limit_output_tokens`），不是错误、不是静默卡死，且终止后消息历史仍有效、**可加预算原地续跑**。
→ **落地**：终止原因做成受控枚举而非布尔 success，至少 `completed` / `limit_cost` / `limit_duration` / `limit_steps` / `cancelled` / `failed` 六值，`limit_*` 一族必须与 `failed` 在类型上分开。部署 agent 触到 `limit_cost` 时，工单里写"预算耗尽于第 N 步，已用 $X / 上限 $Y，可提额续跑"，而不是一条红色的 CI 失败。
`https://strandsagents.com/docs/user-guide/concepts/agents/agent-loop/index.md`

**其余**：Dagger（`LLMTokenUsage` 是 API schema 里的一等类型，五字段区分缓存读/写/未缓存输入/输出/总计）、Trigger.dev（`costInCents` 是 run 记录字段，`usage.getCurrent()` 读实时花费，等待不计 compute）、Vercel Sandbox（Active CPU 计费，等 I/O 不计）、Temporal（**`avoid`** — 止步 Namespace 级，无 per-run 成本字段，无按成本硬停）。

**→ 对章程**：L4 内核不要指望 Temporal 管钱。成本字段与 AgentRun **同事务写入**——这正是"30 天成本静默少报"的修复方向。差异化明确：**Orrery 要有 Temporal 没有的"触顶即硬停且必须响"**。

---

#### D7 审批门 / 人在环中 — 18 张卡 / 12 个产品（卡最多的一维）

> **FeedMob 今天**：PR 评审就是审批门；frontmatter 三字段发布门。风险记录："auto/code 模式等于给任何能开 issue 的人一个 shell"。
> **GitHub Actions 现任**：Environments required reviewers + wait timer，但**只在部署环境级**，不是通用的流程内审批步。

**共同的正确决定**：Airflow、Trigger.dev、Inngest 三家都做了同一个决定——**等待期间不占用执行资源**。Inngest 明说并发限制按 step 不按 run，等待中的 run 不计入；Trigger.dev 明说 checkpoint 后释放并发槽且不计 compute 成本。差别在超时后的默认行为：**只有 Airflow 强制你声明 defaults**，另外两家默认只给一个"没等到"的信号。

**★ Atlantis** `[B·copy]` — 把"什么时候才允许 apply"收敛成三个可组合的前置条件：`approved`（至少一个**非作者本人**的人批准）、`mergeable`（PR 在平台侧确实可合并：分支保护、状态检查、无冲突）、`undiverged`（自最近一次 plan 以来没有分叉）。
→ **落地**：Orrery 的部署门用同一组合。`undiverged` 尤其重要——它防止"批的是旧计划、应用的是新代码"。

**★ Spacelift** `[B·copy]` — approval policy 用 Rego 写，输入里同时有本次 run 的 `creator_session` 和当前所有 `reviews`；官方给出的"禁止自我审批"就是比较两者的身份。run 状态机里有显式的 `Unconfirmed` 状态——"计划成功了，但有变更，等人看"。
→ **落地**：`Unconfirmed` 这个状态名值得抄——它把"成功"与"可以继续"分开了。

**★ Windmill** `[B·copy]` — 审批实现为流程步骤的 `suspend` 属性：步骤挂起后靠 `required_events` 计数的**密钥 resume URL** 恢复，timeout 到点自动取消，approver 可被要求是登录用户、**禁止自批**、限定组。核验员额外确认：禁止自批是"在审批页与 flow resume API 端点**双侧强制**"——API 层强制确凿。
→ **落地**：禁止自批必须在 API 层强制，不能只在 UI 上灰掉按钮。

**★ Buildkite** `[B·copy]` — block step 不是布尔审批，而是**带类型的输入表单**：`fields` 支持 text（可用 `format` 正则校验）与 select（≤6 项渲染为单选钮）。
→ **落地**：部署审批不只是"批/不批"，而是"批准把 `<sha>` 部署到 `<从已知目标里选>`"——审批动作本身携带参数，接 D16 的"永远不给自由文本框"。

**★ claude-code-action** `[B·integrate]` — 审批门的触发条件是**"这次变更里有 agent 的手笔"**，而不是"这是哪条分支/哪个目录"；批准**绑定内容哈希，一 push 就作废**；出错一律 fail-closed。
→ **落地**：这是三家厂商 action 里唯一存在"**因为作者是 agent，所以规则不一样**"这个概念的。直接抄。

**★ Argo CD** `[B·copy]` — 发布冻结窗做成 AppProject 上的**声明式对象**：`kind=allow/deny` + cron schedule + duration + timeZone + 选择器。
→ **落地**：冻结窗（如"周五下午不部署"）是声明式资产，不是人的记忆。

**其余**：gh-aw staged 模式（跑完全程但不落地，渲染到 step summary 供人预览，**可按输出类型分别 staged**）、gh-aw 复用 Actions deployment environment 作为真正有强制力的门、Temporal `wait_condition`（等待期间完全不占 worker）、Hatchet 带**回看窗口**的等待（消除"事件比等待先到"的竞态）、Managed Agents `requires_action` 一等会话状态、Airflow 3.1 HITL operators、Dify Human Input 节点（超时默认 3 天、走专门的 timeout 分支）、Port `WAITING_FOR_APPROVAL`、CodeRabbit 三档门禁（off/warning/error）、Activepieces 把审批拆成"生成链接"与"等待"两个独立动作。

---

#### D8 提示注入与不可信输入姿态 — 9 张卡 / 7 个产品

> **FeedMob 今天**：指南规则 #4"issue 正文是不可信输入"；`surface` 判别符；工具从黑名单改为**空白名单**（黑名单留着 Read/WebFetch 活口能读走环境变量）。
> **GitHub Actions 现任**：传统 workflow **本身就是注入面**——`${{ github.event.issue.title }}` 直接内插进 `run:`。**FeedMob 这块做对了**：36 处 `secrets.*` 全走 `env:`，无 `pull_request_target`。

**★ gh-aw safe outputs** `[B·copy]` ← **本维度最重要的一张卡**
agent 永远拿只读 token（`permissions: contents: read, actions: read`），任何写操作**不由 agent 执行**，而是吐出结构化 JSON（`{"type":"create_issue",...}`）经 `safeoutputs` MCP server，由下游一个按操作类型授最小权限的 job 验证并落地。约 50 种输出类型，每种带 `max:` 上限（默认极小：`create-issue` max 1、`add-labels` max 3）。另有 `allowed:`/`blocked:` label glob、`target: triggering|"*"`、`allowed-repos`、`max-patch-size: "50kb"`、`protected-files`、`allowed-domains`（非白名单 URL 替换为 "(redacted)"）。
→ **落地**：**部署域直接照抄这个两段式**。agent job 只读（读仓库、读 CI 状态、读健康检查），**不持有任何能 ssh 到 EC2 的凭据**；只能产出 `{"type":"deploy","target":"prod-web-1","ref":"<sha>"}`；由独立的 deploy-executor job 持有 SSH key 执行。把 safe outputs 清单定义成部署域的动词：`deploy` / `rollback` / `restart-service` / `run-migration` / `scale`，每个带 max 与 target allowlist。**681 行 deploy.yml 里真正危险的恰好只有这几个动词，其余 600 多行是可以被编译掉的。**
`https://github.github.com/gh-aw/reference/safe-outputs/`

**★ gh-aw integrity filter（DIFC）** `[B·copy]` — 不可信内容不靠提示词隔离，而是在 **MCP gateway 层按作者可信度做信息流控制**：五级信任格 `merged > approved > unapproved > none > blocked`，低于 `min-integrity` 的条目**在引擎看到之前**就被删掉，并记 `DIFC_FILTERED` 审计事件。配套 `approval-labels`——**一个有写权限的人打标签**才把内容提升到可信级别。
→ **落地**：等价 DIFC 层落在"Mobius 工单内容进入 agent 上下文"那一跳。被过滤的条目写进运行台账——这样"**agent 没看见那条指令**"本身也是可查证的事实。
`https://github.github.com/gh-aw/reference/integrity/`

**★ Claude Code Routines** `[B·copy]` — 对"预存的提示词"和"触发时带进来的文本"做**结构性分级**：保存好的 prompt 当作已授权任务直接执行，而 API 触发时传入的 text 被包进 `<routine-fire-payload>` 块并**显式标注为不可信数据**，除非 routine 自己的 prompt 明确引用它，否则只是惰性上下文。
→ **落地**：job 定义与运行时输入分两个信任等级。章程里写死一条：**【触发不等于审批】**——无论触发来自 cron、webhook 还是持 token 的外部系统，都不构成对运行中任何一个具体动作的同意，同意只能来自 D7 的审批门。这与"委派即触发"不冲突：委派是人对"开始这项工作"的授权，不是对"这项工作里每个动作"的授权。

**其余**：Buildkite（放弃"限制谁能生成步骤"，改成"**生成的步骤必须验签才能跑**"，默认 block 未签名 job——**唯一在承认"执行内容会在运行时动态产生"的前提下仍给出密码学保证的**，而这正是 agent 场景的形状）、Woodpecker（作用域切成"事件类型 × 消费镜像"两维，冲突取最小权限）、codex-action（调用方传进来的额外参数**不允许降权**，action 主动拒绝）。

---

#### D12 运行台账与决策可观测 — 12 张卡 / 10 个产品

> **FeedMob 今天**：HTML 注释尾标 + `AgentRun` 表**同事务**写入，评论仍是真源——**任何发出尾标的运行时都自动可观测，平台不需要知道它存在**。
> **GitHub Actions 现任**：只有日志，没有决策台账。

**★ Tekton Results** `[A·copy]` ← **本维度唯一 A 级**
在存储层把"运行执行"和"运行台账"拆成**两个系统**：gRPC 可查询 API + 持久化存储 + retention agent；运行一旦成功归档，**原始执行对象即可安全删除**。动机明写是给 etcd 腾地方（TEP-0021）。Argo 的 workflow archive 落 Postgres 是同一模式。
→ **落地**：执行状态与历史台账**必须拆开**——三家都是被规模逼着拆的，Orrery 不要等到被逼。

**★ gh-aw 尾标** `[B·copy]` — 每条 agent 产出的 issue/评论/PR 尾部挂同一个尾标：人能读的一行（运行链接 · 花了多少 AIC · 多少 token · 一个能搜出同工作流所有产物的链接），加上机器能搜的 HTML 注释标记 `<!-- gh-aw-workflow-id: … -->`。
→ **落地**：**与 Mobius 的尾标设计是同一个发明**，互相印证。要补的是 gh-aw 有而 Mobius 没有的：尾标里带一个**能搜出同工作流所有产物的链接**。

**★ Windmill** `[B·copy]` — 运行台账是**一张表一行**：`completed_job` 同时带输入(args)、输出(result)、日志(logs)、流程状态(flow_status)、资源消耗(duration_ms/mem_peak)、代码指纹(script_hash)。
**★ Kestra** `[B·copy]` — 每次 execution 自动打上 `system.*` 标签：`system.username`（谁触发）、`system.from`（怎么触发）、`system.correlationId`（跨 subflow 传播）。
**★ Langfuse** `[B·copy]` — **刻意的数据冗余**：只存一张 observations 表，每行带一份 trace 级属性的拷贝，因此任何切片查询都不需要 join。
**★ LangSmith** `[B·fork]` — 成本是 run 的一等字段而非事后统计：`usage_metadata` 携带分类 token 明细，匹配可自定义的 model price map。
**★ Port** `[B·copy]` — 人点的 action 和事件触发的 automation **共用同一个后端和同一个 "Action run" 对象**，trigger 只是 action 定义上的一个字段。
→ **落地**：Orrery 的 human-triggered run 与 agent run **同表**，`trigger_kind` 只是一列。

**★ gitea/runner** `[B·copy]` — 日志与 job outputs 都是"**按索引增量提交 + 服务端回 ack 索引**"的协议，客户端据 ack 裁剪缓冲区；**投递成功由服务端的 ack 定义，不是由 RPC 返回 200 定义**。
→ **落地**：这与 Mobius 事件流的 cursor ack（"两个勾"）是同一设计。日志流直接抄这个线协议。

**其余**：gh-aw `gh aw audit <run-id> --json`（吐出 metrics、firewall_analysis 每个域名的放行拒绝）、Temporal Event History（同一份历史：人在 UI 看、CI 拿去做确定性回归测试）、Managed Agents（**不另设日志系统**——session 的持久化事件流就是运行台账）、claude-code-action（默认日志不是"agent 说了什么"，而是固定字段的**结果信封**：耗时、轮次、总成本、**权限被拒次数**、模型上下文上限）。

---

#### D13 沙箱、隔离与爆炸半径 — 9 张卡 / 9 个产品

> **本组最重要的发现**：六家沙箱厂商**独立收敛到同一个结论——隔离的真正边界不在内核，在出口代理。**
>
> 所有人都在宣传 Firecracker / gVisor 这种内核级隔离，但真正投入工程的地方是**出站流量**。原因很清楚：microVM 挡的是「逃逸」，而 agent 的真实风险不是逃逸，是**外泄**——提示注入让 agent 把它本来有权读的东西发到不该去的地方，**内核隔离对此完全无效**。

**★ Vercel Sandbox** `[B·copy]` — 做得最好，不是因为功能多，而是**它的文档是唯一一份把自己的失效边界写清楚的**：SNI 匹配挡不住 domain fronting、`subnets.allow` 会把 DNS 完全放开（可用于 DNS 隧道外泄）、空策略等价于 deny-all、matcher 永不阻断只选择处置。另有网络策略**运行中热更新**：开局 allow-all 装依赖，然后 `sandbox.update({ networkPolicy: 'deny-all' })`，再跑不可信代码。
→ **落地**：这份"把绕过面写进文档"的纪律本身比任何功能都值得偷。Orrery 的隔离文档必须有一节叫"**本策略挡不住什么**"。

**★ Gitea ephemeral runner** `[B·copy]` — 凭证在"**任务被分配的那一刻**"就作废——吊销发生在不可信代码开始跑**之前**，而不是跑完之后。
**★ Kamal barrier** `[B·copy]` — 多角色部署有一道 barrier：primary role 是守门人，其他角色排队；**只有第一个 primary 容器健康才放行**，若它起不来就 close barrier，其他角色一个都不启动。
→ **落地**：这是部署域的爆炸半径控制。FeedMob 9/2 那次三个 release job 齐倒，正是缺这道 barrier。

**★ Cloudflare Sandboxes** `[B·fork]` — 要在沙箱外终止 TLS 做策略和凭证注入，就必须给沙箱塞一张 CA。做法是**每个沙箱实例生成一张一次性 CA 和私钥**，CA 放进沙箱并默认受信，私钥留在 sidecar、**从不跨实例共享**。

**其余**：gh-aw AWF（rootless Docker + squid 出口代理 + **Docker socket 对 agent 隐藏**）、Windmill 两档进程隔离可按脚本粒度开（`ENABLE_UNSHARE_PID` 挡住 `/proc/$pid/mem` 和 worker 环境变量）、Bedrock AgentCore（每 session 独占 microVM，结束后销毁且内存 sanitize）、Managed Agents self-hosted sandbox（**编排与执行切开**：模型与状态机留在 Anthropic，文件系统/进程/出口留在你的机器）。

**⚠ 商品化警报**：E2B 官方定价 vCPU $0.000014/秒、RAM $0.0000045/GiB-秒 → $0.0504/vCPU-小时、$0.0162/GiB-小时。**Daytona 官方定价页标的正是 $0.0504/h 和 $0.0162/h**。两家独立公司的公开定价**精确重合到小数点后四位**——这不是巧合，是没有定价权的表现。**Earthly 教训的前兆齐了，而且比 CI runner 那次更明显。Orrery 不要自建沙箱层。**

---

### 2.5 · 文档说的和实际不符：20 条被推翻的声称 ⚠

口碑挖掘找独立用户来源时，**20 条文档级声称被用户证据反驳**。这些是整轮研究最该先读的部分——**有几条正好落在我原本准备抄的设计上**。

#### 直接影响我们决策的六条

**① gh-aw 的 AI Credits 定价会间歇性整个失败** `D5`
零配置默认路径（不写 `model` 即解析为 `copilot/auto`）在 AWF 代理里**根本没有定价条目**，每次推理请求被 HTTP 400 拒绝（`missing_model_pricing`）。而且是间歇发作——付费用户 heiskr 报告同一天同组织里两个同样零配置的工作流，**一个反复失败一个成功**，取决于别名解析落到 `copilot/auto` 还是 `large`。官方给的三个 workaround 他直指是"让用户替一个无法定价的默认值背锅"，其中 `models.default-ai-credits-pricing` 相当于"给一个身份未知的模型编一个费率，会让 AIC 账目变成虚构"。
另有 gh-aw 自己的 collaborator 报告 AIC 计算器对 Anthropic **少算**（错误地从 `input_tokens` 里减掉 `cache_read`）。
> **含义**：§0.1 里"AI Credits 美元化"那条**只有两级硬顶的形状值得抄，定价实现不值得抄**。

**② LiteLLM 的 `max_budget_per_session` 不是硬闸** `D5` ← 最要紧
`async_pre_call_hook` **只读取已累计的 spend 做比较，准入时不做任何预留**，而实际成本只在请求成功之后才累加。因此**同一 session 的两个并发请求会读到同一个"未超限"的值，双双放行**。（issue #34732，2026-07-26 提，至今 open）
另一个实用细节：`agent_id` **没有索引**（只有 `session_id` 有）。
> **含义**：LiteLLM 的**台账可以直接用**（`LiteLLM_DailyAgentSpend` 确实是为 agent 场景专门设计的），但**熔断必须 Orrery 自己做**，不能依赖它。这恰好印证了 §4.3 的判断——差异化就在"触顶即硬停且必须响"。

**③ gh-aw 自动生成的 concurrency group 对扇出型工作流"基本上永远是错的"** `D14`
顶层组和 agent 组都按 `inputs.target_repo` 正确分片，**唯独 conclusion job 用的是静态的 `gh-aw-conclusion-<workflow-id>` 组**——于是 50 个并发运行全部排在同一个槽位上一个一个漏下来。报告者原话：对 `workflow_dispatch` 扇出型工作流，这个默认组基本上永远是错的。

**④ Argo CD 的 rollback 在默认 GitOps 配置下不可用** `D19`
开着 auto-sync 时，要回滚必须**先关掉 auto-sync**；一旦重新打开，Argo 会高高兴兴地再同步回那个刚被回滚掉的 commit。issue #9570：**115 reactions、2022-06 开、至今 open、至少六位独立作者、跨四年未解**。
> **含义**：§2 D19 里"Argo CD 一键回滚"那条要降级理解。Kamal 那几条（`kamal app version`、`stale_containers`、`ssh.proxy`）没有被推翻。

**⑤ Gitea ephemeral runner 的凭证吊销时机与文档相反** `D13`
文档写"任务一旦被分配，凭证即被吊销"；实际行为是 **PR #34447 让 ephemeral runner 在任务完成的那一刻才被标记为已删除**——而且正因为吊销发生在跑完之后、有时早于 runner 把日志发完，**导致日志丢失**。
> **含义**：§2 D13 那张卡的"吊销发生在不可信代码开始跑之前"是错的。Orrery 若要这个语义，得自己实现。

**⑥ Atlantis 的自定义策略检查任何人都能批准** `D20`
开启 `custom_policy_check: true` 后，所有自定义策略检查被默认塞进一个**未定义的、名为 "Custom" 的 policy set，而那个 set 没有任何审批人限制**——结果是任何人都能批准策略失败。**维护者 2025-02-14 亲口确认了根因。**

#### 其余十四条（摘要）

| 产品 | 被推翻的部分 |
|---|---|
| **Windmill** `D9` | 日志脱敏是**路线图不是现状**——创始人自己在 #5450 里把它描述成"还要做的事"；一位 EE 付费用户在 #8268 看到 SSO 的 client ID 和 secret **明文**写进 native worker 的 service log |
| **Windmill** `D10` | `cache_ttl` 放在 **flow step 上是静默 no-op**，只有写在 `script.yaml` 里才生效；同报告里 `concurrent_limit` 两级**都完全不限流** |
| **Windmill** `D13` | NSJAIL 隔离**不是进程边界的普遍属性，是按执行器逐个打的补丁**——安全公告 GHSA-2jfj-x8j7-7mfh（2026-09-10，High）承认即便开了 nsjail，DuckDB 脚本仍在 worker 进程内经 FFI 执行，可污染跨工作区共享的依赖缓存 |
| **Kestra** `D14` | "引擎层保证同一 schedule 不重叠"不成立——`recoverMissedSchedules: LAST` + `concurrency: 1` 时，**同一 trigger 时间起了 3 个 execution，其中 2 个并行跑完**（附 4 张截图，仍开放）。另：UI 上禁用再启用 schedule 会把漏跑的全部补跑，无视 NONE/LAST 设置 |
| **Activepieces** `D7` | `Wait for Approval` **只有紧跟在 `Create Approval Links` 后面才生效**；中间插一个"发 Slack 消息（带批准链接）"，流程就一路跑完**跳过审批**。报告者原话：那个能正常暂停的版本"功能上毫无用处，因为我没法把这些链接放到任何地方" |
| **Hatchet** `D7` | `event scope` 隔离是坏的：两个 durable waiter 用同一 event key 但不同 scope 时，**scope-a 的事件可以满足 scope-b 的 waiter**，把 A 场景的 payload 投给 B 的等待者（报告者自标 Severity: Critical） |
| **Dagster** `D10` | 全局资产页**不把分区已经不同步的资产标成 unsynced**（#22553，29👍，2024-06 开，至今 open，多人连续独立复现） |
| **Airflow 3** `D11` | 勾了"Run with latest bundle version"、audit log 确认勾了，**Airflow 仍然用旧代码启动任务** |
| **Dagger** `D9` | 三条 secret 防护线不等强度：「排除出缓存键」被实证到反噬（secret 名不变则后续层一直命中缓存，token 过期后 `git clone` 一直失败） |
| **GitLab** `D11` | 组件版本解析的"硬性前置条件"被**两个相反方向**证伪 |
| **OpenHands** `D14` | 被引用的那一个文件确实是范本，但**不能推广到仓库里其他工作流** |
| **Langfuse** `D12` | "只存一张 observations 表"不成立——文档自己用了"**概念上**"这个限定词，而声称把它读成了物理实现 |
| **LiteLLM** `D4` | 告警清单与"记账失败"独立告警属实（这部分够 A 级），被推翻的是别的半句 |
| **gh-aw** 其他 | 见上方 ① ③ |

#### 一条方法论

这 20 条里，**没有一条是靠读文档能发现的**——全部来自 GitHub issue（按 reactions 排序）、安全公告、HN 评论、论坛复现报告。

> **对 Orrery 的直接含义**：抄任何一个机制之前，先去它的 issue 区按 reactions 排序翻前 30 条。文档写的是设计意图，issue 区写的是它实际怎么坏的。

---

### Tier B · 平台质量维度

#### D14 触发器与调度器健壮性 — 15 张卡

**三家形状完全相同的发现**：Tekton 在集群 ConfigMap 里放 `default-timeout-minutes: "60"`，Argo 在 controller configmap 里放 `workflowDefaults`（可压 `activeDeadlineSeconds` / `ttlStrategy` / `podGC`，且明确"workflow 自己写了就以自己的为准"），Dagger 的 GHA 模块用 `JobDefaults` + `setDefault(&j.TimeoutMinutes, ...)` 在**生成阶段**注入。

> **默认值属于平台，不属于流水线作者；作者可以覆盖，但不可以遗漏。**
>
> 这正是"12/13 个 workflow 没有超时"的**结构性解释**——GitHub Actions 没有任何组织级默认超时开关，job 默认 360 分钟，于是"忘记写"必然是常态。
>
> **谁解得最好：Tekton**（默认值是集群资源，改一次全集群生效）。**但对 FeedMob 唯一可落地的是 Dagger 的方案**——因为他们还在 GitHub Actions 上，**生成式注入是不换平台就能拿到平台级默认值的唯一路径**。

Temporal Schedules 是重叠策略的金标准（skip / buffer_one / buffer_all / cancel_other / terminate_other / allow_all）；Hatchet 的等待带**回看窗口**消除"事件比等待先到"的竞态。

#### D10 内容哈希幂等 / no-op — 9 张卡

三种范式并存：**Inngest** 是时间窗去重键（event id 或 CEL 表达式，固定 24 小时窗）；**Prefect** 是**缓存键代数**（DEFAULT / INPUTS / TASK_SOURCE / FLOW_PARAMETERS / NO_CACHE，可用 `+` 组合、可用 `INPUTS - 'debug'` **减掉某个参数**）；**Dagster** 是**版本图传播**（`data_version = hash(code_version + 上游 data_versions)`，沿图自动扩散）。

**Dagster 最强**，因为它是唯一让"上游变了"自动传播到下游的。但它也没闭环：**自动跳过至今不是内置规则**（issue #16872 从 2023-09 开到现在），用户只能在 op 内部自己比对然后空跑。
→ **落地**：**Prefect 的组合代数值得和 Dagster 的传播模型合起来抄**——Orrery 既要传播，也要能声明"这个字段不参与哈希"。这比 FeedMob 现有的"cron 写内容哈希状态文件"前进了一代。

#### D11 只增不改的契约与版本 — 10 张卡

**★ GitLab CI 的关键区分**：把**可选的复用**（components + catalog，消费方自愿引用、自选 SHA 或 `~latest`）与**不可选的强制**（pipeline execution policies，供给方注入、默认忽略 `[skip ci]`、例外按 user ID 记名）拆成**两套独立机制**。
→ **落地**：**FeedMob 的痛点同时包含"重复"（复用问题）和"12/13 无超时"（强制问题）——用同一套机制解不了。** 这是本次研究对 charter 最实用的一条结构性建议。

**★ Gitea scoped workflows**（PR #38154, v1.27.0）— 一份 workflow 写在"源仓库"里，自动在组织级或实例级**每个仓库**上运行，执行时用消费仓自己的 runner、secrets 和分支，内容按源仓 commit SHA 钉住；标为 `required` 的消费仓**不能关、不能绕**，直接接进分支保护卡 PR 合并。**别家（含 GitHub 自己）都没有等价物。**
→ **落地**：这是"deploy.yml 在 8 个仓库各写一遍"的完整答案。

**★ Trigger.dev** — 部署拆成三个可独立操作的原语：build、生成**不可变版本号**（`20240313.1` = 日期 + 当日序号）、promote 成 current。run 在启动时**被锁定到当时的 current 版本**，重试和子任务都跟着走。

#### D19 部署原语与环境模型 — 8 张卡（当前主场景）

> **FeedMob 今天**：`deploy.yml` 681 行手写；9/2 两次主干失败都倒在第 6 步 `Add server to known_hosts`；无回滚原语、无"什么版本在哪"视图。

**Kamal 的形态与 FeedMob 最接近**（Docker + SSH 到自有服务器，不是 K8s），四条直接对症：

| 痛点 | Kamal 的答案 |
|---|---|
| 跳板机、主机指纹反复试错 | `ssh.proxy: user@host` 是**一等配置**（映射到 `Net::SSH::Proxy::Jump`），私钥可来自密钥管理器，默认开 keepalive |
| "什么版本跑在哪台机器" | `kamal app version` 是**原生命令**（逐主机打印） |
| 无漂移检测 | `kamal app stale_containers` = 每台主机上所有 app 容器版本 **减去** 当前运行版本 |
| 切流量不确定 | `kamal-proxy deploy <service> --target host:port` 是**阻塞式、健康检查门控、等旧实例排空后才返回**的命令，失败返回非零退出码 |
| 多环境 | `-d <destination>`：`deploy.yml` 与 `deploy.<destination>.yml` 做 deep_merge，密钥按 `.kamal/secrets-common` → `.kamal/secrets.<dest>` 分层 |

**Argo CD 的漂移三开关**值得抄语义（虽然 FeedMob 不用 K8s）：`prune`（Git 里删了就删）、`selfHeal`（集群被手改就改回去）、`allowEmpty`（防止空清单把资源删光）——**三个互不替代**。回滚是 `argocd app rollback APPNAME [ID]`，维护一条编号的部署历史。

#### D9 密钥处理与输出脱敏 — 13 张卡 · D6 投递≠成功 — 6 张卡 · D4 存活与健康 — 6 张卡

D9 上 **GitHub 做得好**（secrets + `::add-mask::` + OIDC + `permissions:`），是对等目标不是差异化点。值得抄的是 Forgejo 的 **cache proxy**（工作流拿到的是一次性 `ACTIONS_CACHE_URL` 指向 runner 本地代理，代理再用共享密钥连真正的 cache server）与 systemd credential 注入（`file:$CREDENTIALS_DIRECTORY/xxx.txt`，与 inline 形式互斥）。

D6 的共同规律：**Inngest / Trigger.dev 的 step-level retry + 死信**是正解；FeedMob 现有的 `last_status` vs `last_delivery_error` 分离已经走在正确方向上。

D4 的基线来自 GitHub 自己：`renewjob` 每 60s 把 `lockedUntil` 推后 10 分钟——**runner 一死锁到期，服务端回收 job**。这是租约模式，值得直接抄。

---

### Tier C · 采纳与生态

#### D16 非工程师编写与自助 — 3 张卡，但结论极硬

Backstage / Port / Rundeck **三家收敛到同一个结构**：受约束的表单 → 服务端定义的作业 → 一个 run 对象。

- Backstage：`spec.parameters` 是 JSON Schema，`ui:field` 从 software catalog 取合法值（OwnerPicker / EntityPicker / RepoUrlPicker 的 `allowedHosts`/`allowedOwners`/`allowedRepos`）
- Port：`trigger.userInputs` 是 JSON Schema，选择器指向 blueprint 实体
- Rundeck：job option 的 allowed values 来自远程 URL JSON，加 "Enforced from Allowed Values"

> **共同的核心设计决策是同一条：永远不给自由文本框，合法值来自系统自己知道的真实清单。**
>
> 这条对 FeedMob **直接对症**——`known_hosts` 反复试错、`deploy.yml` 在 8 个仓库各抄一遍，根子都是"部署参数靠人手敲、靠复制粘贴"。**Orrery 的部署表单如果做成"从 Orrery 已知的服务器/环境/仓库里选"，这两个痛点在提交前就消失了。**

一个值得注意的空白：Backstage 的 scaffolder **没有审批门**。

#### D15 本地层吸收 · D17 技能打包 · D18 多运行时 · D20 自进化

D17 的"技能包"有一个必须记住的反面：**OpenClaw 的 ClawHavoc——2,857 个技能里 341 个恶意（12%）**。结论写进墓碑："不能有技能市场"。

D18 只有 2 张卡（gh-aw 四引擎、Managed Agents），是覆盖最薄的一维——**这本身是发现**：跨引擎统一运行契约这件事，外部几乎没人做。而 Mobius 的 HTML 尾标已经是引擎无关的了。

D20 的三张卡都指向同一条：**agent 只能经 PR/评审改自动化**。gh-aw 的做法是 workflow 被编译并提交进仓库，agent 通过 safe outputs 开 PR。

---

## 3 · 墓碑墙

14 块碑。每一块都回答同一个问题：**Orrery 不能做什么。**

| 产品 | 死于 | 死因 | 等级 |
|---|---|---|---|
| Earthly CI（托管产品） | 2023-10-01 | 迁移成本墙 | B |
| Earthly Cloud / Satellites | 2025-07-16 | 商品化层无法变现 | B |
| BuildJet for GitHub Actions | 2026-03-31 | 商品化层无法变现 | B |
| **Airplane.dev** | 2024-03-01 | 人才收购 | **A** |
| OpenAI Agent Builder | 2026-11-30 | 可视化构建器败给代码 | B |
| **OpenClaw** | 未死，2026-01/02 安全危机 | 安全危机 | **A** |
| Pipedream | 2025-11-19 被 Workday 收购 | 被套件吸收 | B |
| Drone CI | 2020-08-05 被 Harness 收购 | 被套件吸收 | B |
| Codefresh | 2024-02-27 被 Octopus 收购 | 被套件吸收 | B |
| StackStorm | 未关停，维持状态 | 品类塌陷 | B |
| Hubot | 未关停，底座被抽走 | 品类塌陷 | B |
| Sweep | 2025-03 转向 JetBrains 插件 | 退回人在环内 | B |
| Mentat | 2025 归档 | 被官方 CLI 吃掉 | B |
| **Travis CI** | 2019 收购 → 2021 泄露 | 经典衰落曲线 | **A** |

### 3.1 最刺眼的三块

**Earthly CI（2023）— 迁移成本墙**
创始人直说销售电话转化不了，客户反复给出的理由是**迁移成本**；对已在用 Satellites 的老客户，Earthly CI 并没有"好到值得换"。他还发现把官网文案从 CI 改成 builds，**转化率翻倍**——市场不接受 CI 这个定位。结论：开发者工具"买得到但卖不出去"。

> **对 Orrery**：不能设计成需要 8 个仓库把 13 个 workflow **整体迁出** GitHub Actions 的替代品。迁移成本墙会直接杀死采纳。Orrery 必须以"**坐在现有 Actions 之上**"为前提，第一天就能在**不改 deploy.yml** 的情况下产生价值——**先做观测与守门，再谈接管执行**。

**Earthly Cloud（2025）— "有人会 fork"不是退出策略**
公告点名两条：一是 "CI compute being viewed as a commodity"；二是**开源版本自己拆了自己的台**——用户在本地就能拿到全部提速收益，不需要买 Satellites。宣布停止主动维护、不再接受 PR，请社区改名换 logo 去 fork。

> 实测数据：`earthly/earthly` **12,046 星、近 12 个月 0 次提交**；社区 fork EarthBuild 只接住了 **175 星**。**"有人会 fork"不是退出策略。**

**Airplane.dev（2024）— 最像 Orrery 的那个**
形态完全一致（Tasks / Runbooks / Schedules / Views）。账上还有数千万美元、客户满意、收入在涨——Airtable 要的是 CEO 去带 AI 项目，**不是产品、技术或客户**。客户拿到不到三个月迁移期。

> **对 Orrery**：Airplane 的死因恰恰是 Orrery **天然免疫**的（自研、无外部股东、无退出压力）——所以不要羡慕它的产品形态，要看 HN 上 158 条评论的共识：**关键内部工具不能押在一个会被收购的闭源 SaaS 上**。反过来的硬约束是：Orrery **不能变成"只有一个人能维护的黑盒"**——公司内部的 bus factor 就是 Orrery 版的 acqui-hire 风险。

### 3.2 明确不做的清单（每条都有一块碑）

| 不做 | 碑 |
|---|---|
| 更快的 runner / 构建缓存 / 更大机器 | BuildJet、Earthly Cloud |
| 自己的构建 DSL 或 CI 引擎 | Earthly、Drone、Codefresh |
| 拖拽式 workflow 画布 | OpenAI Agent Builder |
| 技能 / 插件市场 | OpenClaw ClawHavoc |
| 通用"传感器 / 规则 / 动作"事件自动化元模型 | StackStorm |
| 绑死单一聊天平台的命令入口 | Hubot |
| 第五个自研编码 agent 运行时 | Mentat |
| "丢个工单给 agent 让它自己干完"当默认模式 | Sweep |
| 租用第三方连接器目录 | Pipedream |

**判断标准很简单**（来自 BuildJet 那块碑）：**如果这个功能写在 GitHub 的 roadmap 上是合理的，Orrery 就不该建它。**

FeedMob 的痛点里没有一条是"CI 跑得慢"——全部是治理和确定性问题（静默顶穿的消费上限、12/13 缺超时、681 行 × 8 份的 deploy.yml、known_hosts 反复试错）。**那才是安全区。**

---

## 4 · 章程变更提案

> 以下每一条都指向 §2 或 §3 里的具体证据。**这是提案，不是已决定的变更**——采纳与否等与 Ken 对齐。

### 4.1 L2 执行器：`nektos/act` 的判断需要修正 ⚠ 最高优先级

章程原文是"复用 `nektos/act`（MIT，71.8k★），Gitea 已验证可库化"。实测推翻了这条的两个前提：

| 事实 | 独立复核结果（2026-09-12，`gh api` 现查） |
|---|---|
| **act 已停更** | master 最后提交 **2026-06-01**（只是版本号 bump）；最后一次真实代码改动是 05-13 的依赖升级。release 节奏原本每月 1 号（04-01 / 05-01 / 06-01）**然后断了三个多月**。114 个开放 PR、266 个开放 issue |
| **`gitea/act` 已归档** | `archived=true`，仓库描述原文 *"Merged into act_runner"* |
| **正确的 fork 目标** | `gitea.com/gitea/runner` 的 `act/` 目录（MIT）。该仓库两周内发了五个版本：v3.3.1(08-26)、v3.3.2(08-31)、v3.4.0(09-07)、v3.4.1(09-08)、v3.4.2(09-09) |

> ⚠ 一个陷阱：`nektos/act` 的 `pushed_at` 显示 2026-08-09，看着像还活着——**那是推到非默认分支的**。master 是真停了。

**更重要的是第二条：act 按设计就没有编排层。**

调研员的原话：

> 兼容 GitHub Actions 天然分成两半——**(a) runner 侧**：步骤执行、容器、表达式求值、`uses:` 解析；**(b) 服务端侧**：并发组、超时、取消、审批、保留期、required 检查、重跑上限、任务分配。act 只做了 (a)，而 **(b) 恰好是 FeedMob 全部四个痛点的所在**（超时缺失、消费上限、重复 deploy.yml、部署卡死）。**Orrery 如果把 act 当引擎模板，等于把 (b) 整块丢掉。**

act 的文档自己列明：`concurrency`、`timeout-minutes`、`permissions`、`environment`、取消、annotation、step summary **全部 "ignored"**。

**提案**：
1. L2 的 fork 目标从 `nektos/act` 改为 **`gitea/runner` 的 `act/` 目录**
2. 章程里明确写出 **(a)/(b) 分界**，并声明 **(b) 是 Orrery 的全部价值所在**
3. L3 协议参照 `gitea/runner` 的 `runner.v1`——**5 个 RPC、约 200 行 proto**（`Register` / `Declare` / `FetchTask` / `UpdateTask` / `UpdateLog`）

### 4.2 P2 楔子（OQ-3）：证据指向"治理与确定性层" ⚠ 最高优先级

墓碑分析给出的一句话结论：

> **Orrery 是 GitHub Actions 之上的治理与确定性层，不是它的替代品。它不执行构建，它管住"谁在什么权限下、带什么预算、满足什么前置条件、在什么超时内"去跑既有的 workflow。**

支持这个结论的三条独立证据：

| 证据 | 说明 |
|---|---|
| **墓碑** | "执行是商品"（Earthly、BuildJet）；"独立 CI 的终局要么被套件吃掉、要么被平台原生化挤死"（Drone、Codefresh） |
| **五问** | 55 个产品里只有 8 个 agent-first；durable execution / 工作流自动化 / 部署三个品类的**领头羊全部不是**。治理层没有现成占位者 |
| **迁移成本墙** | Earthly CI 死于客户"不值得换"。Orrery 若要求整体迁出 Actions，会撞同一堵墙 |

**提案**：把 OQ-3 的四个候选收敛为一条，且**重新定义 P2 的形态**——不是"在自研引擎上做一个更好的功能"，而是"**在现有 Actions 之上加一层守门与台账，不接管执行**"。这也让 P1 与 P2 顺序更自然：P1 自建 runner 省钱 → P2 加治理层 → P3 才谈 agent job。

### 4.3 L4 调度核心：build-vs-embed 的答案是"都不是纯的"

| 发现 | 含义 |
|---|---|
| **Temporal 无成本概念** | 计费止步 Namespace 级，`temporal_cloud_v1_billable_action_count` 约 3 分钟延迟，**无 per-run 成本字段、无按成本硬停**。卡片标记 `avoid` |
| **LiteLLM 已解决成本归属** | `agent_id` / `session_id` 是真实建表列，`max_budget_per_session` 是运行时硬闸。FeedMob **已经在用** |
| **Tekton/Argo 被迫拆开执行与台账** | Tekton Results 的动机明写是给 etcd 腾地方。三家都是被规模逼着拆的 |

**提案**：
1. **成本不自建**——`run_id` 绑进 `x-litellm-trace-id`，LiteLLM 的 `LiteLLM_SpendLogs` 就是事实源。**OQ-2 变成一次聚合查询**
2. **执行状态与历史台账从第一天就分开**，别等被规模逼
3. 若嵌入 durable 内核，**Hatchet（MIT、纯 Postgres）比 Temporal 更合适** FeedMob 的规模；但无论选哪个，成本治理都得自己补

### 4.4 D1 身份：不需要发明协议

三家身份厂商（WorkOS / Okta / Entra）已收敛到 **RFC 8693 token exchange**。Mobius 的"分配 vs 委派"正好是这套凭证语义的**上层**。

**提案**：
1. 令牌带 `sub`（agent 自身）+ `act`（本次授权它的人）——补上"委派没有落到令牌上"这半
2. agent 主体加 `sponsor_user_id`（非空）+ 离职自动转交经理
3. 所有 agent 对环境/仓库的授权带 `expires_at`

### 4.5 新增硬约束：可退回性

来自 Airplane 那块碑：

> **Orrery 挂掉时，8 个仓库必须仍能用原生 GitHub Actions 部署。**

这条同时化解两个风险：内部 bus factor（Orrery 版的 acqui-hire 风险），以及 Earthly 的迁移成本墙（因为从来没有"迁出"过）。

**提案**：写进章程作为不可协商的设计约束，并在 P0 出口条件里加一条验收——**关掉 Orrery，13 个 workflow 照跑**。

### 4.6 新增待拍板问题

| | 问题 | 来源 |
|---|---|---|
| **OQ-9** | Orrery 是否明确声明"不执行构建"？这会改变 P0/P1/P2 的全部范围 | §4.2 |
| **OQ-10** | "复用"与"强制"是否用两套机制？（GitLab 的 components vs pipeline execution policies） | §2 D11 |
| **OQ-11** | 部署原语是抄 Kamal 的形态，还是直接**采用** Kamal？（它与 Docker+SSH+EC2 形态完全吻合） | §2 D19 |
| **OQ-12** | 沙箱层自建还是租？**商品化警报已响**（E2B 与 Daytona 定价重合到小数点后四位） | §2 D13 |

---

## 附录 A · 产品索引

完整的 55 个产品 profile（定位 / 许可 / 状态 / 命中维度 / 主要抱怨 / 对 FeedMob 的硬伤）见证据台账 `claims.jsonl`。

## 附录 B · 证据台账

`research/claims.jsonl` — 195 行，每行一条，字段：

```
id · dimension · product · category · claim_zh · mechanism_en · why_it_works_zh
orrery_mapping · evidence_url · evidence_type · evidence_grade · sentiment
recurrence_count · source_platforms · adoptability · charter_tags
verified · verify_note_zh · source · date_checked
```

| 内容 | 行数 |
|---|---|
| 深潜优点卡（含对抗核验结论） | 78 |
| 群扫优点卡 | 80 |
| 反复出现的抱怨 | 23 |
| 墓碑 | 14 |

**零条缺 URL。** 可用 `jq` 直接查询，例如按章程标签取证：

```bash
jq -c 'select(.charter_tags | index("L2"))' research/claims.jsonl
jq -r '.evidence_grade' research/claims.jsonl | sort | uniq -c
```

## 附录 C · 方法的局限

1. **A 级 61 条（31%）**，B 级 105 条。B 级意味着只有厂商文档说它存在，没有可查证的用户印证——不等于假，但选型时不该当成已验证。
2. **口碑挖掘曾因用量限制整批失败一次**（13 个 agent、93 万 token、零产出），重跑后全部完成。这暴露了一个编排缺陷：13 个 agent 共命运，一批全挂等于全废，应该拆成可独立交付的小批。
3. **Forgejo Actions 一组只采到官方文档自陈的限制**，未做社区抽样。
4. **Mentat 的后继（mentat.ai / MentatBot）本环境 DNS 解析失败**，按"未能核实"处理，未下结论。
5. 引用一律转述，单条不超过 15 词并署名。
