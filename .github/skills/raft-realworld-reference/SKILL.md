---
name: raft-realworld-reference
description: 用 raft-example 作为“真实业务落地 Raft”的参考指南：快速定位写入/读一致性/快照/成员变更/leader 转发等关键点，并给出改造清单。
license: MIT
compatibility: Go + Hashicorp Raft + gRPC（本仓库示例）。
metadata:
  author: raft-example
  version: "1.0"
  generatedBy: "manual"
---

面向用户：你正在实现一个真实业务的 Raft 应用（KV、元数据服务、配置中心、分布式锁等），希望参考本仓库的落地路径。

本 skill 的目标：
- **把本仓库里“真实会踩坑的 Raft 落地要点”串起来**（leader 转发、线性一致读、日志/快照/存储、集群成员变更、gRPC 接口边界）。
- 给出一套**“拿去就能改”的入口清单**，让用户能快速映射到自己的应用。

> 约束：必须以代码为准。需要时先阅读仓库文件，再给建议；不要凭空假设实现细节。

---

## 项目速览（你应该怎么读这个 repo）

### 1) 启动与生命周期
- 入口：`app.go`
  - 解析参数：`--raft_addr`、`--grpc_addr`、`--raft_id`、`--raft_data_dir`、`--raft_bootstrap`
  - 启动 `engine.NewEngine(...)`，收到信号后 `engine.Close()`

### 2) 组合层（Engine）
- `engine/engine.go`
  - 构建 FSM：`fsm.NewStateMachine(...)`
  - 构建 Raft：`newRaft(...)`
  - 启动 gRPC：注册 `Example` 业务服务与 `raft-manager` 管理服务

### 3) Raft 初始化与存储
- `engine/raft.go`
  - `raft.Config`：心跳/选举/快照等参数在这里
  - 日志/稳定存储：`raft-pebbledb`（`pebbledb.NewPebbleStore`）
  - 快照：`raft.NewFileSnapshotStore(...)`
  - 传输：`raft.NewTCPTransport(...)`
  - leader 监控：`NotifyCh` + `RegisterObserver`（用于 leader 变更观测/转发）

### 4) 状态机（FSM）与业务数据存储
- `fsm/fsm.go`
  - `Apply`：提交日志后调用，必须确定性
  - `Snapshot/Persist`：快照生成与持久化
  - `Restore`：从快照恢复
- `fsm/store.go`
  - 业务数据存 Pebble（与 Raft log 的 PebbleStore 分开目录）
  - 快照用 iterator 顺序写出（value gzip），恢复时新建目录 + swap
- `fsm/apply.go`
  - Command 模型：`Insert` / `Query`，payload JSON

### 5) gRPC 层：leader 转发与一致性读
- `service/service.go`
  - 写：`Add`（如果不是 leader，则转发到 leader 执行）
  - 读：`Get`
    - `Linearizable=false`：本地读（可能陈旧）
    - `Linearizable=true`：转发到 leader + 走一致性路径
  - leader → gRPC 地址映射：`server_lookup`（示例里是常量表，真实场景需要动态发现）
- `engine/kv.go` + `engine/linearizable.go`
  - 一致性门禁：`VerifyLeader()` + leader barrier ready（`readyForConsistentReads`）
  - 线性一致读：示例用“读也走 Apply（Query command）”的方式，保证经过 Raft 序列化

---

## 典型真实场景该怎么套（建议按这个顺序做）

### A. 先定义“写入模型”
- 本仓库示例把业务操作编码为：
  - `fsm.Command{Type, Payload}`（JSON）
  - `Insert` 时 `Payload` 是 `SetPayload{Key, Value}`（JSON）
- 你在真实业务里应该做：
  1. 定义你的 command schema（建议：protobuf 或 msgpack；JSON 也行但要稳定）
  2. `FSM.Apply()` 里只做 **确定性**、**可重放** 的状态变更
  3. 返回值只返回“结果”而不是不可序列化对象

对应入口：`fsm/apply.go`、`fsm/fsm.go#Apply`

### B. 明确“读一致性”分层
- 你至少要支持两类读：
  - **本地读（可能陈旧）**：低延迟
  - **线性一致读（Linearizable）**：强一致
