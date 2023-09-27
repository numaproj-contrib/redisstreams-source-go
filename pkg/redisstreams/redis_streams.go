package redisstreams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/numaproj-contrib/redisstreams-source-go/pkg/config"
	"github.com/numaproj-contrib/redisstreams-source-go/pkg/utils"
	sourcesdk "github.com/numaproj/numaflow-go/pkg/sourcer"
	"github.com/numaproj/numaflow/pkg/isb"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// redisStreamsSource is the struct responsible for processing an incoming RedisStream as part of a ConsumerGroup
type redisStreamsSource struct {
	stream       string
	group        string
	consumer     string
	checkBackLog bool

	*redisClient
	log *zap.SugaredLogger
}

// how to designate to Redis where the read in the stream is starting from (beginning or just new messages)
const ReadFromEarliest = "0-0"
const ReadFromLatest = "$"

func New(c *config.RedisStreamsSourceConfig, logger *zap.SugaredLogger) (*redisStreamsSource, error) {
	redisClient, err := newRedisClient(c)
	if err != nil {
		return nil, err
	}

	replica := os.Getenv("NUMAFLOW_REPLICA") // todo: check this was set correctly
	if replica == "" {
		return nil, fmt.Errorf("NUMAFLOW_REPLICA environment variable not set: can't set Consumer name")
	}

	consumerName := fmt.Sprintf("%s-%s", c.ConsumerGroup, replica)
	logger.Infof("Consumer group: %q, Consumer name: %q", c.ConsumerGroup, consumerName)

	redisStreamsSource := &redisStreamsSource{
		stream:       c.Stream,
		group:        c.ConsumerGroup,
		consumer:     consumerName,
		checkBackLog: true,
		redisClient:  redisClient,
		log:          logger,
	}

	// create the ConsumerGroup here if not already created
	err = redisStreamsSource.createConsumerGroup(context.Background(), c)
	if err != nil {
		return nil, err
	}

	return redisStreamsSource, nil
}

// create a RedisClient
func newRedisClient(c *config.RedisStreamsSourceConfig) (*redisClient, error) {
	volumeReader := utils.NewVolumeReader(utils.SecretVolumePath)
	password := ""
	if c.Password != nil {
		password, _ = volumeReader.GetSecretFromVolume(c.Password)
	}

	opts := &redis.UniversalOptions{
		Username:     c.User,       // can be empty
		Password:     password,     // can be empty
		MasterName:   c.MasterName, // can be empty
		MaxRedirects: 3,
	}
	if opts.MasterName != "" {
		urls := c.SentinelURL
		if urls != "" {
			opts.Addrs = strings.Split(urls, ",")
		}
		sentinelPassword, _ := volumeReader.GetSecretFromVolume(c.SentinelPassword)
		opts.SentinelPassword = os.Getenv(sentinelPassword)
	} else {
		urls := c.URL
		if urls != "" {
			opts.Addrs = strings.Split(urls, ",")
		}
	}

	return NewRedisClient(opts), nil
}

func (rsSource *redisStreamsSource) createConsumerGroup(ctx context.Context, c *config.RedisStreamsSourceConfig) error {
	// user can configure to read stream either from the beginning or from the most recent messages
	readFrom := ReadFromLatest
	if c.ReadFromBeginning {
		readFrom = ReadFromEarliest
	}
	rsSource.log.Infof("Creating Redis Stream group %q on Stream %q (readFrom=%v)", rsSource.group, rsSource.stream, readFrom)
	err := rsSource.redisClient.CreateStreamGroup(ctx, rsSource.stream, rsSource.group, readFrom)
	if err != nil {
		if IsAlreadyExistError(err) {
			rsSource.log.Infow("Consumer Group on Stream already exists.", zap.String("group", rsSource.group), zap.String("stream", rsSource.stream))
		} else {
			return fmt.Errorf("failed to create consumer group %q on redis stream %q: err=%v", rsSource.group, rsSource.stream, err)
		}
	}
	return nil
}

// Pending returns the number of pending records.
func (rsSource *redisStreamsSource) Pending(ctx context.Context) int64 {
	// try calling XINFO GROUPS <stream> and look for 'Lag' key.
	// For Redis Server < v7.0, this always returns 0; therefore it's recommended to use >= v7.0

	result := rsSource.Client.XInfoGroups(ctx, rsSource.stream)
	groups, err := result.Result()
	if err != nil {
		rsSource.log.Errorf("error calling XInfoGroups: %v", err)
		return isb.PendingNotAvailable
	}
	// find our ConsumerGroup
	for _, group := range groups {
		if group.Name == rsSource.group {
			return group.Lag
		}
	}
	rsSource.log.Errorf("ConsumerGroup %q not found in XInfoGroups result %+v", rsSource.group, groups)
	return isb.PendingNotAvailable

}

