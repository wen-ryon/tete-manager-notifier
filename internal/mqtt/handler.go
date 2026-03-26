package mqtt

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/wen-ryon/tete-manager-notifier/internal/config"
	"github.com/wen-ryon/tete-manager-notifier/internal/db"
	"github.com/wen-ryon/tete-manager-notifier/internal/notifier"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type Client struct {
	cfg           *config.Config
	client        mqtt.Client
	carName       string
	lastDriveID   uint
	lastChargeID  uint
	lastSentry    bool
	mu            sync.Mutex
	debounceTimer *time.Timer
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg:          cfg,
		lastDriveID:  0,
		lastChargeID: 0,
		lastSentry:   false,
	}
}

func (c *Client) Connect() error {
	broker := fmt.Sprintf("tcp://%s:%d", c.cfg.MQTTHost, c.cfg.MQTTPort)

	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetUsername(c.cfg.MQTTUser).
		SetPassword(c.cfg.MQTTPass).
		SetClientID(fmt.Sprintf("tete-notifier-%d", c.cfg.CarID)).
		SetAutoReconnect(true).
		SetKeepAlive(2 * time.Minute).
		SetCleanSession(true)

	opts.OnConnect = func(client mqtt.Client) {
		log.Println("✅ MQTT 已连接")
		// 加载车辆名称
		name, err := db.GetCarName(c.cfg.CarID)
		if err == nil && name != "" {
			c.carName = name
		} else {
			c.carName = "我的 Tesla"
		}
		log.Printf("✅ 车辆名称: %s", c.carName)
		topic := fmt.Sprintf("teslamate/cars/%d/#", c.cfg.CarID)
		if token := client.Subscribe(topic, 0, c.messageHandler); token.Wait() && token.Error() != nil {
			log.Printf("❌ 订阅失败: %v", token.Error())
		} else {
			log.Printf("✅ 已订阅 topic: %s", topic)
		}
	}

	c.client = mqtt.NewClient(opts)
	token := c.client.Connect()
	if token.Wait() && token.Error() != nil {
		return token.Error()
	}
	return nil
}

func (c *Client) Disconnect() {
	if c.client != nil && c.client.IsConnected() {
		c.client.Disconnect(250)
	}
}

func (c *Client) StartHandler() {
	// 已通过 OnConnect 处理订阅
}

func (c *Client) messageHandler(client mqtt.Client, msg mqtt.Message) {
	topic := msg.Topic()
	payload := string(msg.Payload())

	switch {
	case strings.HasSuffix(topic, "/shift_state"):
		c.handleShiftState(payload)
	case strings.HasSuffix(topic, "/charging_state") || strings.HasSuffix(topic, "/charge"):
		c.handleChargingState(payload)
	case strings.HasSuffix(topic, "/sentry_mode"):
		c.handleSentryMode(payload)
	}
}

// ==================== 行程通知 ====================
func (c *Client) handleShiftState(payload string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if payload != "P" {
		if c.debounceTimer != nil {
			c.debounceTimer.Stop()
		}
		return
	}

	if c.debounceTimer != nil {
		c.debounceTimer.Stop()
	}

	c.debounceTimer = time.AfterFunc(time.Duration(c.cfg.PushDebounceSec)*time.Second, c.processTripEnd)
}

