package models

import "time"

type Drive struct {
	ID                uint      `gorm:"primaryKey;column:id"`
	CarID             int16     `gorm:"column:car_id"`
	StartDate         time.Time `gorm:"column:start_date"`
	EndDate           time.Time `gorm:"column:end_date"`
	Distance          float64   `gorm:"column:distance"`
	DurationMin       int16     `gorm:"column:duration_min"`
	StartIdealRangeKM float64   `gorm:"column:start_ideal_range_km"`
	EndIdealRangeKM   float64   `gorm:"column:end_ideal_range_km"`
	StartPositionID   int32     `gorm:"column:start_position_id"`
	EndPositionID     int32     `gorm:"column:end_position_id"`
}

func (Drive) TableName() string { return "drives" }
