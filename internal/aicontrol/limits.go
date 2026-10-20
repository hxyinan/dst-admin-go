package aicontrol

import (
	"context"
	"encoding/json"
	"time"
)

// Zero explicitly disables that limit; absent settings retain existing defaults.
type ChatLimits struct {
	MaxRounds      int `json:"maxRounds"`
	MaxToolCalls   int `json:"maxToolCalls"`
	TimeoutSeconds int `json:"timeoutSeconds"`
}

func defaultChatLimits() ChatLimits {
	return ChatLimits{MaxRounds: 8, MaxToolCalls: 12, TimeoutSeconds: 150}
}

func (limits *ChatLimits) UnmarshalJSON(raw []byte) error {
	type plain ChatLimits
	value := plain(defaultChatLimits())
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*limits = ChatLimits(value)
	return nil
}

func (config Config) chatLimits() ChatLimits {
	if config.Limits == nil {
		return defaultChatLimits()
	}
	return *config.Limits
}

func (limits ChatLimits) valid() bool {
	// Prevent duration overflow without imposing an arbitrary product ceiling.
	return limits.MaxRounds >= 0 && limits.MaxToolCalls >= 0 && limits.TimeoutSeconds >= 0 &&
		int64(limits.TimeoutSeconds) <= int64((1<<63-1)/time.Second)
}

func chatContext(parent context.Context, limits ChatLimits) (context.Context, context.CancelFunc) {
	if limits.TimeoutSeconds > 0 {
		return context.WithTimeout(parent, time.Duration(limits.TimeoutSeconds)*time.Second)
	}
	return context.WithCancel(parent)
}