// 处理行程结束：查询最新 Drive 记录并推送
func (c *Client) processTripEnd() {
	c.mu.Lock()
	defer c.mu.Unlock()

	result, err := db.GetLatestDrive(c.cfg.CarID)
	if err != nil || result == nil {
		return
	}

	drive := &result.Drive
	if drive.ID == c.lastDriveID || drive.ID == 0 {
		return
	}

	c.lastDriveID = drive.ID

	socUsed := result.StartSOC - result.EndSOC
	rangeReduced := drive.StartIdealRangeKM - drive.EndIdealRangeKM
	achieveRate := 0.0
	if rangeReduced > 0 {
		achieveRate = (drive.Distance / rangeReduced) * 100
	}

	content := fmt.Sprintf(`时间: %s | 耗时: %d分 | 距离: %.1f km
电量: %.0f%%→%.0f%% | 消耗: %.1f%%
减少: %.1f km | 达成率: %.1f%%`,
		drive.EndDate.Local().Format("15:04"),
		drive.DurationMin,
		drive.Distance,
		result.StartSOC, result.EndSOC, socUsed,
		rangeReduced, achieveRate,
	)

	title := fmt.Sprintf("🚗 %s 行程通知📍", c.carName)

	notifier.SendNotification(c.cfg.APIToken, title, content)
	log.Printf("✅ 行程通知已推送 (ID: %d)", drive.ID)
}

// ==================== 充电通知 ====================
func (c *Client) handleChargingState(payload string) {
	// 检测充电结束（Complete / Disconnected）
	if strings.Contains(payload, "Complete") || strings.Contains(payload, "Disconnected") {
		c.processChargeEnd()
	}
}

// ==================== 充电通知 ====================
func (c *Client) processChargeEnd() {
	c.mu.Lock()
	defer c.mu.Unlock()

	charge, err := db.GetLatestCharge(c.cfg.CarID)
	if err != nil || charge == nil || charge.ID == c.lastChargeID || charge.ID == 0 {
		return
	}

	c.lastChargeID = charge.ID

	// ==================== 判断充电类型（快充 / 慢充） ====================
	chargeType := "慢充 (AC)"

	if charge.FastChargerPresent != nil && *charge.FastChargerPresent {
		chargeType = "快充 (DC)"
	} else if charge.ChargerPhases != nil && *charge.ChargerPhases == 0 {
		chargeType = "快充 (DC)"
	} else if charge.ChargerPower > 30 { // 功率辅助判断（单位 kW）
		chargeType = "快充 (DC)"
	}

	socStart := float64(charge.StartBatteryLevel)
	socEnd := float64(charge.EndBatteryLevel)
	energyAdded := charge.ChargeEnergyAdded
	rangeAdded := charge.EndIdealRangeKM - charge.StartIdealRangeKM

	content := fmt.Sprintf(`时间: %s | 类型: %s
充入: %.1f kWh | 电量: %.0f%%→%.0f%%
增加: %.1f km | 耗时: %d分`,
		charge.EndDate.Local().Format("15:04"),
		chargeType,
		energyAdded,
		socStart, socEnd,
		rangeAdded,
		charge.DurationMin,
	)

	title := fmt.Sprintf("🚗 %s 充电通知🔋", c.carName)

	if err := notifier.SendNotification(c.cfg.APIToken, title, content); err != nil {
		log.Printf("❌ 充电通知推送失败: %v", err)
	} else {
		log.Printf("✅ 充电通知推送成功！类型: %s，充入 %.1f kWh", chargeType, energyAdded)
	}
}

// ==================== 哨兵模式通知 ====================
func (c *Client) handleSentryMode(payload string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	isOn := strings.Contains(payload, "true") || strings.Contains(payload, "on") || strings.Contains(payload, "armed")

	if isOn && !c.lastSentry {
		title := fmt.Sprintf("🚗 %s 哨兵通知🚨", c.carName)
		content := "🛑 已开启全方位扫描，守护车辆安全中..."
		notifier.SendNotification(c.cfg.APIToken, title, content)
		log.Println("✅ 哨兵开启通知已推送")
	} else if !isOn && c.lastSentry {
		title := fmt.Sprintf("🚗 %s 哨兵通知🚨", c.carName)
		content := "⭕️ 已关闭全方位扫描，节省电量中..."
		notifier.SendNotification(c.cfg.APIToken, title, content)
		log.Println("✅ 哨兵关闭通知已推送")
	}

	c.lastSentry = isOn
}
