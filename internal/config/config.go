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
	PushDebounceSec int
}

func Load() *Config {
	err := godotenv.Load()
	if err != nil {
		panic(err)
	}
	return &Config{
		APIToken:        os.Getenv("API_TOKEN"),
		DBHost:          os.Getenv("DATABASE_HOST"),
		DBUser:          os.Getenv("DATABASE_USER"),
		DBPass:          os.Getenv("DATABASE_PASS"),
		DBName:          os.Getenv("DATABASE_NAME"),
		DBPort:          mustInt(os.Getenv("DATABASE_PORT"), 5432),
		MQTTHost:        os.Getenv("MQTT_HOST"),
		MQTTPort:        mustInt(os.Getenv("MQTT_PORT"), 1883),
		MQTTUser:        os.Getenv("MQTT_USER"),
		MQTTPass:        os.Getenv("MQTT_PASS"),
		CarID:           mustInt(os.Getenv("CAR_ID"), 1),
		LogLevel:        getEnv("LOG_LEVEL", "info"),
		PushDebounceSec: mustInt(os.Getenv("PUSH_DEBOUNCE_SECONDS"), 15),
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
