# redisstreams-source-go

## Introduction
Redis Streams Source is a user-defined Source for [Numaflow](https://numaflow.numaproj.io/)
Not to be confused with the Redis database/cache, Redis Streams is a messaging system, or more accurately an append-only log akin to Kafka.
See [here](https://redis.io/docs/data-types/streams/) for more information.

Redis Streams Source can run with multiple Pods using a single `ConsumerGroup` and will in fact autoscale with load.

It can run with:

- Redis standalone
- Redis Sentinel
- Redis Cluster

### Keys and Values
Incoming messages may have a single Key/Value pair or multiple. In either case, the published message will have Keys equivalent to the incoming Key(s) and Payload equivalent to the JSON serialization of the map of keys to values.

Example:
If you have this Incoming message:

```
XADD * my-stream humidity 44 temperature 65
```

Then Outgoing message will be: 
```
Keys: ["humidity", "temperature"] Payload: {"humidity":"44","temperature":"65"}
```

## Quick Start
This quick start guide will help you to set up and run a Redis Streams source in a Numaflow pipeline on your local kube cluster. Follow the steps below to get started:

### Prerequisites
* Install Numaflow on your local kube cluster plus ISBSvc if not already present:

```bash
kubectl create ns numaflow-system
kubectl apply -n numaflow-system -f https://raw.githubusercontent.com/numaproj/numaflow/stable/config/install.yaml
kubectl apply -f https://raw.githubusercontent.com/numaproj/numaflow/stable/examples/0-isbsvc-jetstream.yaml
```

### Step-by-step Guide

#### 1. Deploy a Redis Streams Service

```bash
kubectl apply -f ./example/redis-minimal.yaml
```

#### 2. Create a ConfigMap which will be read by the Redis Streams Source to serve as its input file

```bash
kubectl apply -f ./example/configmap.yaml
```

#### 3. Deploy your pipeline and verify the Pipeline/Pods are running successfully

```bash
kubectl apply -f ./example/pipeline.yaml
kubectl get pipeline redis-source-e2e
kubectl get pods -l numaflow.numaproj.io/pipeline-name=redis-source-e2e
```

#### 4. Send a message to the Redis Streams Source on the Stream named "test-stream". Use the CLI in the Redis Server container.

```bash
kubectl exec -it redis-0 -- sh
> redis-cli
> XADD test-stream * temperature 69.4 humidity 61.0
```

#### 5. Verify that the message was received and propagated all the way to the Sink Vertex

You'll see a Pod whose name is prefixed by "redis-source-e2e-out-0-". Run `kubectl logs <podname>`.

You should see the message that got propagated:

```
Incremented by 1 the no. of occurrences of {"humidity":"61.0","temperature":"69.4"} under hash key redis-source-e2e:out
```

#### 6. Cleanup

```bash
kubectl delete -f ./example/pipeline.yaml
kubectl delete -f ./example/configmap.yaml
kubectl delete -f ./example/redis-minimal.yaml

```

## How to use the Redis Streams source in your own Numaflow pipeline

Currently, the configuration is mounted as a ConfigMap to the Pod(s) of your Vertex. You can create a ConfigMap similar to that of `./example/configmap.yaml` based on the `yaml` tags in the `RedisStreamsSourceConfig` struct you'll find in `pkg/config/config.go`.

If you look at what's available in the configuration there are references to any Secrets that you need, e.g.

```go
Password *corev1.SecretKeySelector `yaml:"password,omitempty" protobuf:"bytes,5,opt,name=password"
```

So, you'll need to create those as well. 

Take a look at the pipeline defined in `./example/pipeline.yaml`. The vertex named "in" is the Source Vertex that you'll need in your pipeline as well. As you can see the ConfigMap is referenced in the Volume for the Vertex, and that Volume is mounted to the Source Container. For any Secrets you have, you'll need to do something similar. (If mounting ConfigMaps and Secrets in Kubernetes Pods is new to you, you can find information about that online.)

### Debugging Redis Streams Source
To debug the NATS source, you can set the `NUMAFLOW_DEBUG` environment variable to `true` in the Redis Streams source container.
```yaml
source:
  udsource:
    container:
      image: quay.io/numaio/numaflow-source/redisstreams-source-go:v0.1.0
      env:
        - name: NUMAFLOW_DEBUG
          value: "true"
      volumeMounts:
        ...
```


## To run Unit tests
The Unit tests use the actual Redis Server, so Redis needs to be brought up independently in a Docker container. The Unit tests are listening on localhost:6379, so you can run the Redis Docker container and forward port 6379 to localhost:

```
docker run -p 6379:6379 -e REDIS_ARGS="--health-interval 10s --health-timeout 5s --health-retries 5" --name redis-server --rm redis
```

This needs to be restarted each time prior to running the test.