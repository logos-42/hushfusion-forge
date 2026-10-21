package orchestration

import (
	"fmt"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/bolloon"
	"github.com/logos-42/hushfusion-forge/internal/plant"
	"github.com/logos-42/hushfusion-forge/internal/safety"
)

// Agent 多智能体设计代理，通过 BOLLOON 进行通信和任务协调。
type Agent struct {
	ID       string
	Client   *bolloon.Client
	Device   *plant.Device
	Policy   safety.Policy
	DesignID string
}

// NewAgent 造一个设计代理。
func NewAgent(id string, deviceSpec plant.Spec, policy safety.Policy, bolloonBin string) (*Agent, error) {
	dev := plant.New(deviceSpec, time.Now())
	client := bolloon.NewWithBin(bolloonBin)
	if client == nil {
		return nil, fmt.Errorf("bolloon client returned nil for bin %s", bolloonBin)
	}
	return &Agent{
		ID:       id,
		Client:   client,
		Device:   dev,
		Policy:   policy,
		DesignID: id + "-",
	}, nil
}
