//go:build test

package leader

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/pokt-network/pocket-relay-miner/logging"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTestLeaderElector(t *testing.T, instanceID string) (*GlobalLeaderElector, *miniredis.Miniredis) {
	t.Helper()

	mr, err := miniredis.Run()
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	logger := logging.NewLoggerFromConfig(logging.DefaultConfig())()

	elector := NewGlobalLeaderElector(logger, client, instanceID)

	t.Cleanup(func() {
		elector.Close()
		client.Close()
		mr.Close()
	})

	return elector, mr
}

func TestGlobalLeaderElector_SingleInstance(t *testing.T) {
	elector, _ := newTestLeaderElector(t, "instance-1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start election
	err := elector.Start(ctx)
	require.NoError(t, err)

	// Should become leader quickly
	require.Eventually(t, func() bool {
		return elector.IsLeader()
	}, 2*time.Second, 100*time.Millisecond, "instance should become leader")
}

func TestGlobalLeaderElector_TwoInstances(t *testing.T) {
	// Create shared miniredis
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client1 := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client2 := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client1.Close()
	defer client2.Close()

	logger := logging.NewLoggerFromConfig(logging.DefaultConfig())()

	elector1 := NewGlobalLeaderElector(logger, client1, "instance-1")
	elector2 := NewGlobalLeaderElector(logger, client2, "instance-2")
	defer elector1.Close()
	defer elector2.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start both
	require.NoError(t, elector1.Start(ctx))
	require.NoError(t, elector2.Start(ctx))

	// Wait for one to become leader
	time.Sleep(200 * time.Millisecond)

	// Exactly one should be leader
	isLeader1 := elector1.IsLeader()
	isLeader2 := elector2.IsLeader()
	require.True(t, isLeader1 != isLeader2, "exactly one should be leader")
}

func TestGlobalLeaderElector_LeadershipTransfer(t *testing.T) {
	// Create shared miniredis
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client1 := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client2 := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client1.Close()
	defer client2.Close()

	logger := logging.NewLoggerFromConfig(logging.DefaultConfig())()

	elector1 := NewGlobalLeaderElector(logger, client1, "instance-1")
	elector2 := NewGlobalLeaderElector(logger, client2, "instance-2")
	defer elector2.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start first elector
	require.NoError(t, elector1.Start(ctx))

	// Should become leader
	require.Eventually(t, func() bool {
		return elector1.IsLeader()
	}, 2*time.Second, 100*time.Millisecond)

	// Start second elector
	require.NoError(t, elector2.Start(ctx))

	// Second should NOT be leader
	time.Sleep(200 * time.Millisecond)
	require.False(t, elector2.IsLeader())

	// Close first elector (leader)
	elector1.Close()

	// Second should become leader
	require.Eventually(t, func() bool {
		return elector2.IsLeader()
	}, 35*time.Second, 500*time.Millisecond, "instance-2 should become leader after instance-1 closes")
}

func TestGlobalLeaderElector_Callbacks(t *testing.T) {
	elector, _ := newTestLeaderElector(t, "instance-1")

	var electedCalled, lostCalled bool

	elector.OnElected(func(ctx context.Context) error {
		electedCalled = true
		return nil
	})

	elector.OnLost(func(ctx context.Context) error {
		lostCalled = true
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start election
	err := elector.Start(ctx)
	require.NoError(t, err)

	// Should become leader and call OnElected
	require.Eventually(t, func() bool {
		return electedCalled
	}, 2*time.Second, 100*time.Millisecond)

	// Close should call OnLost
	elector.Close()
	require.True(t, lostCalled, "OnLost should be called on close")
}
