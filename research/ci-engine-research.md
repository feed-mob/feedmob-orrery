# 自研 CI 引擎的三条路

前置调研 · AI-2564 · 2026-09-08

实测样本：feed-mob 名下 8 个仓库、默认分支上实际运行的 13 个 workflow（1791 行），用 gh CLI 与 GitHub REST API 现场采集。

---

## 结论先行

**最贵的那个坑，有个免费的解法。**

2026-08-19 `feedmob-parallax` 主干 CI 连挂三次，不是代码问题——GitHub 的原话是账号付款失败或需要提高消费上限，job 三秒就被拒。而 GitHub 原定 2026 年 3 月对私有仓库自建 runner 收 `$0.002/min` 的计划，在 2025 年 12 月因社区抵制**无限期推迟，至今仍未生效**。自建 runner 现在还是免费的。

所以如果自研的动机里有"省钱"这一项，那一项已经有答案了，而且不需要写引擎。

剩下的动机——本地跑不了、缓存调不明白、8 个仓库各写一遍部署逻辑——才是真值得动手的地方。这三件事恰好都不在 GitHub 解决得好的那一层。而 Earthly 在 2025 年 7 月关掉云服务时给的理由值得抄在墙上：**算力是商品，卖不动**。

---

## 实测：现在的状况

调研之前把 8 个仓库在**默认分支上实际运行**的 workflow 全拉了下来（本地副本对不上：`feedmob-parallax` 在 `feedtv-migration` 分支，另外三个仓库有未提交改动）。

### 🔴 主干 CI 被账单卡死，静默三周

`feedmob-parallax` 8/19 连续三次失败，job 存活 3 秒。GitHub 的 annotation 原文：

```
The job was not started because recent account payments have failed
or your spending limit needs to be increased.
```

之后该仓库再没有推送，所以这个红叉一直挂到今天。同期其他 feed-mob 仓库 9/2–9/7 跑得正常，说明额度问题本身已经解开了——但没人回去重跑 parallax。

### 🔴 部署卡在 SSH 主机指纹，三个 job 同时倒

`feedmob-pixel-dashboard` 9/2 两次主干失败，失败步骤都是第 6 步 `Add server to known_hosts`，连带 `release_backend`、`release_frontends`、`release_summary` 三个 job。当时的提交标题正是 "Retry SSH host-key scans during deployment" 和 "Update deployment jump host"——反复试错修它，而唯一的调试手段是推一次看一次。

### 🟠 12 / 13 个 workflow 没有超时上限

只有 `feedmob-pixel-mcp/ci.yml` 设了 `timeout-minutes`。其余 12 个一旦挂住，会一直烧到 GitHub 的默认上限 360 分钟。按 Linux 2 核 `$0.006/min` 算，一个卡死的 job 就是 `$2.16`，而这正是把额度顶穿的那类支出。

### 🟠 7 个没有 concurrency，6 个没有 permissions

没有 `concurrency` 意味着连续推送时旧的 run 不会被取消，白烧分钟数。没有 `permissions` 块意味着 `GITHUB_TOKEN` 拿的是仓库默认权限，在较老的组织设置下是读写。

### 🟠 Node 20 弃用，被平台推着改

9/2 的运行里 GitHub 已经开始告警：`actions/checkout@v4`、`actions/setup-node@v4`、`docker/login-action@v3`、`webfactory/ssh-agent@v0.9.0` 都被强制跑在 Node 24 上。

全量清点：

| action | 出现次数 |
|---|---|
| `actions/checkout@v4` | 27 |
| `actions/setup-node@v4` | 10 |
| `ruby/setup-ruby@v1` | 10 |
| `docker/login-action@v3` | 9 |
| `webfactory/ssh-agent@v0.9.0` | 3 |

全部按可变 tag 引用，**没有一处钉 SHA**。

### 🟠 部署逻辑在 8 个仓库里各写一遍

