# rcast 修改历史（整理版）

更新：2026-10-12 · 分支：feat/gui@bb9e717（origin/dev=0e895f9，master=1d5ffbe 落后较多，本地 dev 落后 origin/dev 6 个提交——A 级 git 现状）。证据分级：A=提交信息明写，C=推测。

## H-001：初始实现（2025-10）
变化：投屏核心代码、目录结构与模块定名 github.com/tr1v3r/rcast、日志包替换、UUID 持久化路径调整（2fdfc37、5f40a44、64bc194、041c25c、f096cb3 等）。
原因：A（提交可见）；早期演进动机细节未记录——C。
当前影响：UUID 默认路径 `~/.local/rcast/dmr_uuid.txt` 与忽略规则自此时定型，勿随手改路径。

## H-002：协议增强与全屏（2025-12）
变化：seek/断点恢复与更稳的 IPC 处理（c01c1c4、bbf9b7c，PR #1）；IINA 全屏支持（96cb8f8，#2）。
当前影响：Seek 至今只支持 REL_TIME/ABS_TIME（avtransport.go 注释「treat them same」）。

## H-003：生命周期大加固 + 同日补丁串（2026-06-28，PR #3 1ab4a7d）
变化：IPC 请求 id 加锁分配、读写全部加 deadline；后台 reaper 回收闲置播放器/孤儿会话；Pause/Stop/Seek 一并走 requireSession；每安装 UUID；假 mpv IPC 测试服务器。
同日跟进修复（典型「当天补漏」）：抖音 iOS 音量刻度归一（65488fa）、IPC 跨重启恢复（eec4b37）、标题从媒体元数据同步（8ec0c2a）。
原因：A（提交信息详尽）。当前影响：这三条修复都是后来 #7/#9 系列的前身，改动音量/IPC/标题前先读对应测试。

## H-004：测试与 CI 工作流加固（2026-06-28/29，ea9691f、4b6f5a7）
当前影响：`make dev/ci`、race-test、coverage 流程自此时可用；后续大 PR 均以 `-race` 全绿为门槛（提交信息惯例——A）。

## H-005：bug 审计修复系列 #5–#10（2026-09-13/14，dev 线）
变化：一次系统性审计（提交信息引用 output/reports/bug-audit-summary.md 与 t3-net-layer-audit.md，报告文件未入库）驱动六个 PR：
- #5 f43f7a7 清 lint；#6 2b8eb73 升 Go 1.27；
- #7 f38d92a UPnP 语义（音量绕过会话 M1、NaN 入口门、厂商码 800、Track/NrTracks 一致性、XMLText 限定 Body 作用域、ControllerID 折叠 IPv6-mapped）；
- #8 fb55011 SSDP 加固（读错误退避 H1、启动重试 M6、IP 跟踪 M7、MAN 解析 L1、全接口入组 L2、byebye 有界 flush + 第二信号强退）；
- #9 bd6923d IINA 生命周期（~10s IPC 预算 H2、退出确认 M3、忙/死分类 M4、marshal 快败 M5、锁纪律 L1、事件面 M2）；
- #10 0e895f9 播放生命周期一致性（end-file 接线、防误 reap、抢占/停止可重试、SOAP 锁外 flush）。
原因：A（各提交信息逐条列审计编号与动机）。当前影响：代码注释里的 "audit XX" 编号即指这轮审计，是多数「奇怪但不能乱改」行为的出处。

## H-006：GUI 线成型（2026-09-13/14，feat/gui）
变化：抽取可复用 runtime（aeb28ec）→ 快照订阅（1a6f55a）→ 设置持久化（1123328）→ 菜单栏模式（3104938，systray）→ 合并 dev 审计修复（16b8768）→ 评审修复 F1–F6（bb9e717：mute 失败对齐 UPnP 语义/移除死注入面/重启 claim 竞态/退订先于 quit/GUI 第二信号；ed1c1cd：订阅队列容量 1 合并最新）。
原因：A（bb9e717/ed1c1cd 提交信息逐条列 F 编号）。
当前影响：feat/gui 尚未并回 dev/master；systray 事件循环必须占主线程、清理须在 Run 返回后做（onExit 不回调）是平台硬约束。
