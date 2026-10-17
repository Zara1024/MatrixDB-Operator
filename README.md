# MatrixDB Operator

<p align="center">
  <img src="docs/images/kubernetes.svg" width="90" alt="Kubernetes" />
  &nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;
  <img src="docs/images/mysql.svg" width="130" alt="MySQL" />
</p>

<p align="center">
  <strong>金融级零丢数据 (RPO=0) 云原生 MySQL Kubernetes Operator</strong>
</p>

<p align="center">
  <a href="https://github.com/Zara1024/MatrixDB-Operator/actions"><img src="https://img.shields.io/badge/CI-Passing-brightgreen?style=flat-square" alt="CI"></a>
  <a href="https://golang.org/"><img src="https://img.shields.io/badge/Go-1.22+-blue?style=flat-square" alt="Go Version"></a>
  <a href="https://kubernetes.io/"><img src="https://img.shields.io/badge/Kubernetes-1.28%2B-blue?style=flat-square" alt="K8s Version"></a>
  <a href="https://www.mysql.com/"><img src="https://img.shields.io/badge/MySQL-8.0%20%2F%208.4%20LTS-orange?style=flat-square" alt="MySQL Version"></a>
  <a href="docs/verification-report.md"><img src="https://img.shields.io/badge/Tests-26%2F26%20PASSED-brightgreen?style=flat-square" alt="Test Status"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache%202.0-blue?style=flat-square" alt="License"></a>
</p>

<p align="center">
  <strong>简体中文</strong> | <a href="README_EN.md">English</a>
</p>

---

## 📖 项目概述 (Overview)

**MatrixDB Operator v3.0.0** 是一款面向企业级生产环境、金融级零丢数据（RPO=0）的开源云原生 MySQL Kubernetes 控制器。全面自动化纳管 **MySQL 8.0 / 8.4 LTS** 集群在 Kubernetes 上的全生命周期运维。

深度对标业界顶级开源项目（如 **Cybozu MOCO**、**CloudNative-PG**、**Percona MySQL Operator**），MatrixDB Operator 摒弃了基于脆弱脚本或粗暴目录大小计算的传统方案，在 Kubernetes 上原生实现了云厂商级（Cloud-Provider-Grade）的数据库自治体验。

---

## 🏛️ 架构设计全景图 (Architecture)

<p align="center">
  <img src="docs/images/architecture.svg" alt="MatrixDB Operator 架构设计全景图" width="100%" />
</p>

---

## ✨ 核心生产特性 (Key Capabilities)

### 1. 🛡️ 金融级无损半同步复制（RPO = 0）
- 主节点事务必须等待从库 Relay Log 刷盘确认（`AFTER_SYNC`）后方可对客户端提交。
- 控制器动态调控 Quorum 法定人数，配合 `rpl_semi_sync_master_wait_no_slave=0` 防止网络分区死锁，实测 5000+ QPS 暴力断电下**零事务丢失**。

### 2. ⚡ 亚秒级自动故障倒换与自愈（RTO < 3s）
- 基于 Go 原生 MySQL 连接池实时心跳探测，主节点猝死后 3 秒内完成 GTID 代数选主；
- 自动原子切换 `<cluster>-primary` 写入路由；宕机旧主重启后自动解除写锁、收编为从库自愈重回拓扑。

### 3. ⏪ S3 连续 Binlog 流式归档与任意时间点恢复 (PITR)
- 增量 Binlog 实时连续归档上传至 AWS S3 / MinIO 远端对象存储；
- 遭遇人为误删或灾难性破坏时，下发 Restore CR 指定历史安全时间戳，控制器自动基于基础快照并连续重放 Binlog，精确回到事故前那一秒。

### 4. 🚫 Kubernetes Validating Webhook 准入硬性拦截
- 独立运行于 `:9443` 端口，自动签发自签名 TLS 证书；
- 在 Kubernetes API Server 层物理拦截并拒绝非法 CRD 写入（如 `replicas < 1`），杜绝脏数据入库 Etcd。

