package mqtt

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/wen-ryon/tete-manager-notifier/internal/config"
	"github.com/wen-ryon/tete-manager-notifier/internal/db"
	"github.com/wen-ryon/tete-manager-notifier/internal/models"
	"github.com/wen-ryon/tete-manager-notifier/internal/notifier"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type Client struct {
	cfg             *config.Config
	client          mqtt.Client
	carName         string
	lastDriveID     uint
	lastChargeID    uint
	lastShiftState  string
	lastSentry      bool
	lastUserPresent bool

	mu            sync.Mutex
	debounceTimer *time.Timer

	// 指数退避重试计数
	retryCountDrive  int
	retryCountCharge int
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg:             cfg,
		lastDriveID:     0,
		lastChargeID:    0,
		lastSentry:      false,
		lastUserPresent: true, // 初始假设人在车上，避免误触发
	}
}

func (c *Client) Connect() error {
	broker := fmt.Sprintf("tcp://%s:%d", c.cfg.MQTTHost, c.cfg.MQTTPort)

	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetUsername(c.cfg.MQTTUser).
		SetPassword(c.cfg.MQTTPass).
		SetClientID(fmt.Sprintf("tete-notifier-%d-%d", c.cfg.CarID, time.Now().Unix())). // 兼容多个客户端同时运行，避免相同的 ClientID 会导致两者互相踢下线
		SetAutoReconnect(true).
		SetKeepAlive(2 * time.Minute).
		SetCleanSession(true).
		SetConnectRetry(true)

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
	case strings.HasSuffix(topic, "/is_user_present"):
		c.handleIsUserPresent(payload)
	}
}

// ==================== 行程通知（P档 + 主驾下车 才触发 + 指数退避） ====================
func (c *Client) handleShiftState(payload string) {
	c.mu.Lock()
	c.lastShiftState = payload
	c.mu.Unlock()
	c.checkTripEndCondition()
}

func (c *Client) handleIsUserPresent(payload string) {
	c.mu.Lock()
	c.lastUserPresent = (payload == "true")
	c.mu.Unlock()
	c.checkTripEndCondition()
}

func (c *Client) checkTripEndCondition() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 只有「P档 + 驾驶员已离座」才启动 debounce
	if c.lastShiftState != "P" || c.lastUserPresent {
		if c.debounceTimer != nil {
			c.debounceTimer.Stop()
		}
		return
	}

	if c.debounceTimer != nil {
		c.debounceTimer.Stop()
	}
	c.debounceTimer = time.AfterFunc(
		time.Duration(c.cfg.PushDebounceSec)*time.Second,
		c.processTripEnd,
	)
}

// 带指数退避的重试执行器
func (c *Client) tryWithBackoff(retryCount *int, maxRetries int, baseDelaySec int, action func() bool, logPrefix string) {
	*retryCount++
	if *retryCount > maxRetries {
		log.Printf("⏹️ %s 重试超过 %d 次，放弃", logPrefix, maxRetries)
		*retryCount = 0
		return
	}

	delay := time.Duration(baseDelaySec*(1<<uint(*retryCount-1))) * time.Second
	log.Printf("🔄 %s 第 %d 次尝试，等待 %.0f 秒...", logPrefix, *retryCount, delay.Seconds())

	time.AfterFunc(delay, func() {
		if action() {
			*retryCount = 0 // 成功后重置
		} else if *retryCount < maxRetries {
			c.tryWithBackoff(retryCount, maxRetries, baseDelaySec, action, logPrefix)
		} else {
			*retryCount = 0
		}
	})
}

// 处理行程结束通知
func (c *Client) processTripEnd() {
	c.mu.Lock()
	defer c.mu.Unlock()

	result, err := db.GetLatestDrive(c.cfg.CarID)
	if err != nil || result == nil {
		return
	}

	drive := &result.Drive
	if drive.ID == c.lastDriveID || drive.ID == 0 || drive.EndDate.IsZero() {
		// 数据未就绪，进入指数退避重试
		c.tryWithBackoff(&c.retryCountDrive, 3, c.cfg.PushDebounceSec, func() bool {
			res, e := db.GetLatestDrive(c.cfg.CarID)
			if e != nil || res == nil {
				return false
			}

			d := &res.Drive
			if d.ID == c.lastDriveID || d.ID == 0 || d.EndDate.IsZero() {
				return false
			}

			// // 短行程过滤
			// if d.Distance < 0.5 || d.DurationMin < 3 {
			// 	c.lastDriveID = d.ID
			// 	log.Printf("⏭️ 忽略无效短行程 (ID: %d, 距离: %.1fkm, 时长: %d分)", d.ID, d.Distance, d.DurationMin)
			// 	return true
			// }

			// 数据完整 → 推送
			c.lastDriveID = d.ID
			c.doTripNotification(res) // 传入完整 DriveWithSOC
			return true
		}, "行程通知")
		return
	}

	// 第一次查询就完整的情况
	// if drive.Distance < 0.5 || drive.DurationMin < 3 {
	// 	c.lastDriveID = drive.ID
	// 	log.Printf("⏭️ 忽略无效短行程 (ID: %d, 距离: %.1fkm, 时长: %d分)", drive.ID, drive.Distance, drive.DurationMin)
	// 	return
	// }

	c.lastDriveID = drive.ID
	c.doTripNotification(result)
}

