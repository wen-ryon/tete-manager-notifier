package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/wen-ryon/tete-manager-notifier/internal/config"
	"github.com/wen-ryon/tete-manager-notifier/internal/db"
	"github.com/wen-ryon/tete-manager-notifier/internal/mqtt"
)

func main() {
	cfg := config.Load()

	// 初始化日志
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("🚀 特特管家通知器启动 | CarID: %d", cfg.CarID)

	// 初始化数据库
	if err := db.Init(cfg); err != nil {
		log.Fatalf("❌ 数据库连接失败: %v", err)
	}
	log.Println("✅ 数据库连接成功")

	// 初始化 MQTT 客户端并启动订阅
	mqttClient := mqtt.NewClient(cfg)
	if err := mqttClient.Connect(); err != nil {
		log.Fatalf("❌ MQTT 连接失败: %v", err)
	}

	// 启动 MQTT 消息处理
	mqttClient.StartHandler()

	log.Println("✅ MQTT 订阅已启动，等待车辆状态变化...")

	// 优雅退出
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("🛑 收到停止信号，正在优雅退出...")
	mqttClient.Disconnect()
	fmt.Println("👋 程序已退出")
}