### 5. 🌐 多可用区跨主机打散反亲和（TopologySpreadConstraints）
- 底层 StatefulSet 强制注入多可用区分布约束（`kubernetes.io/hostname`, `topologyKey`），确保副本跨机架、跨可用区均匀分布，杜绝单机房故障瘫痪。

### 6. 🚦 ProxySQL 2 节点高可用连接池与读写分离
- 声明式一键启用 ProxySQL 读写分流代理集群，自动收编后方节点，提供连接池削峰填谷与智能 SQL 路由。

### 7. 📊 专属分析型副本池（OLAP Workload Isolation）
- 通过专有标签划分分析型从库，暴露独立专属 Service，彻底将报表慢查询与核心在线事务物理隔绝。

### 8. 🩺 Autonomous DBA Doctor 元数据锁 (MDL) 自治诊断
- 实时探测阻塞 DDL 的长事务与死锁源，超过安全阈值自动精准 Kill 阻塞源会话，保障业务建表/加字段零挂死。

### 9. 🔄 声明式 DDL 结构演进（GitOps Schema Migration）
- 引入 `MatrixDBSchemaMigration` CRD，版本化声明 SQL 变更，自动上报执行耗时与审计日志。

### 10. 📈 生产级 Helm 可观测打包
- 官方发布 Helm Chart 内嵌开箱即用的 5 大核心面板 Grafana Dashboard 及 4 大核心告警规则 PrometheusRule。

---

## 📊 与业界成熟开源项目横向对比

| 功能维度 | MatrixDB Operator v3.0.0 | Cybozu MOCO | Percona Operator | CloudNative-PG (PG) |
| :--- | :---: | :---: | :---: | :---: |
| **底层抽象** | StatefulSet + 多可用区打散 | StatefulSet | 自定义 Pod | StatefulSet 模式 |
| **复制协议** | 无损半同步 (`AFTER_SYNC`) | 无损半同步 | Group Replication | 物理流复制 |
| **极端崩溃数据丢失** | **严格保证 RPO = 0** | RPO = 0 | RPO = 0 | RPO = 0 |
| **任意时间点恢复 (PITR)** | **支持 (连续 S3 Binlog 归档)** | 支持 | 支持 (XtraBackup) | 支持 (WAL-G) |
| **Validating Webhook** | **内置 9443 端口硬性准入拒绝** | 支持 | 支持 | 支持 |
| **多可用区拓扑打散** | **内置 TopologySpreadConstraints** | 手动配置 | 手动配置 | 支持 |
| **连接池与智能路由** | **内置 ProxySQL 2 节点网关** | 无 | 支持 | 支持 PgBouncer |
| **OLAP 分析型物理隔离** | **内置 Dedicated Replica Pool** | 无 | 手动标签 | 无 |
| **DBA Doctor 锁自治** | **支持 (自动查杀 MDL 阻塞源)** | 无 | 无 | 无 |
| **声明式 DDL (GitOps)** | **MatrixDBSchemaMigration CRD** | 无 | 无 | 无 |
| **在线存储热扩容** | **支持 (PVC 在线热扩容 10G-&gt;20G+)** | 支持 | 支持 | 支持 |

---

## 🚀 快速入门 (Quick Start)

### 1. 环境准备
- Kubernetes 集群 v1.28+ (K3s, Kind, Minikube, EKS, ACK, GKE)
- `kubectl` 命令行工具
- 具备默认 StorageClass (如 `local-path`, `standard`, 云盘 CSI)

### 2. 安装 CRD 与部署控制器
```bash
# 克隆仓库
git clone https://github.com/Zara1024/MatrixDB-Operator.git
cd MatrixDB-Operator

# 注册生产级 CRD
kubectl apply -f config/crd/bases/

# 部署 Operator 与 Webhook 准入服务
kubectl apply -f config/rbac/
kubectl apply -f config/manager/manager.yaml
```

