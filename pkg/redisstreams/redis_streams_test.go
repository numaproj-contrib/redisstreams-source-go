package redisstreams

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sourcesdk "github.com/numaproj/numaflow-go/pkg/sourcer"
	"github.com/stretchr/testify/assert"

	"github.com/redis/go-redis/v9"

	"github.com/numaproj-contrib/redisstreams-source-go/pkg/config"
	"github.com/numaproj-contrib/redisstreams-source-go/pkg/utils"
)

var (
	redisURI = ":6379"

	consumerGroupName = "my-group"

	redisOptions = &redis.UniversalOptions{
		Addrs: []string{redisURI},
	}

	multipleKeysValues             = map[string]string{"test-msg-1": "test-val-1", "test-msg-2": "test-val-2"}
	multipleKeysValuesJsonBytes, _ = json.Marshal(multipleKeysValues)
	multipleKeysValuesJson         = string(multipleKeysValuesJsonBytes)
	keysSet                        = map[string]struct{}{
		"test-msg-1": struct{}{},
		"test-msg-2": struct{}{},
	}

	defaultReadTimeout = 5 * time.Second
)

func init() {
	os.Setenv("NUMAFLOW_DEBUG", "true")
}

type readRequest struct {
	count   uint64
	timeout time.Duration
}

func (r *readRequest) TimeOut() time.Duration {
	return r.timeout
}

func (r *readRequest) Count() uint64 {
	return r.count
}

// ackRequest implements the AckRequest interface and is used in the ack handler.
type ackRequest struct {
	offsets []sourcesdk.Offset
}

// Offsets returns the offsets of the records to ack.
func (a *ackRequest) Offsets() []sourcesdk.Offset {
	return a.offsets
}

func Test_Read_MultiConsumer(t *testing.T) {

	streamName := "test-stream-multi-consumer"

	os.Setenv("NUMAFLOW_REPLICA", "1")
	config := &config.RedisStreamsSourceConfig{
		URL:               redisURI,
		Stream:            streamName,
		ConsumerGroup:     consumerGroupName,
		ReadFromBeginning: true,
	}
	source1, err := New(config, utils.NewLogger())
	assert.NoError(t, err)

	os.Setenv("NUMAFLOW_REPLICA", "2")
	source2, err := New(config, utils.NewLogger())
	assert.NoError(t, err)

	publishClient := NewRedisClient(redisOptions)
	writeTestMessages(t, publishClient, []string{"1692632086370-0", "1692632086371-0", "1692632086372-0"}, streamName)

	// Source reads the 1 message but doesn't Ack
	msgs := read(source1, 2, defaultReadTimeout)
	assert.Equal(t, 2, len(msgs))
	checkMessage(t, msgs[0], multipleKeysValuesJson, "1692632086370-0", keysSet)
	checkMessage(t, msgs[1], multipleKeysValuesJson, "1692632086371-0", keysSet)

	msgs = read(source2, 2, defaultReadTimeout)
	assert.Equal(t, 1, len(msgs))
	checkMessage(t, msgs[0], multipleKeysValuesJson, "1692632086372-0", keysSet)

}

