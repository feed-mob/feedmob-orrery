# Orrery 机器规格（交给配环境的人）

一句话：**一台 x86_64 的 Ubuntu 22.04，4 vCPU / 16 GB / 100 GB gp3，装 Docker
Engine 和 git，开一个公网 HTTPS 入口。** 下面是每一条的理由，以及为什么不是别的数。

## 为什么是这个数

参照物是 GitHub 自己的 `ubuntu-latest`：**2 vCPU / 7 GB / 14 GB SSD**。你们现有的
workflow 是照着它写的、今天在它上面是通过的，所以"不低于它"是唯一有证据的下界。

实际压力来自三个 workload，不是平均分布的：

| workflow | 干什么 | 吃什么 |
|---|---|---|
| `ci.yml` | `npm ci` → `prisma generate` → `npm test` → `tsc --noEmit` → lint | CPU + 内存（`tsc` 峰值最高） |
| `build-image.yml` | buildx 多阶段构建 Node 22 镜像并推 GHCR | **三样都吃，磁盘最狠** |
| `deploy.yml` | `appleboy/ssh-action` → ssh 到目标机 → `deploy.sh` → curl 冒烟 | 几乎不吃，纯等待 |

所以按 `build-image.yml` 定规格，另外两个自然放得下。

### CPU / 内存：4 vCPU / 16 GB

- Next 的 `npm run build` 加 `tsc --noEmit`，单进程峰值能到 4 GB 以上；buildx 构建时
  容器内再来一份。8 GB 能跑但会贴着上限，16 GB 是"不用再想这件事"的数。
- **别用 t 系列的突发实例跑构建。** `t3.xlarge` 纸面参数好看，但构建是持续满载，
  CPU 积分几分钟就烧光，之后降到基准的 40%，构建时间会莫名其妙从 4 分钟变成 12 分钟，
  而且没有任何报错——这种问题最难查。选 **`m6a.xlarge`**（4 vCPU / 16 GB，AMD，
  非突发）或 `m7i.xlarge`。
- 预算紧就 **`m6a.large`**（2 vCPU / 8 GB），这是**下界**，正好对齐 GitHub 的规格。
  再往下就是拿构建失败换钱。

### 磁盘：100 GB gp3（这条最容易被砍，也最容易出事）

GitHub 每次跑完把整台机器扔掉，所以 14 GB 够。**自建 runner 是累积的**：

| 占地方的东西 | 大概 |
|---|---|
| `catthehacker/ubuntu:act-22.04`（job 的基础镜像） | ~3 GB 解压后 |
| Docker 层缓存（buildx `mode=max`） | **已封顶 10 GB**，见下 |
| 构建出来的镜像，每次一个 | 每个几百 MB |
| action 缓存、npm 缓存、工作目录 | 几 GB |

自建 runner 挂掉的第一名原因就是磁盘满，而且它的表现是构建随机失败、报错五花八门。
100 GB 是"半年不用管"，60 GB 是"记得配个定期清理"。**别给 30 GB。**

### 架构：x86_64，不要 Graviton

`build-image.yml` 没写 `platforms:`，也就是单架构、跟 runner 同架构。上 arm64 意味着
要么你们的生产机也得是 arm64，要么走 QEMU 跨架构构建——后者会让构建慢 5–10 倍。
Graviton 便宜 20%，但这个场景下省不到。

### 系统：Ubuntu 22.04 LTS

对齐 job 的基础镜像 `act-22.04`。24.04 也行，但没有理由制造这个差异。

## 装什么

```bash
# Docker Engine（不是 Docker Desktop）
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker ubuntu        # 让 runner 不用 sudo 起容器

sudo apt-get install -y git
```

**就这两样。** Orrery 自己是一个静态编译的 Go 二进制，没有运行时依赖、不需要装 Go、
不需要数据库（控制面用 SQLite，一个文件）。

## 网络

| 方向 | 谁 | 端口 | 干什么 |
|---|---|---|---|
| **入** | GitHub 的 webhook 出口 | 443 | 送 push / PR 事件。**没有这个就没有自动触发** |
| **入** | 你们自己（办公网 / VPN） | 443 | 看 Web UI |
| 入 | job 容器 → 宿主机 | 34567 | 共享产物库。同机部署时走 docker 网桥，不用开公网 |
| **出** | → github.com / codeload / ghcr.io | 443 | 拉代码、拉 action、推镜像 |
| **出** | → registry-1.docker.io | 443 | 拉 job 的基础镜像 |
| **出** | → registry.npmjs.org | 443 | `npm ci` |
| **出** | → 部署目标机 | 22 | `appleboy/ssh-action` |

公网入口建议用 Caddy 或 nginx 终止 TLS 再反代到 `127.0.0.1:8080`——Orrery 自己不做
TLS，也不该做。

## 一台还是两台

**先一台。** 控制面（`orrery-server`）和 runner（`orrery-runner`）跑在同一台上，产物库
走宿主机私网地址，没有跨网络路由的麻烦，规格就是上面那份。

**什么时候拆**：加第二台 runner 的时候。那时候控制面单独一台 `t3.small`（2 vCPU /
2 GB 就够，它只有 Go + SQLite），runner 按上面的规格加，产物库已经是中心化的，加机器
不用改架构。

## 不要放在哪

**不要用 `18.216.42.33`。** 那台跑着 ClickClack、Hermes、PostgreSQL 和 4 个公网域名的
Caddy。runner 放上去意味着：任何仓库的任何 workflow 里一句 `run: psql ...` 就能读生产
库——不需要漏洞，`run:` 本来就是任意 shell；而且构建满载时会和生产服务抢 CPU 和磁盘
IO。这也是你们自己写下的规矩：**必须是隔离主机，绝不能是应用/数据库那台**。

## 配好之后，已知要踩的三个坑

不影响选规格，但配环境的人会撞上：

0. **缓存容量已经封顶，不用再操心。** `build-image.yml` 的 `cache-to: type=gha,mode=max`
   是多阶段 Dockerfile 的正确写法（`npm ci` / `npm run build` 都在被丢弃的 `build`
   阶段，用默认的 `mode=min` 等于一层都不缓存）。act 的淘汰只看时间不看容量，所以
   Orrery 自己加了上限，默认 10 GB、对齐 GitHub，超了按最久未使用往下删：
   `-cache-max-gb`。**磁盘规格里那 10–30 GB 的不确定性就是被这条消掉的。**

1. **`cache-from: type=gha` 可能不生效。** `build-image.yml` 用 GitHub 的缓存后端存
   Docker 层。Orrery 的缓存服务端实现的是 v3 协议（`_apis/artifactcache`），新版 buildx
   可能要走 `ACTIONS_RESULTS_URL`——我们**有意不设**那个变量。最坏情况是缓存不命中、
   构建变慢，不是构建失败。第一次跑要专门看一眼。
2. **`$GITHUB_STEP_SUMMARY` 的内容会丢。** `deploy.yml` 用它写成功/失败摘要。act 把它
   写进容器里就再也不读回来（已知缺口，要改 act 那棵树才能修）。部署照常，摘要看不到。
3. **第三方 action 要先进白名单，并且钉 SHA。** `appleboy/ssh-action@v1`、
   `docker/build-push-action@v6` 这些走 `-allowed-actions`。`@v1` 是可变 tag，
   tj-actions 2025-03 那次供应链事故就是这么来的——钉成 40 位 SHA，用 `-require-action-sha`
   强制。