检查 Operator 运行状态：
```bash
kubectl get pods -n matrixdb-operator-system
```

### 3. 一键部署生产级 3 节点高可用集群
```bash
kubectl apply -f examples/01-ha-cluster.yaml
```

查看集群就绪状态：
```bash
kubectl get mdb
```

终端输出示例：
```text
NAME            PHASE     PRIMARY           REPLICAS   READY   SYNCED   AGE
matrixdb-prod   Healthy   matrixdb-prod-0   3          3       2        2m
```

### 4. 连接主库与从库
提取自动生成的强加密 Root 密码：
```bash
PASSWORD=$(kubectl get secret matrixdb-prod-secret -o jsonpath="{.data.root-password}" | base64 -d)
echo "MySQL Root Password: $PASSWORD"
```

**写流量入口**（命中当前主库）：
```bash
kubectl run mysql-client --rm -it --image=mysql:8.0.36 -- \
  mysql -h matrixdb-prod-primary.default.svc -uroot -p$PASSWORD
```

**读流量入口**（在各只读从库间自动负载均衡）：
```bash
kubectl run mysql-client --rm -it --image=mysql:8.0.36 -- \
  mysql -h matrixdb-prod-replica.default.svc -uroot -p$PASSWORD
```

---

## 📁 开箱即用示例清单 (Examples)

在 [examples/](examples/) 目录中提供了涵盖全生命周期的完整配置 YAML：
- [`01-ha-cluster.yaml`](examples/01-ha-cluster.yaml): 3 节点金融级 HA 集群（无损半同步 + S3 Binlog 连续归档 + DBA Doctor 锁查杀）。
- [`02-cluster-proxysql.yaml`](examples/02-cluster-proxysql.yaml): 集成 ProxySQL 读写分流网关集群。
- [`03-cluster-analytics.yaml`](examples/03-cluster-analytics.yaml): 开启专属分析型从库 Dedicated Pool 隔离 OLAP 负载。
- [`04-backup-s3.yaml`](examples/04-backup-s3.yaml): 一键远程全量备份直传 S3 / MinIO。
- [`05-scheduled-backup.yaml`](examples/05-scheduled-backup.yaml): 生产级 Cron 定时备份与保留生命周期轮转。
- [`06-restore-full.yaml`](examples/06-restore-full.yaml): 从 S3 基础快照全量灾难恢复。
- [`07-restore-pitr.yaml`](examples/07-restore-pitr.yaml): PITR 任意时间点精准数据抢救恢复。
- [`08-schema-migration.yaml`](examples/08-schema-migration.yaml): 声明式 DDL Schema 版本化迁移执行。

---

## 🧪 真实物理机生产验证测试

MatrixDB Operator v3.0.0 在真实的云原生宿主机（`Ubuntu 22.04 LTS`, `K3s v1.36.5`）上，**完全抹除旧持久卷 PVC、完全从 0 状态拉起**，完成了全套严苛验证：
- **12 大平台级生产维度**
- **26 项端到端严苛自动化用例**
- **100% 通过（Exit Code 0，无任何失败）**

详细实测指标、5000+ QPS 暴力 Kill 容灾日志、GTID 轨迹与 PITR 过程详见：[`docs/verification-report.md`](docs/verification-report.md)。

---

## 📦 Helm Chart 交付安装

使用官方发布 Helm Chart 一键交付：
```bash
helm install matrixdb-operator charts/matrixdb-operator/ \
  --namespace matrixdb-operator-system \
  --create-namespace
```

包含组件：
- Operator 控制器与准入 Webhook (:9443)
- 开箱即用 Grafana 仪表盘 ConfigMap (`matrixdb-overview.json`)
- PrometheusRule 生产告警规则（高延迟、主从脱钩、半同步降级、实例宕机）

---

## 📄 开源许可证 (License)

本项目遵循 [Apache License 2.0](LICENSE) 开源许可证。
