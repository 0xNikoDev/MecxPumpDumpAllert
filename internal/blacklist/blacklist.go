package blacklist

import (
	"strings"
	"sync"
	"time"
)

type Blacklist struct {
	mu        sync.Mutex
	tempItems map[string]time.Time
}

func NewBlacklist() *Blacklist {
	return &Blacklist{
		tempItems: make(map[string]time.Time),
	}
}

func (b *Blacklist) Add(symbol string, duration time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	symbol = strings.ToLower(symbol)
	b.tempItems[symbol] = time.Now().Add(duration)
}

func (b *Blacklist) IsBlacklisted(symbol string) bool {
	symbol = strings.ToLower(symbol)
	b.mu.Lock()
	defer b.mu.Unlock()

	expire, exists := b.tempItems[symbol]
	if !exists || time.Now().After(expire) {
		delete(b.tempItems, symbol)
		return false
	}
	return true
}

func (b *Blacklist) ListBlacklist() (temporary map[string]time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	temporary = make(map[string]time.Time)
	for symbol, expire := range b.tempItems {
		if time.Now().After(expire) {
			delete(b.tempItems, symbol)
		} else {
			temporary[symbol] = expire
		}
	}

	return temporary
}
