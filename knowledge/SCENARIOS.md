# rcast 自然语言测试案例库

更新：2026-10-12 · 基准：feat/gui@bb9e717。结果必须三态：已执行且通过 | 已执行但失败 | 环境不满足未执行。不得只读代码宣布通过。

## S-001：第二控制点抢占正在播放的会话
前置：DMR_ALLOW_PREEMPT 默认开；控制点 A（IP-a）已 Play 且 transport=PLAYING。
动作：控制点 B（IP-b）发 SetAVTransportURI + Play。
预期：1. A 的 IINA 实例被停止（进程退出、套接字删除）；2. 会话 owner 变为 IP-b 并正常播放；3. 无 SOAP 错误。
验证：GetTransportInfo 返回 PLAYING；GetSessionOwner（GUI 菜单）显示 IP-b；`pgrep -f IINA` 与 /tmp 下 rcast 套接字数量。

## S-002：禁止抢占时第二控制点被拒
前置：DMR_ALLOW_PREEMPT=false；A 正在播放。
动作：B 发 Play。
预期：1. SOAP Fault，errorCode=800、描述 ERRC_SESSION_IN_USE；2. A 的播放不受影响。
验证：抓 SOAP 响应体；A 侧 GetPositionInfo 位置仍在前进。

## S-003：自然播完后位置钉在时长
前置：播放一段短片（<1min）；--mpv-keep-open=yes 生效。
动作：等待播放自然结束，并继续等待 reaper 回收（闲置 >10min）或手动再等 1min。
预期：1. transport=STOPPED（end-file 事件驱动）；2. 之后 GetPositionInfo 的 RelTime==TrackDuration，而非 00:00:00。
验证：curl SOAP GetPositionInfo 比较两字段；日志无 "player stop error"。

## S-004：IINA 忙时拒绝重启活实例
前置：IINA 正在播放大文件/慢速网络流（IPC 回复超时 >3s）。
动作：同一 URI 再次 Play（复用探测路径）。
预期：1. 探测超时重试一次后 Play 返回错误、SOAP 501；2. 现存 IINA 实例与播放不被杀掉重启。
验证：IINA 进程 PID 前后一致；日志出现 "IINA ipc still busy, keeping live instance"。

## S-005：第二控制点调音量不抢会话
前置：A 正在播放；B 是另一 IP。
动作：B 发 SetVolume(30)。
预期：1. 返回成功；2. 会话 owner 仍是 A；3. 播放不中断，实际音量变化。
验证：GetVolume=30；A 的 GetTransportInfo 仍 PLAYING。

## S-006：抖音 iOS 音量刻度映射
前置：伪造 UA `Aweme/1.0 CFNetwork/1399 Darwin/22.0` 发起 SetVolume。
动作：从 50 连续步进到 70（每次 +5，共 4 步）。
预期：1. 应用到播放器的音量按 scale=2.5 扩到 0–100 域；2. 该控制端 GetVolume 回读逻辑值 70 而非映射值。
验证：mpv 属性 volume（日志 mapped raw/applied 行）与 GetVolume 响应对比。

## S-007：主机 IP 变化后 LOCATION 刷新
前置：不设 DMR_ADVERTISE_IP；记录当前广播的 LOCATION。
动作：切换网络（Wi-Fi↔有线）触发 IPv4 变化。
预期：1. announce socket 重绑新 IP 并重新 alive；2. 新 M-SEARCH 响应携带新 LOCATION；3. 全程无需重启进程。
验证：日志 "SSDP announce socket rebound"；tcpdump/ssdp 抓包比对 LOCATION。

## S-008：关停期第二信号强制退出
前置：headless 模式正在运行。
动作：发 SIGINT 后立刻再发一次 SIGINT。
预期：1. 第一次进入优雅关停（byebye 有界 flush ≤2s）；2. 第二次直接退出，退出码 130。
验证：`echo $?`==130；日志 "signal ... forcing exit"。

## S-009：IPv6-mapped 地址与 IPv4 是同一控制点
前置：A 以 IPv4 连接并持有会话（双栈环境）。
动作：同一主机经 ::ffff:<IPv4> 映射地址发 Play。
预期：识别为同 owner 续订，不触发自我抢占；播放正常续接。
验证：会话 owner 不变；无 800 错误。

## S-010：GUI 播放中切换需重启的设置
前置：GUI 模式；正在播放。
动作：勾选 Link System Volume（或取消 Allow Preemption）。
预期：1. settings.json 立即持久化该值；2. 服务器不立即重启；3. 播放停止后的下个 STOPPED 快照触发重启，新配置生效。
验证：观察进程重启日志 "gui: server restarted"；播放全程无中断；重启后行为符合新设置。

## S-011：畸形 seek 目标被拒
前置：任意会话。
动作：发 Seek Unit=REL_TIME Target=0:0:NaN（或 0:0:Inf）。
预期：SOAP 711 Illegal seek target；不向 mpv 发出任何 IPC 写。
验证：SOAP 响应；日志无 "calling iina seek"。
