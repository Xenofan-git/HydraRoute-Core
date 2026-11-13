package xrayapi

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"hydraroute.local/core/internal/xrayapi/command"
)

const DefaultAddress = "127.0.0.1:16285"

type OutboundStatus struct {
	Tag           string `json:"tag"`
	Alive         bool   `json:"alive"`
	DelayMS       int64  `json:"delay_ms"`
	LastError     string `json:"last_error,omitempty"`
	LastTryTime   int64  `json:"last_try_time"`
	LastSeenTime  int64  `json:"last_seen_time"`
	HealthAverage int64  `json:"health_average_ms,omitempty"`
	HealthFail    int64  `json:"health_fail,omitempty"`
	Stale         bool   `json:"stale,omitempty"`
}

func GetOutboundStatus(ctx context.Context, address string) ([]OutboundStatus, error) {
	if address == "" {
		address = DefaultAddress
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("connect to Xray Observatory API: %w", err)
	}
	defer conn.Close()
	resp, err := command.NewObservatoryServiceClient(conn).GetOutboundStatus(ctx, &command.GetOutboundStatusRequest{})
	if err != nil {
		return nil, fmt.Errorf("read Xray Observatory status: %w", err)
	}
	out := make([]OutboundStatus, 0, len(resp.GetStatus().GetStatus()))
	for _, item := range resp.GetStatus().GetStatus() {
		if item == nil || item.GetOutboundTag() == "" {
			continue
		}
		v := OutboundStatus{Tag: item.GetOutboundTag(), Alive: item.GetAlive(), DelayMS: item.GetDelay(), LastError: item.GetLastErrorReason(), LastTryTime: item.GetLastTryTime(), LastSeenTime: item.GetLastSeenTime()}
		if hp := item.GetHealthPing(); hp != nil {
			v.HealthAverage, v.HealthFail = hp.GetAverage(), hp.GetFail()
		}
		out = append(out, v)
	}
	return out, nil
}
