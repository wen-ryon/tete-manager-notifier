package mqtt

import (
	"fmt"
	"log"
	"runtime/debug"
	"strconv"
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
	cfg                *config.Config
	client             mqtt.Client
	carName            string
	lastDriveID        uint
	lastChargeID       uint
	lastShiftState     string
	lastUserPresent    bool
	lastChargingState  string
	lastBatteryLevel   float64
	lastIdealRangeKM   float64
	lastDriverDoorOpen bool
	lastLocked         bool
	lastState          string
	chargeLimitSoc     int

	mu               sync.Mutex
	debounceTimer    *time.Timer
	stateSettleTimer *time.Timer

	retryCountDrive  int
	retryCountCharge int
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg:               cfg,
		lastDriveID:       0,
		lastChargeID:      0,
		lastUserPresent:   true,
		lastChargingState: "",
		lastBatteryLevel:  0,
		lastIdealRangeKM:  0,
		lastState:         "",
	}
}

func (c *Client) Connect() error {
	broker := fmt.Sprintf("tcp://%s:%d", c.cfg.MQTTHost, c.cfg.MQTTPort)

	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetUsername(c.cfg.MQTTUser).
		SetPassword(c.cfg.MQTTPass).
		SetClientID(fmt.Sprintf("tete-notifier-%d-%d", c.cfg.CarID, time.Now().Unix())).
		SetAutoReconnect(true).
		SetKeepAlive(2 * time.Minute).
		SetCleanSession(true).
		SetConnectRetry(true)

	opts.OnConnect = func(client mqtt.Client) {
		log.Println("✅ MQTT 已连接")
		name, err := db.GetCarName(c.cfg.CarID)
		if err == nil && name != "" {
			c.carName = name
		} else {
			c.carName = "我的 Tesla"
		}

		base := fmt.Sprintf("teslamate/cars/%d", c.cfg.CarID)
		topics := []string{
			base + "/state",
			base + "/shift_state",
			base + "/is_user_present",
			base + "/charging_state",
			base + "/battery_level",
			base + "/ideal_battery_range_km",
			base + "/driver_front_door_open",
			base + "/locked",
			base + "/charge_limit_soc",
		}

		for _, t := range topics {
			if token := client.Subscribe(t, 0, c.messageHandler); token.Wait() && token.Error() != nil {
				log.Printf("❌ 订阅失败 %s: %v", t, token.Error())
			}
		}

		// 发送服务启动通知
		title := "🔔 推送服务通知"
		content := fmt.Sprintf("🚗 %s 通知推送服务已开启 ✅", c.carName)
		if c.cfg.APIToken != "" {
			go func() {
				if err := notifier.SendNotification(c.cfg.APIToken, title, content); err != nil {
					log.Printf("❌ 服务启动通知推送失败: %v", err)
				} else {
					log.Printf("✅ 服务启动通知已推送")
				}
			}()
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

	// 是否需要触发业务逻辑检查的标记
	isDrive := false

	switch {
	case strings.HasSuffix(topic, "/state"):
		c.mu.Lock()
		if c.lastState != payload {
			c.lastState = payload
			isDrive = true
		}
		c.mu.Unlock()

	case strings.HasSuffix(topic, "/shift_state"):
		c.mu.Lock()
		if c.lastShiftState != payload {
			c.lastShiftState = payload
			isDrive = true
		}
		c.mu.Unlock()

	case strings.HasSuffix(topic, "/charging_state"):
		isCharge := false
		c.mu.Lock()
		if c.lastChargingState != payload {
			c.lastChargingState = payload
			isCharge = true
		}
		c.mu.Unlock()
		if isCharge {
			go c.checkChargingCondition()
		}

	case strings.HasSuffix(topic, "/is_user_present"):
		c.mu.Lock()
		val := payload == "true"
		if c.lastUserPresent != val {
			c.lastUserPresent = val
			isDrive = true
		}
		c.mu.Unlock()

	case strings.HasSuffix(topic, "/driver_front_door_open"):
		c.mu.Lock()
		val := payload == "true"
		if c.lastDriverDoorOpen != val {
			c.lastDriverDoorOpen = val
			isDrive = true
		}
		c.mu.Unlock()

	case strings.HasSuffix(topic, "/locked"):
		c.mu.Lock()
		val := payload == "true"
		if c.lastLocked != val {
			c.lastLocked = val
			isDrive = true
		}
		c.mu.Unlock()

	// 以下是非关键状态，只更新内存值，不触发 scheduleStateSettle
	case strings.HasSuffix(topic, "/battery_level"):
		if val, err := strconv.ParseFloat(payload, 64); err == nil {
			c.mu.Lock()
			c.lastBatteryLevel = val
			c.mu.Unlock()
		}
	case strings.HasSuffix(topic, "/ideal_battery_range_km"):
		if val, err := strconv.ParseFloat(payload, 64); err == nil {
			c.mu.Lock()
			c.lastIdealRangeKM = val
			c.mu.Unlock()
		}
	case strings.HasSuffix(topic, "/charge_limit_soc"):
		if val, err := strconv.Atoi(payload); err == nil {
			c.mu.Lock()
			c.chargeLimitSoc = val
			c.mu.Unlock()
		}
	}

	// 只有关键状态变化，才启动/重置沉淀计时器
	if isDrive {
		c.scheduleStateSettle()
	}
}

// scheduleStateSettle 负责状态沉淀
func (c *Client) scheduleStateSettle() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stateSettleTimer != nil {
		c.stateSettleTimer.Stop()
	}

	c.stateSettleTimer = time.AfterFunc(1*time.Second, func() {
		// 检查行程是否结束
		c.checkTripEndCondition()
	})
}

func (c *Client) checkChargingCondition() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[充电检查] 崩溃: %v\n%s", r, string(debug.Stack()))
		}
	}()
	switch c.lastChargingState {
	case "Starting":
		c.processChargeStart()
	case "Stopped", "Complete", "Disconnected":
		c.processChargeEnd()
	}
}

