package models

type Car struct {
	ID   int16  `gorm:"column:id"`
	Name string `gorm:"column:name"`
}

func (Car) TableName() string { return "cars" }
