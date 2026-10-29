import { Module } from '@nestjs/common';
import { resolve } from 'node:path';
import { ConfigModule } from '@nestjs/config';
import { WebhookModule } from './modules/webhook/webhook.module';
import { WhatsAppModule } from './modules/whatsapp/whatsapp.module';
import { HealthModule } from './modules/health/health.module';
import { AgentModule } from './modules/agent/agent.module';
import { QueueModule } from './modules/queue/queue.module';
import configuration from './config/configuration';
import { validationSchema } from './config/validation';
import { PinoAppLogger } from './common/logger';

@Module({
  imports: [
    PinoAppLogger.register(),
    ConfigModule.forRoot({
      isGlobal: true,
      load: [configuration],
      validationSchema,
      validationOptions: {
        allowUnknown: true,
        abortEarly: false,
      },
      envFilePath: [resolve(process.cwd(), '.env')],
    }),
    WhatsAppModule,
    WebhookModule,
    HealthModule,
    QueueModule,
    AgentModule,
  ],
})
export class AppModule {}
