# rcast 架构与选型说明

更新：2026-10-12 · 基准：feat/gui@bb9e717。证据等级：A=提交/代码可回查，C=推测。

## D-001：播放控制走 mpv JSON IPC，AppleScript 只作系统音量与兜底退出
背景：IINA 是 mpv 前端；播放控制需要低延迟、可轮询（GetPositionInfo）。选择：`--mpv-input-ipc-server` Unix 套接字 + JSON IPC（internal/player/iina.go, ipc_transport.go）。替代：AppleScript 全量控制（慢、无位置查询）——仅保留 `set volume` 与 quit 兜底（system_volume_darwin.go, iina_stop.go）。证据：A（#9 bd6923d；CLAUDE.md「AppleScript is used only for macOS system volume linkage」）。重新评估：更换播放器或 IINA 关闭 IPC 入口时。状态：生效中。

## D-002：单控制器会话所有权，冲突返回厂商错误码 800
背景：多个 DLNA 控制点会同时投屏。选择：按规范化 IP 识别控制器，同时只允许一个 owner；抢占可配置（`DMR_ALLOW_PREEMPT`，默认开）；拒绝时返回 SOAP 800 `ERRC_SESSION_IN_USE`（UPnP 700–799 保留给标准码，f38d92a）。动机「为什么是单会话而非多会话」：C（推测为简化语义，维护者不在场）。重新评估：出现真实的多控制点并发需求或 GENA 事件（D-006）落地时。状态：生效中。

## D-003：变更命令全局排序，SOAP 响应在锁外 flush
背景：GUI 本地控制与远端 UPnP 命令并发到达。选择：`state.SerializeResult` 用 commandMu 按到达序执行；响应先写入缓冲，解锁后才写网络（soap.go bufferedSOAPResponse，0e895f9）。替代：每命令独立加锁——会导致抢占/停止交错。重新评估：若命令粒度需要并行化（如音量与传输解耦）时需重新设计锁域。状态：生效中。

## D-004：IPC「忙」与「死」分类——超时绝不重启活实例
背景：大文件 demux/慢网络流时 IINA 回复慢但存活；错误重启会打断用户观看。选择：仅 deadline 超时判「忙」（重试一次后报错保留实例）；仅 EOF/ECONNREFUSED/写失败判「死」（重启）（ipc_transport.go isIPCBusy，#9 M4）。证据：A（bd6923d）。重新评估：mpv 提供可靠 liveness 探针时。状态：生效中。

## D-005：音量/静音绕过会话 + 抖音 iOS 刻度映射
背景：DLNA 语义里音量是渲染器全局属性（f38d92a M1）；Aweme iOS 端 8 档×5 点只有 40 点逻辑区间。选择：SetVolume/SetMute 不走 requireSession；UA 匹配 `aweme/`+CFNetwork+Darwin 时 scale=2.5，`volumeMapping` 保留该控制器的逻辑原始值供 GetVolume 回读（renderingcontrol.go, state.go mapVolumeRequest）。证据：A（65488fa, f38d92a）。重新评估：其他粗刻度客户端需要同样映射时改为按客户端特征表。状态：生效中。

## D-006：GENA 事件不实现，端点返回 501
背景：控制点靠轮询 GetPositionInfo/GetTransportInfo 获取状态。选择：SUBSCRIBE/UNSUBSCRIBE 返回 501——不发假 SID 让控制点空等（event.go 注释）。动机「暂缓还是永久放弃」：C。影响：状态推送类客户端体验受限。重新评估：实现 LastChange 事件并接入 state 订阅时。状态：生效中。

## D-007：SSDP 按「网络随时会坏」设计
背景：launchd 开机竞态、接口拔插、IP 漂移都会让设备不可发现或广播陈旧 LOCATION。选择：socket 建立无限重试（1s→30s 退避）；BaseURLSource 跟踪 IP 变化并重绑 announce socket；所有合格 IPv4 接口入组播组；读错误退避防忙转；关停有界等待 byebye 2s + 第二信号强退（ssdp.go, main.go，#8）。证据：A（fb55011）。重新评估：几乎不成立；新增接口类型（纯 IPv6）时需复核。状态：生效中。

## D-008：GUI 设置分级生效，需重启项播放中延迟执行
背景：handlers 在启动时捕获配置；重启会打断播放。选择：debug 即时生效；fullscreen 由播放器工厂在下个会话读取；linkVolume/allowPreempt 空闲立即重启、播放中挂起到 STOPPED（gui.go doToggle/scheduleRestart，bb9e717 F4 修复重启竞态）。证据：A（bb9e717；README「GUI setting behavior」）。重新评估：handlers 改为读动态配置源时可取消重启机制。状态：生效中。

## D-009：mpv keep-open + end-file 事件钉住自然播完位置
背景：keep-open 让 mpv 播完暂停在结尾而非退出；窗口随后可能被 reaper 回收，晚到的查询会拿到 0。选择：订阅 mpv `end-file` 事件（带代际校验），把 finalPosition 钉在媒体时长（state.go handlePlayerEvent，#9 M2 + 0e895f9）。证据：A（bd6923d, 0e895f9）。重新评估：关闭 keep-open 或换播放器时。状态：生效中。