`feedmob-pixel-dashboard/deploy.yml` 已经 681 行，里面 `docker/login-action` 出现 9 次、`ruby/setup-ruby` 7 次、`webfactory/ssh-agent` 3 次，全是同一套动作的复制。好消息是它的 `ci.yml` 已经在用 `uses: ./.github/workflows/deploy.yml`——可复用 workflow 的模式已经摸出来了，只是没跨仓库推开。

### 🟢 密钥处理干净，没有注入面

扫了全部 36 处 `secrets.*` 引用，都是通过 `env:` 传入，没有直接内插进 `run:` 的 shell 字符串。`github.event.*` 的取值也都是 SHA 且走 env。没有 `pull_request_target`。**这一块不需要改。**

---

> 把这七条压成一句：**挨的痛里，没有一条是"GitHub Actions 的调度不够好"**。是成本、是本地不可调试、是跨仓库复用缺位、是默认不安全。这决定了自研该攻哪一层。

---

## 原理：Runner 是怎么拿到活的

GitHub Actions 的 runner 端是**开源且 MIT 授权**的（`actions/runner`，C#，6,244★）。它由两个可执行文件组成：**Listener** 负责注册、轮询、取活，**Worker** 负责真正执行步骤。

注册时 GitHub 下发一份 JIT 配置，里面有 API 地址、授权地址和一把 RSA 私钥；Listener 用这把私钥签 JWT 换 OAuth token，之后所有请求都用它做 Bearer 认证。

```
Runner Listener          Broker API           Run Service          Worker
      │                       │                     │                 │
      │  RSA 私钥签 JWT → OAuth token                │                 │
      │                       │                     │                 │
      ├── POST /sessions ────>│                     │                 │
      │<──── sessionId ───────┤                     │                 │
      │                       │                     │                 │
      ├── GET /message?sessionId=… ────────────────>│                 │
      │        （服务端挂起最多 50s）                  │                 │
      │<── RunnerJobRequest ──┤                     │                 │
      │    { runner_request_id, run_service_url }   │                 │
      │                       │                     │                 │
      ├── POST /acquirejob ─────────────────────────>│                 │
      │        （须在 2 分钟内）                       │                 │
      │<── 完整 job 指令 + planId（x-plan-id）─────────┤                 │
      │                       │                     │                 │
      ├── 派发 job（进程边界）────────────────────────────────────────>│
      │                       │                     │                 │
      ├── POST /renewjob  每 60s → lockedUntil ≈ +10min ─────────────>│
      │                       │                     │                 │
      │                       │                     │<── 日志 / 状态 ──┤
```

长轮询是整个设计的核心——runner 不需要公网入口，也不需要消息队列客户端，一个挂起 50 秒的 HTTP GET 就够了。续锁那条（每 60 秒把 `lockedUntil` 往后推 10 分钟）是崩溃检测：runner 一死，锁到期，服务端回收 job。

配置里还带 `ServerUrlV2` 和 `UseV2Flow` 两个字段——GitHub 正在把 runner 从早年的 Azure DevOps 端点迁到自家的 Broker API，新端点返回的字段比旧的少。

> **这一层不该照抄**——GitHub 的 V2 端点是私有契约，随时会变（Depot 已经吃过字段消失的亏）。Gitea 的做法更值得学：他们不复刻 GitHub 的 HTTP 契约，而是自己定义了一套 gRPC over HTTP 协议（proto 放在 `actions-proto-def` / `actions-proto-go`），理由很实际——不想让服务端多听一个端口，所以要一个能跑在 HTTP 上的协议。

---

## 基座：可以直接 fork 的东西

以下数字为 2026-09-08 用 GitHub API 现查。

**关键结论**：解析 workflow YAML、跑步骤、处理表达式和上下文这一整层，已经有一个 71,848★ 的 MIT 项目做完了——`nektos/act`。Gitea 当年也没重写，直接 fork 成 `gitea/act` 当库用。

