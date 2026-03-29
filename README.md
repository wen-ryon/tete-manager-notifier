# tete-manager-notifier

为 Teslamate 自建用户打造的轻量通知工具。通过 MQTT 监听车辆状态，结合数据库查询，在行程结束时自动推送美观格式的通知到「特特管家」iOS App。

## 功能特性

- 行程结束、充电结束、哨兵模式状态变更自动推送通知
- 包含行驶时间、距离、电量变化等详细信息
- 集成到现有的 Teslamate docker-compose 环境中
- 轻量级，资源占用低
- 支持多平台（amd64、arm64）

## Docker 部署

### 前提条件

- 已运行 Teslamate（包含 database 和 mosquitto 服务）
- Docker 和 Docker Compose 已安装

### 安装步骤

1. **编辑你的 `docker-compose.yml` 文件**，在 `services` 部分添加以下内容（参考 `docker-compose.yml.example`）：

   ```yaml
   services:
     # ... 现有的 Teslamate 服务 ...
     
     tete-notifier-1:
       image: wenryon/tete-notifier:latest
       restart: always
       environment:
         - API_TOKEN=你的特特管家完整的API地址
         - DATABASE_HOST=database
         - DATABASE_USER=teslamate
         - DATABASE_PASS=你的数据库密码
         - DATABASE_NAME=teslamate
         - MQTT_HOST=mosquitto
         - CAR_ID=1
         - PUSH_DEBOUNCE_SECONDS=5
       depends_on:
         - database
         - mosquitto
   ```

2. **配置说明**

   | 环境变量 | 说明 | 必填 | 默认值 |
   |---------|------|------|--------|
   | API_TOKEN | 特特管家的推送 API 地址 | 是 | - |
   | DATABASE_HOST | 数据库主机名 | 否 | database |
   | DATABASE_USER | 数据库用户名 | 否 | teslamate |
   | DATABASE_PASS | 数据库密码 | 是 | - |
   | DATABASE_NAME | 数据库名称 | 否 | teslamate |
   | MQTT_HOST | MQTT 主机名 | 否 | mosquitto |
   | CAR_ID | 车辆 ID | 否 | 1 |
   | PUSH_DEBOUNCE_SECONDS | 推送防抖动初始时间（秒） | 否 | 5 |

3. **启动服务**

   如果你已经有正在运行的容器，使用以下命令（只会更新有变化的容器）：

   ```bash
   docker compose up -d
   ```

   Docker Compose 会自动检测变化，只重新创建新增的 `tete-notifier-1` 容器，不会影响其他正在运行的容器。

4. **查看日志**

   ```bash
   docker compose logs -f tete-notifier-1
   ```

## 获取 API_TOKEN

1. 在「特特管家」iOS App 中获取推送 API
2. 将 API 填入 `API_TOKEN` 环境变量

## 版本说明

- `v1.0.0` - 初始版本，支持行程结束、充电结束、哨兵模式状态变更自动通知

## 技术栈

- Go 语言
- GORM（数据库 ORM）
- paho.mqtt.golang（MQTT 客户端）

## 本地开发

```bash
# 克隆项目
git clone https://github.com/wen-ryon/tete-manager-notifier.git

# 配置环境变量
cp .env.example .env
# 编辑 .env 文件

# 运行
go run ./cmd/app
```

## License

MIT License