- 本仓库做法：
  - `Linearizable=false`：直接读 Pebble（`ReadLocal`）
  - `Linearizable=true`：转发到 leader，并通过 Raft 序列化保证一致

对应入口：`service/service.go#Get`、`engine/kv.go#Query`、`engine/linearizable.go#ConsistentRead`

### C. 解决“只能 leader 执行 Apply”的现实
- Hashicorp Raft 要求 `Apply` 必须在 leader 调用，否则会失败。
- 所以业务层必须支持 leader 转发。

本仓库的最小实现：
- `service/GRPCService.forwardRequestToLeader(...)` 找 leader，然后 gRPC 调用 leader 的同名 RPC。

真实业务你需要补齐：
- leader 的 **业务 RPC 地址** 发现（示例用 `server_lookup` 常量表，生产要动态）
  - 可选：memberlist/gossip、服务注册（Consul/etcd）、DNS、K8s headless service
- TLS / mTLS（跨机房/公网必须）
- 失败重试策略（leader 变更、网络抖动）

对应入口：`service/service.go#forwardRequestToLeader`

### D. 快照与恢复（决定你的可维护性）
- 快照的关键点：
  - `Snapshot()` 要快：只拿 snapshot 句柄，不做重 IO
  - `Persist()` 才做重 IO
  - 恢复要能原子切换（示例：新目录写完→记录当前目录→swap→删除旧目录）

对应入口：`fsm/fsm.go#Snapshot/#Restore`、`fsm/store.go#saveSnapShot/#recoverSnapShot`

### E. 集群成员变更（上线后必然用到）
- README 给了一个推荐路径：使用 `raft-manager` 与 `add_voter` 加入节点。
- 真实场景你要考虑：
  - 新节点 catch-up（applied index / snapshot）
  - 观测 follower lag（raft stats）
  - 迁移/缩容流程（remove server）

对应入口：`README.md`（manager 操作）、`engine/kv.go#Nodes/#Stats`

### F. 动态 Leader 地址发现（生产必做）
- 硬编码 `server_lookup` 只适合开发环境
- 真实业务需要：节点上下线自动感知、leader 地址动态获取

本仓库实现：
- `gossip/gossip.go`：使用 hashicorp/memberlist 实现节点发现
- `resolver/resolver.go`：抽象层（支持 Static / Gossip 两种实现）
- `engine/engine.go#EngineConfig`：配置 `GossipEnabled`、`GossipAddr`、`GossipSeeds`
- `service/service.go#NewGrpcServiceWithResolver`：注入 resolver

启用方式：
```bash
./raft-example --raft_id=node1 --raft_addr=... --grpc_addr=... \
  --gossip_addr=0.0.0.0:7946 --gossip_seeds=192.168.1.2:7946,192.168.1.3:7946
```

对应入口：`gossip/gossip.go`、`resolver/resolver.go`、`engine/engine.go#setupGossip`

### G. ReadIndex 风格线性一致读（推荐优化）
- 原始方案：读走 Apply（写 Raft log），开销大
- 优化方案：VerifyLeader + barrier，不写 log

本仓库做法（已优化）：
- `engine/kv.go#Query`：`consistent=true` 时调用 `ConsistentRead()` + 本地读
- `engine/linearizable.go#ConsistentRead`：`VerifyLeader()` + barrier 检查

对应入口：`engine/kv.go#Query`、`engine/linearizable.go#ConsistentRead`

### H. 优雅关闭（Graceful Shutdown）
- 生产环境：关闭前应等待在途请求完成，而不是直接切断

本仓库实现：
- `engine/engine.go#Close`：
  - `grpcServer.GracefulStop()`（带超时）
  - 优雅退出 gossip 集群（`gossip.Leave()`）
  - 关闭 Raft transport、FSM

对应入口：`engine/engine.go#Close`

---

## 快速验证清单（真实落地建议最少做到这些）

### 正确性
- [ ] 所有写入都能在非 leader 节点通过转发成功
- [ ] `Linearizable=true` 的读在 leader 切换期间可用（允许短暂失败，但不应返回陈旧值）
- [ ] 快照后重启/恢复一致（随机写入→触发快照→kill -9→重启→校验数据）

### 可运维性
- [ ] 暴露基础指标：leader、applied index、log size、snapshot 次数、fsm dir size
- [ ] 明确数据目录布局：raft log / stable store / snapshot / user data 分开
- [ ] 优化 `raft.Config`：针对你的写入量调 `SnapshotThreshold`、`TrailingLogs`、超时参数