| 项目 | 许可 | 语言 | ★ | 在自研里的角色 |
|---|---|---|---|---|
| `nektos/act` | MIT | Go | 71,848 | workflow 执行器。直接决定"复现"的成本量级 |
| `go-gitea/gitea` | MIT | Go | 57,885 | 完整先例：从零实现兼容层 + 自定义 runner 协议 |
| `harness/harness` | Apache-2.0 | Go | 38,283 | Drone 血统的现代 CI，带 UI 与流水线模型 |
| `argoproj/argo-workflows` | Apache-2.0 | Go | 16,960 | 通用 DAG 引擎，扇出/扇入矩阵最强 |
| `dagger/dagger` | Apache-2.0 | Go | 16,233 | BuildKit + GraphQL，惰性求值 |
| `tektoncd/pipeline` | Apache-2.0 | Go | 9,062 | K8s 原生 CI 原语 + 供应链安全 |
| `woodpecker-ci/woodpecker` | Apache-2.0 | Go | 7,833 | 轻量自托管 CI，代码量小 |
| `actions/runner` | MIT | C# | 6,244 | 官方 runner。读它是为了搞懂协议，不是 fork C# |

### 那"难"在哪

执行器有现成的，协议是几个 HTTP 端点。真正要自己写、而且写不好就没人用的是这几块：

- **调度与状态机**——job 的排队、依赖、矩阵展开、取消传播、重试语义。Gitea 靠 runner label（`label[:schema[:args]]`，如 `label:docker://image`、`label:host`）匹配 `runs-on`，每个 job 起新容器保证隔离
- **日志流**——实时、可回溯、能扛住单 job 几十万行。做糙了就是"日志可见性差"，恰好是 GitHub 被骂最多的点之一
- **缓存与产物**——公认没被解决的一层。有个说法很到位：每个平台都把调度和编排解决得不错，但**没有一个自己解决了共享缓存和分布式计算**
- **密钥与权限**——作用域、审计、OIDC 联邦。36 处 secrets 引用横跨 8 个仓库，这一层做错就是全公司范围的事故

---

## 格局：竞品在哪一层

最有用的一个认知转换：这些工具大多**不在同一层竞争**。问"有什么好的竞品"之前得先定"要攻哪一层"，否则会拿 Dagger 去比 Blacksmith，而它们根本不解决同一个问题。

| 层 | 代表 | 状态 |
|---|---|---|
| **管道定义** | GitHub Actions、GitLab CI、Dagger（代码即管道）、Earthly（DSL） | 拥挤 |
| **编排调度** | Tekton、Argo Workflows、Buildkite、Woodpecker、Harness、Jenkins | 拥挤 |
| **执行与算力** | Blacksmith、Depot、Namespace、RunsOn、Ubicloud、WarpBuild | 正在卷 |
| **构建加速与共享缓存** | Bazel+RBE、Turborepo Remote Cache、BuildKit | **无人独立解决** |

四层各有赢家，但最下面那层是空的。如果"进化版"要有立足点，那一层是唯一还没被占住的地方。

### 执行层：真正在卷的地方

这一代产品全部选择**不重写 GitHub Actions，只替换它的 runner**——保留现有 workflow 文件，换掉底下的机器：

- **Blacksmith**——裸金属 microVM，Docker 类负载约为 GitHub 托管 runner 的两倍速
- **RunsOn**——跑在**自己的 AWS** 上，用 EC2 spot；成本约为 GitHub 托管的一成；唯一提供 GPU runner
- **Ubicloud**——Hetzner 背书，同样约一成成本
- **Depot**——Docker 构建场景的最优解
- **Namespace**——x64 与 arm64 单线程 CPU 性能领先
- **WarpBuild**——Windows / macOS / Linux 一家全包

缓存这一项差距最能说明问题。同一套基准下，保存加恢复的总耗时：Blacksmith、Namespace、RunsOn 一档是 **24–37 秒**；Ubicloud、WarpBuild 与 AWS CodeBuild 一档是 **113–176 秒**。四到五倍差距，而这仅仅是缓存。

