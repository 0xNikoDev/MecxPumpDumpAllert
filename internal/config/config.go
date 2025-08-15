package config

import (
	"fmt"
	"sync"
)

type Config struct {
	IntervalSeconds int     // Время в секундах для проверки
	PriceChangePct  float64 // Процент изменения цены
	VolumeUSD       float64 // Минимальный объем в долларах
	mu              sync.RWMutex
}

func NewConfig(intervalSeconds int, priceChangePct, volumeUSD float64) *Config {
	return &Config{
		IntervalSeconds: intervalSeconds,
		PriceChangePct:  priceChangePct,
		VolumeUSD:       volumeUSD,
	}
}

func (c *Config) SetInterval(seconds int) error {
	if seconds <= 0 {
		return fmt.Errorf("интервал должен быть больше 0")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.IntervalSeconds = seconds
	return nil
}

func (c *Config) SetPriceChangePct(pct float64) error {
	if pct <= 0 {
		return fmt.Errorf("процент изменения должен быть больше 0")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.PriceChangePct = pct
	return nil
}

func (c *Config) SetVolumeUSD(volume float64) error {
	if volume <= 0 {
		return fmt.Errorf("объем должен быть больше 0")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.VolumeUSD = volume
	return nil
}

func (c *Config) GetConfig() (int, float64, float64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.IntervalSeconds, c.PriceChangePct, c.VolumeUSD
}
