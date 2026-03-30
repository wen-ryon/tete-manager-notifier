package config

import (
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	APIToken        string
	DBHost          string
	DBUser          string
	DBPass          string
	DBName          string
	DBPort          int
	MQTTHost        string
	MQTTPort        int
	MQTTUser        string
	MQTTPass        string
	CarID           int
	LogLevel        string
	PushDebounceSec int // 推送防抖初始时间，后续会进行3次指数退避重试，按(次数-1)倍增加
}

func Load() *Config {
	_ = godotenv.Load()

	return &Config{
		APIToken:        os.Getenv("API_TOKEN"),
		DBHost:          getEnv("DATABASE_HOST", "database"),
		DBUser:          getEnv("DATABASE_USER", "teslamate"),
		DBPass:          os.Getenv("DATABASE_PASS"),
		DBName:          getEnv("DATABASE_NAME", "teslamate"),
		DBPort:          mustInt(os.Getenv("DATABASE_PORT"), 5432),
		MQTTHost:        getEnv("MQTT_HOST", "mosquitto"),
		MQTTPort:        mustInt(os.Getenv("MQTT_PORT"), 1883),
		MQTTUser:        os.Getenv("MQTT_USER"),
		MQTTPass:        os.Getenv("MQTT_PASS"),
		CarID:           mustInt(os.Getenv("CAR_ID"), 1),
		LogLevel:        getEnv("LOG_LEVEL", "info"),
		PushDebounceSec: mustInt(os.Getenv("PUSH_DEBOUNCE_SECONDS"), 5),
	}
}

func mustInt(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
