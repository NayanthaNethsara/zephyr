import { Logger } from '@nestjs/common';
import { setTimeout } from 'node:timers/promises';
import { QueueSubscription } from './queue.types';

export class QueueWorker implements QueueSubscription {
  private stopped = false;
  private readonly task: Promise<void>;

  constructor(
    work: () => Promise<void>,
    logger: Logger,
    private readonly interrupt: () => void = () => {},
  ) {
    this.task = (async () => {
      while (!this.stopped) {
        try {
          await work();
        } catch (error) {
          if (!this.stopped) {
            const errorDetails =
              error instanceof Error
                ? error.stack || error.message
                : String(error);
            logger.error(`Queue worker failed: ${errorDetails}`);
            await setTimeout(1000);
          }
        }
      }
    })();
  }

  async stop(): Promise<void> {
    this.stopped = true;
    this.interrupt();
    await this.task;
  }
}
