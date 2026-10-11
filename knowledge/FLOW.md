# rcast 运行流程（组件 = 整个 DMR 应用）

更新：2026-10-12 · 基准：feat/gui@bb9e717（工作树现状）· 入口 main.go / internal/app/runtime.go

```text
【发现（启动 + 网络变化）】app.Start
  加载 UUID(uuid.LoadOrCreate) → 选 IPv4(netutil.FirstUsableIPv4；DMR_ADVERTISE_IP 则固定)
  ├─ HTTP :8200(默认) 注册 /device.xml /upnp/control/* /upnp/event/* /metrics
  ├─ AnnounceTracking: 每 30s 发 6 条 ssdp:alive(ssdp.announceLoop)
  │   拨号失败 → 指数退避 1s→30s 无限重试(开机竞态 #8 M6)
  └─ SearchResponderTracking: 监听 239.255.255.250:1900(respondLoop)
      listen 失败 → 同上重试；所有合格 IPv4 接口入组播组(#8 L2)
      读循环 2s deadline ──持续读错误→退避 100ms→1s(防 ~20M/s 忙转 #8 H1)
      M-SEARCH 的 MAN 头按字段解析校验(#8 L1)；响应用 32 容量信号量
      + MX(1-5s)随机延迟异步发出，超限丢弃并告警
  主机 IP 变化: announce tick → BaseURLSource.refreshIP() 重写 LOCATION(保端口)
    → 关旧 socket 重拨绑新 IP → 重新 alive；alive 全部写失败 → 立即复查重绑(#8 M7)
    固定 DMR_ADVERTISE_IP 时不跟踪

【会话建立】SOAP POST /upnp/control/* → ControllerID(IPv4 规范化，::ffff:x 折叠 f38d92a)
  AVTransport 变更类动作先 requireSession → AcquireSession:
  ├─ 无主 / 同主 → 获得会话(同主续订 sessionUsed；上次抢占未清理完则继续要求重试 0e895f9)
  ├─ 异主 + 允许抢占(默认) → 抢占: transport=STOPPED, clearTransportOnStop=true
  │    └─ StopPlayer 失败 → SOAP 501(下次动作重试清理)
  └─ 异主 + 禁止抢占 → SOAP 800 ERRC_SESSION_IN_USE(厂商码, f38d92a)
  注: SetVolume/SetMute 刻意不走会话 —— 音量是渲染器全局属性(f38d92a M1)

【播放控制】serializeSOAP: commandMu 按到达序执行，SOAP 响应缓冲、解锁后 flush(0e895f9)
  Play: TRANSITIONING → EnsurePlayer(工厂实时读 fullscreen 设置) → IINAPlayer.Play:
  ├─ 复用探测 get_property path:
  │   ├─ 同 URI → SetVolume+Resume+bringToFront
  │   ├─ 回复超时 = IINA 忙 → 重试 1 次后报错，绝不重启活实例(#9 M4)
  │   └─ EOF/ECONNREFUSED/写失败 = 已死 → Stop 后全新启动
  ├─ 启动: iina-cli 优先，否则 /Applications 走 open -n -a IINA
  │   参数 --keep-running --mpv-input-ipc-server=/tmp/rcast_iina-ipc-sock_<uuid>
  │        --mpv-volume --mpv-keep-open=yes [+fs]；waitForIPC ~10s 预算指数退避(#9 H2)
  ├─ IPC(ipc_transport.go): send 持锁仅到 write；每连接代际一个后台 readLoop
  │   按请求 id 配对回复；mpv 事件入 64 容量队列 → OnEvent 串行派发(#9 M2/L1)
  │   marshal 失败立即失败(#9 M5)；套接字 chmod 0600 尽力而为(#9 L6)
  ├─ DIDL <title> → force-media-title；成功 → PLAYING；任一步失败 → STOPPED + SOAP 501
  └─ mpv end-file 事件 → handlePlayerEvent: 代际校验(晚到事件不覆盖新实例)
      → transport=STOPPED；finalPosition 钉在媒体时长(0e895f9)

【会话结束 / 异常】
  Stop 动作 → StopPlayer(iina_stop.go 可靠停):
    IPC 送 quit(socket 未出现 → 轮询等待晚到的 socket, #9 H2)
    → 等退出证据: socket 文件消失 / IPC EOF / 启动器退出(#9 M3)
    → 超时升级: SIGTERM iina-cli → AppleScript quit(open 启动路径仅有杠杆) → SIGKILL
    → STOPPED + ReleaseSession(失败的 Stop 保留引用供重试, 0e895f9)
  reaper 每分钟: 非 PLAYING 且 10min 未用的播放器 → 停掉；无播放器的孤儿会话 → 清
  进程退出: ctx cancel → HTTP Shutdown(3s) → SSDP byebye 有界等待 2s(#8)
    → state.Stop 用全新 2s 窗口送 quit(app ctx 已取消)
    关停中再收 SIGINT/SIGTERM → 强退码 130(main.go armForceExit, #8)

【GUI 前端(feat/gui)】internal/gui controller 订阅快照(队列容量 1, 合并最新 ed1c1cd)
  菜单动作与 UPnP 同语义走 Serialize；服务器自死 → watchRuntime 触发退出
  设置分级: debug 即时 log.SetLevel；fullscreen 由播放器工厂下次会话生效；
  linkVolume/allowPreempt 需重启 —— 空闲(读实时态)立即重启，播放中挂起
  待 STOPPED 快照；重启是互斥锁守护的 claim + 重挂重检循环(bb9e717 F4)
  退出: 等 viewReady(≤10s) → 先退订再 view.quit(防渲染竞入 Cocoa, F5)；
  清理在 systray.Run 返回后(systray Quit 路径不回调 onExit)
```
