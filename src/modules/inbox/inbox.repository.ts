import { Injectable, Logger, OnApplicationShutdown } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { createHash } from 'node:crypto';
import Redis from 'ioredis';

@Injectable()
export class InboxRepository implements OnApplicationShutdown {
  private readonly client: Redis;
  private readonly logger = new Logger(InboxRepository.name);

  constructor(config: ConfigService) {
    this.client = new Redis(config.getOrThrow<string>('queue.redisUrl'), {
      maxRetriesPerRequest: 2,
      connectTimeout: 5000,
    });
    this.client.on('error', () =>
      this.logger.error('WhatsApp inbox storage unavailable'),
    );
  }

  private keys(sender: string) {
    const key = `eitri:inbox:{${createHash('sha256').update(sender).digest('hex')}}`;
    return [`${key}:pending`, `${key}:active`, `${key}:lock`, `${key}:seen`];
  }

  async enqueue(
    sender: string,
    messageId: string,
    body: string,
  ): Promise<boolean> {
    const keys = this.keys(sender);
    keys[3] += `:${createHash('sha256').update(messageId).digest('hex')}`;
    const result = await this.client.eval(
      `
      if redis.call('EXISTS', KEYS[4]) == 1 then return 1 end
      if redis.call('LLEN', KEYS[1]) >= 50 then return 0 end
      redis.call('RPUSH', KEYS[1], ARGV[2])
      redis.call('SET', KEYS[4], 1, 'EX', 86400)
      redis.call('EXPIRE', KEYS[1], 86400)
      return 1
    `,
      4,
      ...keys,
      messageId,
      body,
    );
    return Number(result) === 1;
  }

  async claim(sender: string, owner: string): Promise<string | null> {
    const result = await this.client.eval(
      `
      if redis.call('SET', KEYS[3], ARGV[1], 'NX', 'PX', 90000) == false then return false end
      local active = redis.call('GET', KEYS[2])
      if active then
        local batch = cjson.decode(active)
        batch.attempts = batch.attempts + 1
        active = cjson.encode(batch)
      else
        local messages = {}
        local size = 0
        for i = 1, 50 do
          if #messages >= 20 then break end
          local next = redis.call('LINDEX', KEYS[1], 0)
          if not next then break end
          local message = cjson.decode(next)
          local expired = tonumber(message.timestamp) * 1000 + 72000000 <= tonumber(ARGV[2])
          if not expired and #messages > 0 and size + 2 + string.len(message.text) > 4096 then break end
          redis.call('LPOP', KEYS[1])
          if not expired then
            table.insert(messages, message)
            size = size + 2 + string.len(message.text)
          end
        end
        if #messages == 0 then redis.call('DEL', KEYS[3]); return false end
        active = cjson.encode({messages=messages, attempts=1})
      end
      redis.call('SET', KEYS[2], active, 'EX', 86400)
      return active
    `,
      4,
      ...this.keys(sender),
      owner,
      Date.now(),
    );
    return typeof result === 'string' ? result : null;
  }

  async renew(sender: string, owner: string): Promise<boolean> {
    return (
      Number(
        await this.client.eval(
          `
      if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
      redis.call('PEXPIRE', KEYS[1], 90000)
      return 1
    `,
          1,
          this.keys(sender)[2],
          owner,
        ),
      ) === 1
    );
  }

  async complete(sender: string, owner: string): Promise<boolean> {
    return (
      Number(
        await this.client.eval(
          `
      if redis.call('GET', KEYS[2]) ~= ARGV[1] then return 0 end
      redis.call('DEL', KEYS[1], KEYS[2])
      return 1
    `,
          2,
          this.keys(sender)[1],
          this.keys(sender)[2],
          owner,
        ),
      ) === 1
    );
  }

  async release(sender: string, owner: string): Promise<void> {
    await this.client.eval(
      `
      if redis.call('GET', KEYS[1]) == ARGV[1] then redis.call('DEL', KEYS[1]) end
    `,
      1,
      this.keys(sender)[2],
      owner,
    );
  }

  onApplicationShutdown() {
    this.client.disconnect();
  }
}