### 安全与可靠性（生产必做）
- [ ] gRPC 使用 TLS/mTLS
- [ ] `server_lookup` 替换为动态发现
- [ ] 请求超时、重试、幂等语义（尤其是写）

---

## “我该改哪些文件？”（面向迁移的最小改造路径）

1. 业务接口：扩展 `proto/service.proto` + `service/service.go`
2. 命令编码：扩展 `fsm/apply.go` 的 `CommandType` / payload
3. 状态存储：扩展 `fsm/store.go`（或替换为你自己的存储引擎）
4. 一致性策略：按需调整 `engine/kv.go#Query`（是否让读走 Apply）
5. leader 发现：替换 `service/service.go` 的 `server_lookup`

---

## 按场景套用模板（KV / 配置中心 / 元数据服务）

这一段给你一个“从业务需求 → Raft 命令 → FSM Apply → 读一致性 → 快照恢复”的套用骨架。你可以直接按模板在自己的工程里落地，然后再回头对齐本仓库的实现细节。

### 场景 1：KV（类似本仓库）

**目标**：写入强一致；读支持本地读与线性一致读两种。

**命令设计（建议）**
- CommandType：`Put` / `Get` / `Delete` / `BatchPut`（示例仓库是 `Insert` / `Query`）
- Payload：建议 protobuf（稳定、可演进）；示例仓库用 JSON 便于理解

**写路径模板**
1. gRPC `Put` → 若非 leader：转发到 leader（生产要动态发现 leader 的业务 RPC 地址）
2. leader `Put` → `Apply(cmd)` 进入 Raft log
3. `FSM.Apply(log)` → 对底层存储执行确定性写入

**读路径模板**
- `consistent=false`：本地读（低延迟）
- `consistent=true`：
  - 方案 A（本仓库）：读也封装成 Raft 命令并 `Apply(Query)`，读经过 Raft 序列化
  - 方案 B（你可能想做）：leader `VerifyLeader + barrier/lease` 后直接本地读（不写 log）

**快照模板**
- `Snapshot()`：只拿 snapshot 句柄
- `Persist()`：遍历 KV 全量写出（可压缩）
- `Restore()`：新目录恢复 → swap → 清理旧目录

### 场景 2：配置中心（多 key、watch、灰度）

**目标**：
- 写入强一致（发布配置必须线性化）
- 读大多允许本地读，但“发布后立即可见”需要线性一致读或读修正策略
- 需要 watch/推送（不建议把 watch 事件本身写进 Raft log）

**命令设计模板**
- `PublishConfig(namespace, key, value, version)`：写入并返回版本号（version 由 leader 生成并写入状态）
- `DeleteConfig(...)`
- `UpsertNamespaceMeta(...)`

**读一致性建议**
- 读 API：`GetConfig(consistent: bool)`
- watch：
  - watch 订阅只走 gRPC 流，不进 Raft
  - 由 leader 在 `FSM.Apply` 后向本地订阅者广播；follower 也可以广播（基于各自 Apply 的结果），避免中心化

**需要额外补齐的能力**
- 版本语义：防止旧版本覆盖新版本（幂等）
- 限流与发布审核：建议在业务层实现，不写入 Raft
- 大 value：考虑分块/外部存储（Raft log 不适合超大 payload）

### 场景 3：元数据服务（目录/表/分片路由等）

**目标**：
- 强一致写入（创建/删除/变更元数据）
- 强一致读（读多但对一致性敏感，例如路由/权限）
- 常见需要支持 CAS / 事务化检查（至少是 Compare-And-Set）

**命令设计模板**
- `CreateTable(name, schema, ifNotExists)`
- `AlterTable(name, changes)`
- `DropTable(name)`
- `CAS(key, expectedVersion, newValue)`（或 `Txn(ops...)` 但要控制复杂度）

**一致性读策略建议**
- 首选：所有“必须一致”的读走 leader 线性化路径（本仓库的 Linearizable=true 思路）
- 可选优化：对只读热点元数据做 follower 本地缓存 + 版本校验（读时带版本、发现落后再走 leader）