// 发送行程通知
func (c *Client) doTripNotification(result *db.DriveWithSOC) {
	drive := &result.Drive

	socUsed := result.StartSOC - result.EndSOC
	rangeReduced := drive.StartIdealRangeKM - drive.EndIdealRangeKM
	achieveRate := 0.0
	if rangeReduced > 0 {
		achieveRate = (drive.Distance / rangeReduced) * 100
	}

	content := fmt.Sprintf(`时间: %s | 耗时: %d分 | 距离: %.1f km
电量: %.0f%%→%.0f%% | 消耗: %.1f%%
表显减少: %.1f km | 达成率: %.1f%%`,
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

// ==================== 充电通知（同样加上指数退避） ====================
func (c *Client) handleChargingState(payload string) {
	if strings.Contains(payload, "Complete") || strings.Contains(payload, "Disconnected") {
		c.processChargeEnd()
	}
}

func (c *Client) processChargeEnd() {
	c.mu.Lock()
	defer c.mu.Unlock()

	charge, err := db.GetLatestCharge(c.cfg.CarID)
	if err != nil || charge == nil || charge.ID == c.lastChargeID || charge.ID == 0 {
		return
	}

	// 如果充电记录还没完全结束（EndDate 为空等），进入重试
	if charge.EndDate.IsZero() { // 根据你的实际结构体调整判断条件
		c.tryWithBackoff(&c.retryCountCharge, 3, c.cfg.PushDebounceSec, func() bool {
			ch, e := db.GetLatestCharge(c.cfg.CarID)
			if e != nil || ch == nil || ch.ID == c.lastChargeID || ch.EndDate.IsZero() {
				return false
			}
			c.lastChargeID = ch.ID
			c.doChargeNotification(ch)
			return true
		}, "充电通知")
		return
	}

	c.lastChargeID = charge.ID
	c.doChargeNotification(charge)
}

// 发送充电通知
func (c *Client) doChargeNotification(charge *models.Charge) { // 假设你的 db 类型
	chargeType := "慢充 (AC)"
	if charge.FastChargerPresent != nil && *charge.FastChargerPresent {
		chargeType = "快充 (DC)"
	} else if charge.ChargerPhases != nil && *charge.ChargerPhases == 0 {
		chargeType = "快充 (DC)"
	} else if charge.ChargerPower > 30 {
		chargeType = "快充 (DC)"
	}

	content := fmt.Sprintf(`时间: %s | 类型: %s
充入: %.1f kWh | 电量: %.0f%%→%.0f%%
表显增加: %.1f km | 耗时: %d分`,
		charge.EndDate.Local().Format("15:04"),
		chargeType,
		charge.ChargeEnergyAdded,
		float64(charge.StartBatteryLevel), float64(charge.EndBatteryLevel),
		charge.EndIdealRangeKM-charge.StartIdealRangeKM,
		charge.DurationMin,
	)

	title := fmt.Sprintf("🚗 %s 充电通知🔋", c.carName)

	if err := notifier.SendNotification(c.cfg.APIToken, title, content); err != nil {
		log.Printf("❌ 充电通知推送失败: %v", err)
	} else {
		log.Printf("✅ 充电通知推送成功！类型: %s，充入 %.1f kWh", chargeType, charge.ChargeEnergyAdded)
	}
}

// ==================== 哨兵模式通知（只在 P 档时触发） ====================
func (c *Client) handleSentryMode(payload string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	isOn := isSentryOn(payload)

	// 只要不是 P 档（例如挂 D 档），就只更新状态，不推送任何通知
	if c.lastShiftState != "P" {
		c.lastSentry = isOn
		return
	}

	// 以下只有在 P 档时才会推送
	if isOn && !c.lastSentry {
		title := fmt.Sprintf("🚗 %s 哨兵通知🚨", c.carName)
		content := "🛑 已开启全方位扫描，守护车辆安全中..."
		notifier.SendNotification(c.cfg.APIToken, title, content)
		log.Println("✅ 哨兵开启通知已推送")
	}

	if !isOn && c.lastSentry {
		title := fmt.Sprintf("🚗 %s 哨兵通知🚨", c.carName)
		content := "⭕️ 已关闭全方位扫描，节省电量中..."
		notifier.SendNotification(c.cfg.APIToken, title, content)
		log.Println("✅ 哨兵关闭通知已推送")
	}

	c.lastSentry = isOn
}

func isSentryOn(payload string) bool {
	lower := strings.ToLower(payload)
	return strings.Contains(lower, "true") ||
		strings.Contains(lower, "on") ||
		strings.Contains(lower, "armed")
}
