package miner

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/pocket-relay-miner/logging"
	"github.com/pokt-network/pocket-relay-miner/transport"
	redistransport "github.com/pokt-network/pocket-relay-miner/transport/redis"
)

// testMergedMsg pairs a stream message with the consumer that delivered it (for ACK routing).
type testMergedMsg struct {
	msg      transport.StreamMessage
	consumer *redistransport.StreamsConsumer
}

// TestMultiSourceConsumer_TwoRedisStreams verifies that a miner can consume from two
// Redis instances (e.g. EU + US), merge messages by supplier, and ACK each message
// to the correct source. Uses two miniredis instances to simulate two regions.
func TestMultiSourceConsumer_TwoRedisStreams(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.Disabled)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	mr1, err := miniredis.Run()
	require.NoError(t, err)
	defer mr1.Close()
	mr2, err := miniredis.Run()
	require.NoError(t, err)
	defer mr2.Close()

	url1 := fmt.Sprintf("redis://%s", mr1.Addr())
	url2 := fmt.Sprintf("redis://%s", mr2.Addr())

	client1, err := redistransport.NewClient(ctx, redistransport.ClientConfig{URL: url1})
	require.NoError(t, err)
	defer client1.Close()
	client2, err := redistransport.NewClient(ctx, redistransport.ClientConfig{URL: url2})
	require.NoError(t, err)
	defer client2.Close()

	logger := logging.NewLoggerFromConfig(logging.Config{Level: "debug", Format: "nop"})

	const (
		streamPrefix  = "ha:relays"
		supplierAddr  = "pokt1multisource123"
		consumerGroup = "miner"
		consumerName  = "miner-0"
		sessionID     = "session-1"
	)
	cacheTTL := 2 * time.Hour

	pub1 := redistransport.NewStreamsPublisher(logger, client1, streamPrefix, cacheTTL)
	defer pub1.Close()
	pub2 := redistransport.NewStreamsPublisher(logger, client2, streamPrefix, cacheTTL)
	defer pub2.Close()

	consumerConfig := transport.ConsumerConfig{
		StreamPrefix:            streamPrefix,
		SupplierOperatorAddress: supplierAddr,
		ConsumerGroup:            consumerGroup,
		ConsumerName:             consumerName,
		BatchSize:                10,
		ClaimIdleTimeout:         30000,
		MaxRetries:               3,
	}

	cons1, err := redistransport.NewStreamsConsumer(logger, client1, consumerConfig, time.Minute)
	require.NoError(t, err)
	defer cons1.Close()
	cons2, err := redistransport.NewStreamsConsumer(logger, client2, consumerConfig, time.Minute)
	require.NoError(t, err)
	defer cons2.Close()

	ch1 := cons1.Consume(ctx)
	ch2 := cons2.Consume(ctx)

	for i := 0; i < 3; i++ {
		m := &transport.MinedRelayMessage{
			SessionId:               sessionID,
			SessionEndHeight:        1000,
			SupplierOperatorAddress: supplierAddr,
			ServiceId:               "svc1",
			RelayHash:               []byte(fmt.Sprintf("hash1-%d", i)),
			RelayBytes:              []byte("relay1"),
			ComputeUnitsPerRelay:    1,
		}
		require.NoError(t, pub1.Publish(ctx, m))
	}
	for i := 0; i < 2; i++ {
		m := &transport.MinedRelayMessage{
			SessionId:               sessionID,
			SessionEndHeight:        1000,
			SupplierOperatorAddress: supplierAddr,
			ServiceId:               "svc1",
			RelayHash:               []byte(fmt.Sprintf("hash2-%d", i)),
			RelayBytes:              []byte("relay2"),
			ComputeUnitsPerRelay:    1,
		}
		require.NoError(t, pub2.Publish(ctx, m))
	}

	mergedCh := make(chan testMergedMsg, 20)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for m := range ch1 {
			select {
			case mergedCh <- testMergedMsg{msg: m, consumer: cons1}:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for m := range ch2 {
			select {
			case mergedCh <- testMergedMsg{msg: m, consumer: cons2}:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(mergedCh)
	}()

	var received int
	for m := range mergedCh {
		require.NoError(t, m.consumer.AckMessage(ctx, m.msg))
		received++
		if received == 5 {
			break
		}
	}
	require.Equal(t, 5, received, "should receive 3 from source1 + 2 from source2")

	time.Sleep(100 * time.Millisecond)

	p1, err := cons1.Pending(ctx)
	require.NoError(t, err)
	p2, err := cons2.Pending(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), p1, "source1 pending should be 0")
	require.Equal(t, int64(0), p2, "source2 pending should be 0")
}