func (c *Client) checkTripEndCondition() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 诊断日志：查看当前内存中的状态快照
	log.Printf("[行程检查] 当前状态: State=%s, Shift=%s, UserPresent=%v, Locked=%v",
		c.lastState, c.lastShiftState, c.lastUserPresent, c.lastLocked)

	isNotDriving := c.lastState == "online"
	isParked := c.lastShiftState == "P" || !c.lastUserPresent || c.lastDriverDoorOpen || !c.lastLocked

	if isNotDriving || isParked {
		if c.debounceTimer == nil {
			log.Printf("[行程检查] 满足结束条件，启动 %d 秒防抖计时...", c.cfg.PushDebounceSec)
			c.debounceTimer = time.AfterFunc(
				time.Duration(c.cfg.PushDebounceSec)*time.Second,
				c.processTripEnd,
			)
		}
	} else {
		if c.debounceTimer != nil {
			log.Println("[行程检查] 车辆重新开始行驶，取消防抖计时")
			c.debounceTimer.Stop()
			c.debounceTimer = nil
		}
	}
}

// 充电开始通知 - 加入了限值显示
func (c *Client) processChargeStart() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now().Local().Format("15:04")
	limitStr := ""
	if c.chargeLimitSoc > 0 {
		limitStr = fmt.Sprintf(" (限值: %d%%)", c.chargeLimitSoc)
	}

	content := fmt.Sprintf(`时间: %s
当前电量: %.0f%%%s
当前表显: %.1f km`,
		now, c.lastBatteryLevel, limitStr, c.lastIdealRangeKM,
	)

	title := fmt.Sprintf("🚗 %s 充电开始 🔌", c.carName)

	go func() {
		if err := notifier.SendNotification(c.cfg.APIToken, title, content); err != nil {
			log.Printf("❌ 充电开始推送失败: %v", err)
		} else {
			log.Println("✅ 充电开始通知已推送")
		}
	}()
}