### 管道定义层：Earthly 的墓志铭

Dagger 用 GraphQL API 包住 BuildKit，用 TypeScript 之类的语言构造一张容器操作的 DAG，惰性求值，只跑输出真被需要的步骤。Earthly 走 DSL 路线，同样基于 BuildKit。

**Earthly 在 2025 年 7 月关停了云服务**，理由是无法把商品化的算力变现，开源项目留给社区 fork。这是最需要正视的一条数据：技术做成了，商业模式没成。

---

## 经济账：2026 现价

2026 年 1 月 1 日 GitHub 把托管 runner 降价最多 39%，同时新增了 `$0.002/min` 的平台费（托管机型已内含在降后价里）。

| 机型 / 项目 | 2025 | 2026 | 说明 |
|---|---|---|---|
| Linux 2 核 | $0.008 | **$0.006** | 每分钟；降幅 25% |
| Windows 2 核 | $0.016 | $0.010 | 每分钟；降幅 38% |
| macOS | $0.080 | $0.062 | 每分钟 |
| Linux 4 / 8 / 16 核 | — | $0.012 / $0.022 / $0.042 | 每分钟 |
| Linux 32 / 64 核 | — | $0.082 / $0.162 | 每分钟 |
| 平台费 | — | $0.002 | 每分钟；托管机型已内含 |
| **自建 runner（私有仓库）** | **免费** | **免费** | 原定 2026-03-01 起收 $0.002/min，2025-12 因抵制无限期推迟，至今未生效 |
| Team 方案含量 | 3,000 | 3,000 | 分钟/月；$4 每人每月。Free 2,000，Enterprise 50,000 |

**倒数第二行是整份调研里最重要的一个数字。** 把 workflow 挪到自建 runner，GitHub 侧的分钟数账单归零，只付底层机器钱——用 RunsOn 跑在自己 AWS 的 spot 实例上，大约是托管价的一成。2026-08-19 那次额度卡死，用几天工作量就能永久解决，不需要写一行引擎代码。

---

## 决策：三条路

| | 路线 A · 复现 | 路线 B · 适合公司 | 路线 C · 进化版 |
|---|---|---|---|
| **做什么** | Fork `nektos/act` 当执行器，自写最小服务端：长轮询取活、job 状态机、日志回传。跑通一个 hello-world | 不写引擎。自建 runner 解成本；8 仓库部署抽成跨仓库可复用 workflow；补超时/并发/权限三件套并钉 SHA | 要有具体楔子，不能从"做个更好的 GitHub Actions"起步 |
| **成本** | 1–2 周，一个人 | 几天 | 季度级，需要专人 |
| **产出** | 能讲清机制的原型 | 七条实测问题解掉六条 | — |
| **注意** | 别照抄 GitHub V2 私有端点 | 依据：自建 runner 仍免费 | 风险：算力是商品，卖不动 |

### 如果做路线 C，楔子只有这几个可选

- **本地与生产同一个引擎**——`act` 已经证明 workflow 可以在本地跑，而 GitHub 自己做不到。"没有本地执行、edit-commit-push-wait 循环"是它被骂得最狠的一条。9/2 那次改 `known_hosts` 只能靠推一次试一次，就是这个代价的实例
- **内容寻址缓存**——把 `actions/cache` 的 key 猜谜换成 step 级 CAS。现状是"key 难懂、miss 静默、淘汰不透明，调缓存花的时间比省下的多"，而这一层公认无人独立解决
- **部署原语做成一等公民**——known_hosts、跳板机、镜像 tag 不该在每个仓库里重写一遍。681 行的 `deploy.yml` 和 9/2 那次三 job 齐倒，就是这一层缺位的账单
- **默认安全**——超时、权限、并发默认打开，action 引用在解析时钉成 SHA。13 个文件里 12 个没有超时上限——这不是团队疏忽，是平台默认值的必然结果