**快照与恢复建议**
- 元数据通常量不大：快照更频繁、恢复更快
- 重点在“版本/索引”的正确恢复：恢复后要确保版本单调不回退

---

## 关键函数索引（文件 → 符号）

用这张表可以快速从“概念”跳到“代码入口”。

### 启动与组装
- `app.go`: `main()`（支持 `--gossip_addr`、`--gossip_seeds` 参数）
- `engine/engine.go`: `NewEngine(...)`、`NewEngineWithConfig(cfg)`、`(*Engine).Close()`
- `engine/engine.go`: `(*Engine).setupGossip(cfg)`（可选 gossip 初始化）
- `engine/engine.go`: `EngineConfig`（包含 gossip 配置项）

### Raft 初始化/存储/leader 观测
- `engine/raft.go`: `newRaft(...)`
- `engine/raft.go`: `(*raftServer).monitorLeadership()`、`(*raftServer).leaderLoop()`
- `engine/raft.go`: `(*raftServer).observe()`（Observer 监听 leader/心跳异常）

### 写入/读取 API（业务侧调用入口）
- `engine/kv.go`: `(*raftServer).Apply(ctx, key, val)`
- `engine/kv.go`: `(*raftServer).Query(ctx, key, consistent)`
- `engine/kv.go`: `(*raftServer).GetLeader()`、`(*raftServer).IsLeader()`
- `engine/kv.go`: `(*raftServer).Stats()`、`(*raftServer).Nodes()`

### 线性一致读门禁
- `engine/linearizable.go`: `(*raftServer).ConsistentRead()`
- `engine/linearizable.go`: `(*raftServer).consistentReadWithContext(ctx)`

### FSM（决定性 Apply + 快照/恢复）
- `fsm/fsm.go`: `(*StateMachine).Apply(log)`
- `fsm/fsm.go`: `(*StateMachine).Snapshot()`、`(*fsmSnapshot).Persist(sink)`
- `fsm/fsm.go`: `(*StateMachine).Restore(reader)`
- `fsm/fsm.go`: `(*StateMachine).ReadLocal(key)`

### 命令编码与存储落盘
- `fsm/apply.go`: `CommandType`、`(*store).applyCommand(data)`、`(*store).insert(...)`、`(*store).query(...)`
- `fsm/store.go`: `(*store).saveSnapShot(...)`、`(*store).recoverSnapShot(...)`、`(*store).close()`
- `fsm/wb.go`: `pebbleWriteBatch`（恢复时的批量写入）

### gRPC 与 leader 转发
- `proto/service.proto`: `service Example`（Add/Get/Stat）
- `service/service.go`: `NewGrpcService(...)`、`NewGrpcServiceWithResolver(...)`
- `service/service.go`: `(*GRPCService).Add(...)`、`(*GRPCService).Get(...)`、`(*GRPCService).Stat(...)`
- `service/service.go`: `(*GRPCService).forwardRequestToLeader(...)`（核心：leader 转发）
- `service/service.go`: `getOrCreateConn(...)`（连接缓存）

### 动态服务发现（Gossip）
- `gossip/gossip.go`: `New(cfg Config)`、`(*Gossip).GetGRPCAddr(nodeID)`
- `gossip/gossip.go`: `(*Gossip).OnJoin(callback)`、`(*Gossip).OnLeave(callback)`
- `gossip/gossip.go`: `(*Gossip).Leave(timeout)`、`(*Gossip).Shutdown()`
- `gossip/gossip.go`: `ParseHostPort(addr)`

### 地址解析器（Resolver）
- `resolver/resolver.go`: `AddressResolver`（接口）
- `resolver/resolver.go`: `NewStaticResolver(initial)`、`NewGossipResolver(getter)`


---

## 交互式帮助（你可以怎么用我）

你可以直接告诉我你的真实场景，我会基于这个仓库给你一份“改造落点”清单：
- 你的业务模型是什么（KV/元数据/锁/队列）
- 读一致性要求（读多写少？允许陈旧？必须线性一致？）
- 部署环境（裸机/K8s/多机房）
- 节点规模与写入量（影响 raft 参数与快照策略）

我会输出：
- 需要修改的文件列表与关键函数
- leader 转发与发现方案建议
- 一致性读的实现选择（VerifyLeader + barrier vs 读走 Apply vs ReadIndex 风格）
- 运维指标与故障演练脚本建议