func Test_Read_WithBacklog(t *testing.T) {
	os.Setenv("NUMAFLOW_REPLICA", "1")
	streamName := "test-stream-backlog"

	// new RedisStreamsSource with ConsumerGroup Reads but does not Ack
	config := &config.RedisStreamsSourceConfig{
		URL:               redisURI,
		Stream:            streamName,
		ConsumerGroup:     consumerGroupName,
		ReadFromBeginning: true,
	}
	source, err := New(config, utils.NewLogger())
	assert.NoError(t, err)

	// 1 new message published
	publishClient := NewRedisClient(redisOptions)
	writeTestMessages(t, publishClient, []string{"1692632086370-0"}, streamName)

	// Source reads the 1 message but doesn't Ack
	msgs := readDefault(source)
	assert.Equal(t, 1, len(msgs))

	// no Ack

	// another 2 messages published
	writeTestMessages(t, publishClient, []string{"1692632086371-0", "1692632086372-0"}, streamName)

	// second RedisStreamsSource with same ConsumerGroup, and same Consumer (imitating a Pod that got restarted) reads and gets
	// 1 backlog message, and on subsequent Read, gets 2 new messages
	// this time it Acks all messages
	source, err = New(config, utils.NewLogger())
	assert.NoError(t, err)
	msgs = readDefault(source)
	assert.Equal(t, 1, len(msgs))
	// ack
	offset := sourcesdk.NewOffset([]byte("1692632086370-0"), 0)
	source.Ack(context.Background(), &ackRequest{offsets: []sourcesdk.Offset{offset}})

	msgs = readDefault(source)
	assert.Equal(t, 2, len(msgs))
	offset = sourcesdk.NewOffset([]byte("1692632086371-0"), 0)
	source.Ack(context.Background(), &ackRequest{offsets: []sourcesdk.Offset{offset}})
	offset = sourcesdk.NewOffset([]byte("1692632086372-0"), 0)
	source.Ack(context.Background(), &ackRequest{offsets: []sourcesdk.Offset{offset}})

	// imitate the Pod getting restarted again: this time there should be no messages to read
	source, err = New(config, utils.NewLogger())
	assert.NoError(t, err)
	msgs = readDefault(source)
	assert.Equal(t, 0, len(msgs))

	// this time create a situation in which the number of Backlog messages can't be fully read in 1 call to Read()
	writeTestMessages(t, publishClient, []string{"1692632086373-0", "1692632086374-0", "1692632086375-0"}, streamName)
	msgs = readDefault(source)
	assert.Equal(t, 3, len(msgs))
	// no Ack

	// imitate the Pod getting restarted again
	source, err = New(config, utils.NewLogger())
	assert.NoError(t, err)
	msgs = read(source, 2, defaultReadTimeout) // just read 2 messages instead of all 3
	assert.Equal(t, 2, len(msgs))
	offset = sourcesdk.NewOffset([]byte("1692632086373-0"), 0)
	source.Ack(context.Background(), &ackRequest{offsets: []sourcesdk.Offset{offset}})
	offset = sourcesdk.NewOffset([]byte("1692632086374-0"), 0)
	source.Ack(context.Background(), &ackRequest{offsets: []sourcesdk.Offset{offset}})
	msgs = read(source, 2, defaultReadTimeout) // final message read
	assert.Equal(t, 1, len(msgs))
	offset = sourcesdk.NewOffset([]byte("1692632086375-0"), 0)
	source.Ack(context.Background(), &ackRequest{offsets: []sourcesdk.Offset{offset}})
	msgs = read(source, 1, defaultReadTimeout)
	assert.Equal(t, 0, len(msgs))

}

// returns the number of messages read in
func read(source *redisStreamsSource, count uint64, duration time.Duration) []sourcesdk.Message {
	msgChannel := make(chan sourcesdk.Message, 50)
	source.Read(context.Background(), &readRequest{count: count, timeout: duration}, msgChannel)
	close(msgChannel)
	return getMessages(msgChannel)
}

func readDefault(source *redisStreamsSource) []sourcesdk.Message {
	return read(source, 10, defaultReadTimeout)
}

func checkMessage(t *testing.T, msg sourcesdk.Message, payloadValue string, offset string, keysSet map[string]struct{}) {
	assert.Equal(t, payloadValue, string(msg.Value()))
	assert.Equal(t, offset, string(msg.Offset().Value()))
	splitOffset := strings.Split(offset, "-")
	assert.Equal(t, splitOffset[0], fmt.Sprintf("%d", msg.EventTime().UnixMilli()))
	assert.Equal(t, len(keysSet), len(msg.Keys()))
	for _, key := range msg.Keys() {
		assert.Contains(t, keysSet, key)
	}
}

func getMessages(msgChannel chan sourcesdk.Message) []sourcesdk.Message {
	msgs := make([]sourcesdk.Message, 0)
	for msg := range msgChannel {
		msgs = append(msgs, msg)
	}
	return msgs
}

func writeTestMessages(t *testing.T, publishClient *redisClient, ids []string, streamName string) {
	for _, id := range ids {
		err := publishClient.Client.XAdd(context.Background(), &redis.XAddArgs{
			ID:     id,
			Stream: streamName,
			Values: multipleKeysValues,
		}).Err()
		assert.NoError(t, err)
	}
}