func (c *Client) processChargeEnd() {
	c.mu.Lock()
	defer c.mu.Unlock()

	charge, err := db.GetLatestCharge(c.cfg.CarID)
	if err != nil {
		log.Printf("❌ [充电检查] 数据库查询失败: %v", err)
		return
	}

	if charge.ID == c.lastChargeID || charge.ID == 0 || charge.EndDate.IsZero() {
		log.Printf("⏳ [充电检查] 数据未就绪: 当前数据库ID=%d, 上次ID=%d, 结束时间=%v。准备进入重试...",
			charge.ID, c.lastChargeID, charge.EndDate)

		// 触发指数退避重试
		c.tryWithBackoff(&c.retryCountCharge, 3, c.cfg.PushDebounceSec, func() bool {
			ch, e := db.GetLatestCharge(c.cfg.CarID)
			if e != nil {
				log.Printf("❌ [充电重试] 查询失败: %v", e)
				return false
			}

			if ch.ID != c.lastChargeID && ch.ID != 0 && !ch.EndDate.IsZero() {
				log.Printf("✅ [充电重试] 成功捕获新数据! ID: %d", ch.ID)
				c.lastChargeID = ch.ID
				c.doChargeNotification(ch)
				return true
			}

			log.Printf("😴 [充电重试] 数据仍未更新，继续等待... (当前DB ID: %d)", ch.ID)
			return false
		}, "充电通知")
		return
	}

	// 如果直接查到了新 ID
	log.Printf("🚀 [充电检查] 直接捕获新数据: ID %d", charge.ID)
	c.lastChargeID = charge.ID
	c.doChargeNotification(charge)
}

// 充电结束通知 - 优化类型判断
func (c *Client) doChargeNotification(charge *models.Charge) {
	chargeType := c.getChargeType(charge)

	content := fmt.Sprintf(`时间: %s→%s | 历时: %s
充入: %.1f kWh | 类型: %s
表显: %.0f→%.0f km (+%.1f km)
电量: %.0f→%.0f%% (+%.1f%%)`,
		charge.StartDate.Local().Format("15:04"), charge.EndDate.Local().Format("15:04"), formatDuration(charge.DurationMin),
		charge.ChargeEnergyAdded, chargeType,
		charge.StartIdealRangeKM, charge.EndIdealRangeKM, charge.EndIdealRangeKM-charge.StartIdealRangeKM,
		float64(charge.StartBatteryLevel), float64(charge.EndBatteryLevel), float64(charge.EndBatteryLevel-charge.StartBatteryLevel),
	)

	title := fmt.Sprintf("🚗 %s 充电%s 🔋", c.carName, getChargeState(c.lastChargingState))

	go func() {
		if err := notifier.SendNotification(c.cfg.APIToken, title, content); err != nil {
			log.Printf("❌ 充电通知推送失败: %v", err)
		} else {
			log.Printf("✅ 充电通知已推送 (ID: %d)", charge.ID)
		}
	}()
}
func (c *Client) processTripEnd() {
	c.mu.Lock()
	defer c.mu.Unlock()

	result, err := db.GetLatestDrive(c.cfg.CarID)
	if err != nil {
		log.Printf("❌ [行程检查] 数据库查询失败: %v", err)
		return
	}

	drive := &result.Drive
	log.Printf("[行程检查] 数据库最新 ID: %d, 上次推送 ID: %d, 结束时间: %v",
		drive.ID, c.lastDriveID, drive.EndDate)

	if drive.ID == c.lastDriveID || drive.ID == 0 || drive.EndDate.IsZero() {
		log.Printf("⏳ [行程检查] 数据未就绪（可能数据库尚未更新），进入重试模式...")

		c.tryWithBackoff(&c.retryCountDrive, 3, c.cfg.PushDebounceSec, func() bool {
			res, e := db.GetLatestDrive(c.cfg.CarID)
			if e != nil {
				log.Printf("❌ [行程重试] 查询失败: %v", e)
				return false
			}

			// 重试成功的标准：ID 变了 且 已经有结束时间
			if res.Drive.ID != c.lastDriveID && res.Drive.ID != 0 && !res.Drive.EndDate.IsZero() {
				// 校验是否是无效短行程
				if res.Drive.Distance == 0 || res.Drive.DurationMin == 0 {
					log.Printf("⏭️ [行程重试] 捕获到新行程 ID %d，但属于无效短行程，跳过推送", res.Drive.ID)
					c.lastDriveID = res.Drive.ID // 标记已处理
					return true
				}

				log.Printf("✅ [行程重试] 成功捕获新行程数据! ID: %d", res.Drive.ID)
				c.lastDriveID = res.Drive.ID
				c.doTripNotification(res)
				return true
			}

			log.Printf("😴 [行程重试] 数据库 ID 仍为 %d，继续等待更新...", res.Drive.ID)
			return false
		}, "行程通知")
		return
	}

	// 如果运气好，第一次查就查到了新 ID
	if drive.Distance == 0 || drive.DurationMin == 0 {
		log.Printf("⏭️ [行程检查] 忽略无效短行程 (ID: %d)", drive.ID)
		c.lastDriveID = drive.ID
		return
	}

	log.Printf("🚀 [行程检查] 直接捕获到新行程: ID %d", drive.ID)
	c.lastDriveID = drive.ID
	c.doTripNotification(result)
}

