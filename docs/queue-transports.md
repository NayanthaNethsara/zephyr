# Queue transport contracts

The gateway exposes `QueueTransport.connect`, `publish`, `subscribe`, `check`, and `close`. Publication accepts a logical topic, a UTF-8 JSON body, and a stable message key. Subscription yields a delivery ID, body, and attempt count. Handlers resolve only after successful processing; rejection schedules redelivery. `QueueService` applies the same validation, maximum attempts, and dead-letter policy to every transport.

## Redis Streams

Redis 6.2 or later is required for `XAUTOCLAIM`; Compose uses Redis 7.4. Topic `whatsapp.outgoing` maps to `eitri:queue:{whatsapp.outgoing}` by default. `body` is the stream entry's JSON field. Publishers atomically add the entry and a deduplication key using Lua; both keys share a Redis Cluster hash tag. A dedicated subscriber connection performs blocking reads, leaving the publication connection available. `XREADGROUP` reads new work and `XAUTOCLAIM` recovers idle pending work. Heartbeats renew ownership without increasing delivery counts. Ownership is checked before renewal and completion. Successful handling executes acknowledgment and deletion together.

The default group is `eitri-whatsapp-gateway`; use the same group for all outgoing gateway replicas. Incoming workers may use a different group on the incoming topics. Each stream supports one logical worker fleet because acknowledged entries are deleted. To publish a quick outgoing example with `redis-cli`:

```sh
redis-cli XADD 'eitri:queue:{whatsapp.outgoing}' '*' body '{"to":"94771234567","type":"text","content":{"text":"hello:inventory_agent"}}'
```

That direct command bypasses publisher deduplication; production publishers should implement the Lua publication contract in `redis.transport.ts` with a stable message key. Incoming workers must acknowledge after durable processing and use pending recovery, renewal, retry limits, and deduplication. They should publish a reply before completing incoming work, using a reply key derived from the incoming ID.

References: [Redis consumer groups](https://redis.io/docs/latest/commands/xreadgroup/), [pending recovery](https://redis.io/docs/latest/commands/xautoclaim/), [dedicated blocking connections](https://redis.io/docs/latest/develop/use-cases/streaming/nodejs/).

## PostgreSQL

`gateway_queue_messages` stores transport messages independently of inventory. The uniqueness constraint on `(topic, message_key)` deduplicates publication. An atomic update selects one available row with `FOR UPDATE SKIP LOCKED`, increments its attempt, and grants a lease token. The database transaction ends before external HTTP work. Renewals and completion are conditional on that token; crashed workers leave an expiring lease that another replica can acquire. Completed rows remain for deduplication and are removed in bounded batches after the configured duration. Unprocessed and dead-letter rows are never automatically deleted.

External workers should follow the same claim/token/renew/complete contract, rather than holding a transaction open during model calls. Do not delete a pending row or mark it completed before its reply is durably published. A SQL transaction can atomically publish a reply and complete an incoming row when both use the same PostgreSQL queue.

Reference: [PostgreSQL locking clauses and queue use of SKIP LOCKED](https://www.postgresql.org/docs/current/sql-select.html#SQL-FOR-UPDATE-SHARE).

## NATS JetStream

The default stream is `EITRI_WHATSAPP_EVENTS`. It covers configured incoming/outgoing topics and their `.dead` subjects. New streams use file storage and work-queue retention. Durable pull consumer names are formed from the group and topic with punctuation replaced by underscores. Unlike the previous Core NATS subscription, they retain work through gateway downtime. Consumers acknowledge after delivery, send progress acknowledgments while working, and negatively acknowledge transient failures with a retry delay. Publication sets `Nats-Msg-Id` to `<topic>:<message-key>`. The duplicate window follows `QUEUE_DEDUP_SECONDS`.

External workers must use JetStream publication and durable consumers. Core NATS publication does not provide the persisted publish acknowledgment expected by this interface. Existing stream and consumer configuration should be reviewed before reusing an old stream; changing a provider does not migrate it.

References: [JetStream durability](https://docs.nats.io/concepts/jetstream), [consumer acknowledgment policies](https://github.com/nats-io/nats.docs/blob/master/nats-concepts/jetstream/consumers.md).