func (rsSource *redisStreamsSource) Read(ctx context.Context, readRequest sourcesdk.ReadRequest, messageCh chan<- sourcesdk.Message) {

	rsSource.log.Debugf("Ready to Read: count=%d, duration=%+v", readRequest.Count(), readRequest.TimeOut())

	functionStartTime := time.Now()
	finalTime := functionStartTime.Add(readRequest.TimeOut())
	msgCount := int(readRequest.Count())

	// if there are messages previously delivered to us that we didn't acknowledge, repeatedly check for that until there are no
	// messages returned, or until we reach timeout
	// "0-0" means messages previously delivered to us that we didn't acknowledge
	if rsSource.checkBackLog {
		xstreams, err := rsSource.processXReadResult(ctx, "0-0", int64(msgCount), readRequest.TimeOut(), messageCh)
		if err != nil {
			rsSource.log.Errorf("processXReadResult failed, checkBackLog=%v, err=%s", rsSource.checkBackLog, err)
			return
		}

		// NOTE: If all messages have been delivered and acknowledged, the XREADGROUP 0-0 call returns an empty
		// list of messages in the stream. At this point we want to read everything from last delivered which would be indicated by ">"
		if len(xstreams) == 1 && len(xstreams[0].Messages) == 0 {
			rsSource.log.Infow("We have delivered and acknowledged all PENDING msgs, setting checkBacklog to false")
			rsSource.checkBackLog = false
		} else {
			return
		}
	}

	// get undelivered messages up to the count we want and block until the timeout
	if !rsSource.checkBackLog {
		remainingTime := time.Until(finalTime)
		if int64(remainingTime) < 0 {
			rsSource.log.Infof("deletethis: checkBackLog=false; returning: out of time, finalTime=%+v", finalTime)
			return
		} else {
			rsSource.log.Infof("deletethis: checkBackLog=false; about to call processXReadResult(), finalTime=%+v, remainingTime=%+v", finalTime, remainingTime)
		}

		// this call will block until either the msgCount has been fulfilled or the remainingTime has elapsed
		// note: if we ever need true "streaming" here, we should instead repeatedly call with no timeout on each call
		_, err := rsSource.processXReadResult(ctx, ">", int64(msgCount), remainingTime, messageCh)
		if err != nil {
			rsSource.log.Errorf("processXReadResult failed, checkBackLog=%v, err=%s", rsSource.checkBackLog, err)
			return
		}
	}

}

// Ack acknowledges the data from the source.
func (rsSource *redisStreamsSource) Ack(ctx context.Context, request sourcesdk.AckRequest) {
	offsets := request.Offsets()
	strOffsets := make([]string, len(offsets))
	for i, o := range offsets {
		strOffsets[i] = string(o.Value())
	}
	if err := rsSource.Client.XAck(ctx, rsSource.stream, rsSource.group, strOffsets...).Err(); err != nil {
		rsSource.log.Errorf("Error performing Ack on offsets %+v: %v", offsets, err)
	}

}

func (rsSource *redisStreamsSource) Close() error {
	rsSource.log.Info("Shutting down redis streams source server...")
	rsSource.log.Info("Redis streams source server shutdown")
	return nil
}

// processXReadResult is used to process the results of XREADGROUP
// for any messages read, stream them back on message channel
func (rsSource *redisStreamsSource) processXReadResult(ctx context.Context, startIndex string, count int64, blockDuration time.Duration, messageCh chan<- sourcesdk.Message) ([]redis.XStream, error) {

	rsSource.log.Debugf("XReadGroup: startIndex=%q, count=%d, blockDuration=%+v", startIndex, count, blockDuration)

	result := rsSource.Client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    rsSource.group,
		Consumer: rsSource.consumer,
		Streams:  []string{rsSource.stream, startIndex},
		Count:    count,
		Block:    blockDuration,
	})
	xstreams, err := result.Result()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, redis.Nil) {
			rsSource.log.Debugf("redis.Nil/context cancelled, startIndex=%q, err=%v", startIndex, err)
			return nil, nil
		}
		return xstreams, err
	}
	if len(xstreams) > 1 {
		rsSource.log.Warnf("processXReadResult shouldn't return more than one Stream; xstreams=%+v", xstreams)
		return xstreams, nil
	} else if len(xstreams) == 1 {
		msgs, err := rsSource.xStreamToMessages(xstreams[0])
		if err != nil {
			return xstreams, err
		}

		for _, m := range msgs {
			messageCh <- *m
		}
	}

	return xstreams, nil
}

func (rsSource *redisStreamsSource) xStreamToMessages(xstream redis.XStream) ([]*sourcesdk.Message, error) {
	messages := []*sourcesdk.Message{}
	rsSource.log.Debugf("Read %d messages", len(xstream.Messages))
	for _, message := range xstream.Messages {
		var outMsg *sourcesdk.Message
		var err error
		if len(message.Values) >= 1 {
			outMsg, err = produceMsg(message)
			if err != nil {
				return nil, err
			}
		} else {
			// don't think there should be a message with no values, but if there is...
			rsSource.log.Warnf("unexpected: RedisStreams message has no values? message=%+v", message)
			continue
		}
		messages = append(messages, outMsg)
	}

	return messages, nil
}

func produceMsg(inMsg redis.XMessage) (*sourcesdk.Message, error) {
	var readOffset = sourcesdk.NewOffset([]byte(inMsg.ID), "0")

	jsonSerialized, err := json.Marshal(inMsg.Values)
	if err != nil {
		return nil, fmt.Errorf("failed to json serialize RedisStream values: %v; inMsg=%+v", err, inMsg)
	}
	var keys []string
	for k := range inMsg.Values {
		keys = append(keys, k)
	}

	msgTime, err := msgIdToTime(inMsg.ID)
	if err != nil {
		return nil, err
	}

	msg := sourcesdk.NewMessage(
		jsonSerialized,
		readOffset,
		msgTime).WithKeys(keys)

	return &msg, nil
}

// the ID of the message is formatted <msecTime>-<index>
func msgIdToTime(id string) (time.Time, error) {
	splitStr := strings.Split(id, "-")
	if len(splitStr) != 2 {
		return time.Time{}, fmt.Errorf("unexpected message ID value for Redis Streams: %s", id)
	}
	timeMsec, err := strconv.ParseInt(splitStr[0], 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	mSecRemainder := timeMsec % 1000
	t := time.Unix(timeMsec/1000, mSecRemainder*1000000)

	return t, nil

}
