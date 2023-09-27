package redisstreams

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

// redisClient datatype to hold redis client attributes.
type redisClient struct {
	Client redis.UniversalClient
}

// NewRedisClient returns a new Redis Client.
func NewRedisClient(options *redis.UniversalOptions) *redisClient {
	client := new(redisClient)
	client.Client = redis.NewUniversalClient(options)
	return client
}

// CreateStreamGroup creates a redis stream group and creates an empty stream if it does not exist.
func (cl *redisClient) CreateStreamGroup(ctx context.Context, stream string, group string, start string) error {
	return cl.Client.XGroupCreateMkStream(ctx, stream, group, start).Err()
}

// DeleteStreamGroup deletes the redis stream group.
func (cl *redisClient) DeleteStreamGroup(ctx context.Context, stream string, group string) error {
	return cl.Client.XGroupDestroy(ctx, stream, group).Err()
}

// DeleteKeys deletes a redis keys
func (cl *redisClient) DeleteKeys(ctx context.Context, keys ...string) error {
	return cl.Client.Del(ctx, keys...).Err()
}

// StreamInfo returns redis stream info
func (cl *redisClient) StreamInfo(ctx context.Context, streamKey string) (*redis.XInfoStream, error) {
	return cl.Client.XInfoStream(ctx, streamKey).Result()
}

// StreamGroupInfo returns redis stream group info
func (cl *redisClient) StreamGroupInfo(ctx context.Context, streamKey string) ([]redis.XInfoGroup, error) {
	return cl.Client.XInfoGroups(ctx, streamKey).Result()
}

// IsStreamExists check the redis keys exists
func (cl *redisClient) IsStreamExists(ctx context.Context, streamKey string) bool {
	_, err := cl.StreamInfo(ctx, streamKey)
	return err == nil
}

// PendingMsgCount returns how many messages are pending.
func (cl *redisClient) PendingMsgCount(ctx context.Context, streamKey, consumerGroup string) (int64, error) {
	cmd := cl.Client.XPending(ctx, streamKey, consumerGroup)
	pending, err := cmd.Result()
	if err != nil {
		return 0, err
	}
	return pending.Count, nil
}

// IsStreamGroupExists check the stream group exists
func (cl *redisClient) IsStreamGroupExists(ctx context.Context, streamKey string, groupName string) bool {
	result, err := cl.StreamGroupInfo(ctx, streamKey)
	if err != nil {
		return false
	}
	if len(result) == 0 {
		return false
	}
	for _, groupInfo := range result {
		if groupInfo.Name == groupName {
			return true
		}
	}
	return false
}

func IsAlreadyExistError(err error) bool {
	return strings.Contains(err.Error(), "BUSYGROUP")
}

func NotFoundError(err error) bool {
	return strings.Contains(err.Error(), "requires the key to exist")
}

func GetRedisStreamName(s string) string {
	return fmt.Sprintf("{%s}", s)
}