至于 2026 年最热的 agentic CI，方向是真的，但它是**最容易做出 demo、最难做成产品**的一条。建议放在楔子之后，不要作为立项理由。

### 建议

**路线 B 现在就做**，它把七条实测问题解掉六条，成本是几天而不是几个季度。**路线 A 作为路线 C 的可行性验证**——一两周投入很低，而且能真正搞懂协议，判断楔子成不成立时手里有实据。**路线 C 等楔子明确之后再投人**，并且立项文档第一页就该写上 Earthly 那句：算力是商品，卖不动。

---

## 来源

1. Depot，[GitHub Actions Runner architecture: The Listener](https://depot.dev/blog/github-actions-runner-architecture-part-1-the-listener)——Listener/Worker 分工、JIT 配置、sessions / message / acquirejob / renewjob 协议细节
2. Gitea 文档，[Design of Gitea Actions](https://docs.gitea.com/1.22/usage/actions/design/)——gRPC over HTTP、actions-proto-def、runner label 语义、容器隔离策略
3. Gitea 博客，[Hacking on Gitea Actions](https://blog.gitea.com/hacking-on-gitea-actions/)——为何 fork nektos/act 成库
4. GitHub Changelog，[Update to GitHub Actions pricing](https://github.blog/changelog/2025-12-16-coming-soon-simpler-pricing-and-a-better-experience-for-github-actions/) 与 [Pricing changes for GitHub Actions](https://github.com/resources/insights/2026-pricing-changes-for-github-actions)
5. CICDCost，[GitHub Actions Pricing Changes 2026](https://cicdcost.com/github-actions-pricing-changes-2026)——自建 runner 收费推迟的时间线
6. RunsOn，[Providers Benchmark](https://runs-on.com/reference/benchmarks-gha-providers/) 与 [Cache benchmark](https://runs-on.com/benchmarks/github-actions-cache-performance/)
7. Bitrise，[The best GitHub Actions runners in 2026](https://bitrise.io/blog/post/best-github-actions-runners-in-2026-and-hidden-pricing-traps-to-avoid)
8. Youngju Kim，[CI/CD Systems 2026 Deep Dive](https://www.youngju.dev/blog/culture/2026-05-16-cicd-systems-github-actions-buildkite-circleci-gitlab-jenkins-argo-tekton-earthly-dagger-2026-deep-dive.en)——分层模型、Tekton 评价、Earthly 关停云服务
9. Kanopy，[Dagger vs Earthly vs Depot](https://kanopylabs.com/blog/dagger-vs-earthly-vs-depot-fast-ci-cd)
10. NomadX，[Tekton vs Argo Workflows (2026)](https://kubernetes.ae/tekton-vs-argo-workflows/)
11. [GitHub Actions Is Slowly Killing Your Engineering Team](https://www.iankduncan.com/engineering/2026-02-05-github-actions-killing-your-team/) 与 [GitHub Actions is the weakest link](https://nesbitt.io/2026/04/28/github-actions-is-the-weakest-link.html)——缓存与本地调试的批评
12. Zylos Research，[Agentic CI/CD and the Rise of CA/CD](https://zylos.ai/research/2026-05-12-agentic-cicd-ai-driven-delivery-pipelines/)
13. GitHub Docs，[Actions Runner Controller](https://docs.github.com/en/actions/concepts/runners/actions-runner-controller)——长轮询与 Job Available 消息

---

*实测部分由 gh CLI 与 GitHub REST API 于 2026-09-08 现场采集：8 个仓库默认分支上的 13 个 workflow 文件（1791 行）、近期运行记录、job 与 step 级失败详情、action 版本清点。星数与许可同日查得。*

*账单与用量接口需要 `admin:org` 与 `user` scope，当前 token 未授权，故成本推算基于公开价目表而非实际发票。*