// 指数退避
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
			*retryCount = 0
		} else if *retryCount < maxRetries {
			c.tryWithBackoff(retryCount, maxRetries, baseDelaySec, action, logPrefix)
		} else {
			*retryCount = 0
		}
	})
}

// 行程通知内容
func (c *Client) doTripNotification(result *db.DriveWithSOC) {
	drive := &result.Drive
	socUsed := result.StartSOC - result.EndSOC
	rangeReduced := drive.StartIdealRangeKM - drive.EndIdealRangeKM
	achieveRate := 0.0
	if rangeReduced > 0 {
		achieveRate = (drive.Distance / rangeReduced) * 100
	}
	avgSpeed := calculateAvgSpeed(drive.Distance, drive.DurationMin)

	content := fmt.Sprintf(`时间: %s→%s | 历时: %s
距离: %.1f km | 均速: %.1f km/h
表显: %.0f→%.0f km (-%.1f km)
电量: %.0f→%.0f%% (-%.1f%%) 达成率: %.1f%%`,
		drive.StartDate.Local().Format("15:04"), drive.EndDate.Local().Format("15:04"), formatDuration(drive.DurationMin),
		drive.Distance, avgSpeed,
		drive.StartIdealRangeKM, drive.EndIdealRangeKM, rangeReduced,
		result.StartSOC, result.EndSOC, socUsed, achieveRate,
	)

	title := fmt.Sprintf("🚗 %s 行程通知 📍", c.carName)

	go func() {
		if err := notifier.SendNotification(c.cfg.APIToken, title, content); err != nil {
			log.Printf("❌ 行程通知推送失败: %v", err)
		} else {
			log.Printf("✅ 行程通知已推送 (ID: %d)", drive.ID)
		}
	}()
}

// 优化后的快充判断逻辑
func (c *Client) getChargeType(charge *models.Charge) string {
	// 1. 检查 FastChargerPresent 标志位
	if charge.FastChargerPresent != nil && *charge.FastChargerPresent {
		return "快充 (DC)"
	}
	// 2. 检查相位，DC 充电通常显示为 0 或 null
	if charge.ChargerPhases != nil && *charge.ChargerPhases == 0 {
		return "快充 (DC)"
	}
	// 3. 功率兜底判断 (大于 30kW 基本为直流桩)
	if charge.ChargerPower > 30 {
		return "快充 (DC)"
	}
	return "慢充 (AC)"
}

func getChargeState(state string) string {
	if state == "Complete" {
		return "完成"
	}
	if state == "Disconnected" {
		return "断开"
	}
	if state == "Stopped" {
		return "停止"
	}
	return "结束"
}

func formatDuration(minutes int16) string {
	if minutes < 60 {
		return fmt.Sprintf("%dmin", minutes)
	}
	hours := minutes / 60
	mins := minutes % 60
	if mins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dmin", hours, mins)
}

func calculateAvgSpeed(distanceKm float64, durationMin int16) float64 {
	if durationMin == 0 {
		return 0
	}
	return distanceKm * 60 / float64(durationMin)
}
