package models

import "time"

type Charge struct {
	ID                uint      `gorm:"primaryKey;column:id"`
	CarID             int16     `gorm:"column:car_id"`
	StartDate         time.Time `gorm:"column:start_date"`
	EndDate           time.Time `gorm:"column:end_date"`
	ChargeEnergyAdded float64   `gorm:"column:charge_energy_added"`
	StartBatteryLevel int16     `gorm:"column:start_battery_level"`
	EndBatteryLevel   int16     `gorm:"column:end_battery_level"`
	DurationMin       int16     `gorm:"column:duration_min"`
	StartIdealRangeKM float64   `gorm:"column:start_ideal_range_km"`
	EndIdealRangeKM   float64   `gorm:"column:end_ideal_range_km"`

	// 关联 charges 表的关键字段（用于判断快充/慢充）
	FastChargerPresent *bool  `gorm:"column:fast_charger_present"`
	ChargerPhases      *int16 `gorm:"column:charger_phases"`
	ChargerPower       int16  `gorm:"column:charger_power"`
}

func (Charge) TableName() string { return "charging_processes" }
